package signer

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// errEvmStateNotConfigured: without the state store no EVM transaction is
// signed (fail closed, D1).
var errEvmStateNotConfigured = stderrors.New("signer EVM state store is not configured")

// blockNumberFn reads the latest block, stored with each signed transaction
// as the scan start for manual checks.
var blockNumberFn = func(ctx context.Context, evmClient *ethclient.Client) (uint64, error) {
	return evmClient.BlockNumber(ctx)
}

// evmScopeMemory is the little state kept in memory per scope. The DB stays
// the only source of truth; these only remember what the DB cannot.
//
// Across lease epochs, a refusal in terminalRetry is dropped (another holder
// may have re-sent the bytes) and the nonce cache and lastSeenPending are
// re-synced. A receipt-backed terminalRetry, consumeUnknown and awaiting are
// kept: they rest on a receipt and this signer's own committed audit, which
// no later holder can undo, and dropping them would let the operation be
// signed again.
type evmScopeMemory struct {
	// epoch is the lease epoch the nonce cache and lastSeenPending were
	// last synced for; a request under another epoch syncs them first.
	epoch uint64
	// terminalRetry holds the evidence of a terminal transition whose write
	// failed. A refusal's error string cannot be fetched again, so it must
	// be retried before anything else is signed in the scope.
	terminalRetry *evmTerminalRetry
	// consumeUnknown is a succeeded outcome whose consume commit result is
	// unknown and could not be read back.
	consumeUnknown *outcomeRecord
	// awaiting maps operation to result hash for results already taken out
	// of the DB while the caller got an error; the next request for the
	// operation gets the hash. Lost on restart, like a lost response (plan risk R6).
	awaiting map[ethcmn.Hash]ethcmn.Hash
	// lastSeenPending is the id of the pending row last read or written by
	// this process; finding the scope empty with it set means the row was
	// removed by someone else (the CLI).
	lastSeenPending uint64
	// autoSeen is the first sighting of an automatic conclusion that waits
	// for a second one at a higher height (evm_state_auto.go).
	autoSeen *evmAutoObservation
}

type evmTerminalRetry struct {
	pending  pendingRecord
	terminal pendingTerminal
	// epoch is the lease epoch the transition was observed under. A
	// refusal only holds within it: under another epoch a different holder
	// may have re-sent the recorded bytes.
	epoch uint64
}

// evmState is the signer's view of the EVM state store.
type evmState struct {
	store      evmStateStore
	chainID    string
	evmChainID uint64
	lease      *evmLease

	// spawn runs recovery work off the calling goroutine; workers tracks it
	// so Stop can wait before closing the DB.
	spawn   func(func())
	workers sync.WaitGroup

	mu      sync.Mutex
	scopes  map[SignType]*evmScopeMemory
	audited map[string]bool // evidence_invalid already written for this row
	// outcomeChecked is when each blocked outcome was last checked on chain.
	outcomeChecked map[uint64]time.Time
}

func newEvmState(store evmStateStore, chainID string, evmChainID uint64, lease *evmLease) *evmState {
	st := &evmState{
		store: store, chainID: chainID, evmChainID: evmChainID, lease: lease,
		scopes: map[SignType]*evmScopeMemory{}, audited: map[string]bool{}, outcomeChecked: map[uint64]time.Time{},
	}
	st.spawn = func(f func()) {
		st.workers.Add(1)
		go func() {
			defer st.workers.Done()
			f()
		}()
	}
	return st
}

// waitWorkers waits for spawned recovery work, at most until ctx ends.
func (st *evmState) waitWorkers(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		st.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (st *evmState) memory(scope SignType) *evmScopeMemory {
	st.mu.Lock()
	defer st.mu.Unlock()
	mem, ok := st.scopes[scope]
	if !ok {
		mem = &evmScopeMemory{awaiting: map[ethcmn.Hash]ethcmn.Hash{}}
		st.scopes[scope] = mem
	}
	return mem
}

func (st *evmState) setLastSeen(scope SignType, id uint64) {
	mem := st.memory(scope)
	st.mu.Lock()
	mem.lastSeenPending = id
	st.mu.Unlock()
}

func (st *evmState) clearLastSeen(scope SignType, id uint64) {
	mem := st.memory(scope)
	st.mu.Lock()
	if mem.lastSeenPending == id {
		mem.lastSeenPending = 0
	}
	st.mu.Unlock()
}

// verify runs V(e) and also requires the evidence to belong to the EVM chain
// this signer is configured for.
func (st *evmState) verify(evidence *signedTxEvidence, ref evidenceRef) (*types.Transaction, error) {
	tx, err := verifySignedTxEvidence(evidence, ref)
	if err != nil {
		return nil, err
	}
	if evidence.EvmChainID != st.evmChainID {
		return nil, fmt.Errorf("%w: row %d evm_chain_id=%d, signer EVM chain id is %d", errEvmEvidenceInvalid,
			evidence.ID, evidence.EvmChainID, st.evmChainID)
	}
	return tx, nil
}

// loadEvidence reads and verifies the evidence of a referencing row. A read
// error is returned as is; a missing or invalid row as errEvmEvidenceInvalid,
// audited once per row.
func (st *evmState) loadEvidence(ctx context.Context, fence *evmFence, kind string, rowID uint64, ref evidenceRef,
	event evmAuditEvent,
) (*types.Transaction, error) {
	evidence, err := st.store.GetSignedTx(ctx, ref.SignedTxID)
	if err != nil {
		return nil, fmt.Errorf("failed to read EVM signed transaction evidence %d: %w", ref.SignedTxID, err)
	}
	tx, err := st.verify(evidence, ref)
	if err == nil {
		return tx, nil
	}
	key := fmt.Sprintf("%s:%d", kind, rowID)
	st.mu.Lock()
	seen := st.audited[key]
	st.mu.Unlock()
	log.CtxErrorw(ctx, "EVM signed transaction evidence is invalid; the row stays blocked until handled manually",
		"row", key, "error", err)
	if !seen {
		event.Event = evmAuditEvidenceInvalid
		event.Detail = err.Error()
		wctx, cancel := evmStateCtx(ctx)
		auditErr := st.store.AppendAudit(wctx, fence, st.chainID, event)
		cancel()
		if auditErr == nil {
			st.mu.Lock()
			st.audited[key] = true
			st.mu.Unlock()
		}
	}
	return nil, err
}

// keyAddress is the EVM address of the scope's current key.
func (client *MocaChainSignClient) keyAddress(scope SignType) (ethcmn.Address, bool) {
	key, ok := client.evmPrivateKeys[scope]
	if !ok || key == nil {
		return ethcmn.Address{}, false
	}
	return crypto.PubkeyToAddress(key.PublicKey), true
}

// nonceCache is the cached next nonce of the scope's account. Callers hold
// the scope lock.
func (client *MocaChainSignClient) nonceCache(scope SignType) *uint64 {
	switch scope {
	case SignOperator:
		return &client.operatorAccNonce
	case SignSeal:
		return &client.sealAccNonce
	case SignGc:
		return &client.gcAccNonce
	}
	return nil
}

func (client *MocaChainSignClient) scopeLock(scope SignType) *sync.Mutex {
	switch scope {
	case SignOperator:
		return &client.opLock
	case SignSeal:
		return &client.sealLock
	case SignGc:
		return &client.gcLock
	}
	return nil
}

// refreshNonceFromChain resets the cache from the chain only, without the
// pending lower bound. Callers hold the scope lock.
func (client *MocaChainSignClient) refreshNonceFromChain(ctx context.Context, scope SignType) error {
	cache := client.nonceCache(scope)
	if cache == nil {
		return nil
	}
	nonce, err := getCosmosNonceFn(client.mocaClients[scope], ctx)
	if err != nil {
		return fmt.Errorf("failed to refresh the %s account nonce: %w", scope, err)
	}
	*cache = nonce
	return nil
}

// resolvePendingEvmTx decides, under the scope lock, whether a request for
// operation may be signed. handled means the request is answered here: with
// the hash of an earlier result, or with an error that keeps the scope or
// operation blocked. An empty operation (background resolution) never takes
// a result.
func (client *MocaChainSignClient) resolvePendingEvmTx(ctx context.Context, scope SignType,
	operation ethcmn.Hash,
) (string, bool, error) {
	st := client.evm
	if st == nil {
		return "", true, errEvmStateNotConfigured
	}
	fence, ok := st.lease.fence()
	if !ok {
		return "", true, errEvmLeaseNotHeld
	}
	syncErr := client.syncEvmScopeEpoch(ctx, st, fence, scope, nil)
	if syncErr != nil && !stderrors.Is(syncErr, errEvmNonceUnsynced) {
		return "", true, syncErr
	}
	hash, handled, err := client.resolveEvmState(ctx, st, fence, scope, operation, 0)
	if handled || syncErr == nil {
		return hash, handled, err
	}
	// Answering from the state needs no nonce; signing does.
	return "", true, syncErr
}

// errEvmNonceUnsynced: the scope's nonce cache could not be reset for the
// current lease epoch, so nothing new is signed in it.
var errEvmNonceUnsynced = stderrors.New("EVM account nonce not synced for the current lease epoch")

// syncEvmScopeEpoch resets what the scope remembers about the account when
// fence is a lease epoch the scope has not seen yet: the nonce cache, from
// the chain with the pending lower bound, and lastSeenPending. pending is the
// scope's row when the caller has already read it; nil reads it. Callers hold
// the scope lock. On error the scope stays unsynced and nothing is signed.
func (client *MocaChainSignClient) syncEvmScopeEpoch(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	pending *pendingRecord,
) error {
	mem := st.memory(scope)
	st.mu.Lock()
	synced := mem.epoch == fence.Epoch
	st.mu.Unlock()
	if synced {
		return nil
	}
	sctx, cancel := evmStateCtx(ctx)
	defer cancel()
	if pending == nil {
		current, err := st.store.GetPending(sctx, st.chainID, scope)
		if err != nil {
			return fmt.Errorf("failed to read EVM pending state: %w", err)
		}
		pending = current
	}
	st.mu.Lock()
	mem.lastSeenPending = 0
	if pending != nil {
		mem.lastSeenPending = pending.ID
	}
	st.mu.Unlock()
	if cache := client.nonceCache(scope); cache != nil && client.mocaClients[scope] != nil {
		nonce, err := getCosmosNonceFn(client.mocaClients[scope], sctx)
		if err != nil {
			return fmt.Errorf("%w: scope %s, epoch %d: %w", errEvmNonceUnsynced, scope, fence.Epoch, err)
		}
		// A pending row of an old key does not bound the new key's nonce (D4).
		if addr, ok := client.keyAddress(scope); pending != nil && ok && addr == pending.From && pending.Nonce+1 > nonce {
			nonce = pending.Nonce + 1
		}
		*cache = nonce
	}
	st.mu.Lock()
	mem.epoch = fence.Epoch
	st.mu.Unlock()
	return nil
}

// evmResolveMaxDepth bounds how often one request re-reads the state after a
// transition: each automatic step (ending a pending row, settling an outcome)
// is progress and reads it again.
const evmResolveMaxDepth = 4

func (client *MocaChainSignClient) resolveEvmState(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	operation ethcmn.Hash, depth int,
) (string, bool, error) {
	if depth > evmResolveMaxDepth {
		return "", true, fmt.Errorf("EVM state for scope %s keeps changing; retry later", scope)
	}
	rctx, cancel := evmStateCtx(ctx)
	defer cancel()

	// Step 2: retry what only memory knows.
	if hash, handled, err := client.retryEvmMemory(rctx, st, fence, scope, operation); handled {
		return hash, true, err
	}

	// Step 3: an active outcome for this operation.
	if operation != (ethcmn.Hash{}) {
		outcome, err := st.store.GetOutcome(rctx, st.chainID, scope, operation)
		if err != nil {
			return "", true, fmt.Errorf("failed to read EVM outcome state: %w", err)
		}
		if outcome != nil {
			if _, err := st.loadEvidence(rctx, &fence, "outcome", outcome.ID, outcome.ref(), outcome.audit("")); err != nil {
				return "", true, err
			}
			switch outcome.State {
			case evmOutcomeSucceeded:
				return client.consumeOutcome(rctx, st, fence, scope, operation, *outcome, depth)
			case evmOutcomeExecutedElsewhere:
				return "", true, fmt.Errorf("EVM operation %s was executed by transaction %s (%s); outcome %d stays until it is disposed manually",
					operation.Hex(), outcome.EvidenceTxHash, outcome.EvidenceKind, outcome.ID)
			case evmOutcomeBlockedUnknown:
				settled, settleErr := client.autoSettleOutcome(rctx, st, fence, *outcome)
				if settled {
					return client.resolveEvmState(ctx, st, fence, scope, operation, depth+1)
				}
				if settleErr != nil {
					log.CtxWarnw(ctx, "failed to check a blocked EVM outcome on chain", "outcome_id", outcome.ID, "error", settleErr)
				}
				return "", true, fmt.Errorf("EVM operation %s is blocked: outcome_id=%d reason=%s; it stays blocked until the chain decides it or it is handled manually (see 'signer-evm list')",
					operation.Hex(), outcome.ID, outcome.Reason)
			default:
				return "", true, fmt.Errorf("%w: outcome %d has unknown state %q", errEvmStateConflict, outcome.ID, outcome.State)
			}
		}
	}

	// Step 4: the scope's pending transaction.
	pending, err := st.store.GetPending(rctx, st.chainID, scope)
	if err != nil {
		return "", true, fmt.Errorf("failed to read EVM pending state: %w", err)
	}
	mem := st.memory(scope)
	if pending == nil {
		st.mu.Lock()
		lastSeen := mem.lastSeenPending
		st.mu.Unlock()
		if lastSeen != 0 {
			if err := st.store.AppendAudit(rctx, &fence, st.chainID, evmAuditEvent{
				Event: evmAuditPendingVanished, Scope: scope, Detail: fmt.Sprintf("pending row %d was removed outside this signer", lastSeen),
			}); err != nil {
				return "", true, fmt.Errorf("failed to audit the vanished EVM pending row: %w", err)
			}
			if err := client.refreshNonceFromChain(ctx, scope); err != nil {
				return "", true, err
			}
			st.clearLastSeen(scope, lastSeen)
			log.CtxWarnw(ctx, "EVM pending row was removed outside this signer; nonce refreshed from chain",
				"scope", scope, "pending_id", lastSeen)
		}
		return "", false, nil
	}
	st.setLastSeen(scope, pending.ID)
	signedTx, err := st.loadEvidence(rctx, &fence, "pending", pending.ID, pending.ref(), pending.audit(""))
	if err != nil {
		return pending.TxHash.String(), true, err
	}

	qctx, cancelQuery := context.WithTimeout(ctx, evmChainQueryTimeout)
	receipt, err := transactionReceiptFn(qctx, client.evmClient, pending.TxHash)
	cancelQuery()
	if err != nil || receipt == nil {
		moved, sent, autoErr := client.autoDisposePending(ctx, st, fence, scope, *pending, signedTx)
		if moved {
			return client.resolveEvmState(ctx, st, fence, scope, operation, depth+1)
		}
		if autoErr != nil {
			log.CtxWarnw(ctx, "failed to check the EVM pending transaction on chain", "scope", scope,
				"tx_hash", pending.TxHash.Hex(), "error", autoErr)
		}
		// The bytes may never have reached a node (cancelled mid-send, lost
		// before forwarding). Re-sending the identical transaction can execute
		// at most once, so it is safe and keeps the scope from blocking forever.
		if !sent {
			client.rebroadcastPendingEvmTx(ctx, scope, *pending, signedTx)
		}
		reason := "receipt unavailable"
		if err != nil {
			reason = err.Error()
		}
		return pending.TxHash.String(), true, fmt.Errorf(
			"EVM transaction submitted but unconfirmed: scope=%s nonce=%d hash=%s ambiguous=%t: %s",
			scope, pending.Nonce, pending.TxHash.Hex(), pending.Ambiguous, reason)
	}

	terminal := pendingTerminal{Kind: terminalFailed, ReceiptStatus: uint64Ptr(receipt.Status), Source: evmOutcomeSourceResolve}
	if receipt.BlockNumber != nil {
		terminal.ReceiptBlock = receipt.BlockNumber.Uint64()
	}
	if receipt.Status == types.ReceiptStatusSuccessful {
		terminal.Kind = terminalSuccessKeep
		if pending.Operation == operation {
			terminal.Kind = terminalSuccessConsume
		}
	}
	err = st.applyTerminal(rctx, fence, *pending, terminal)
	switch {
	case err == nil:
	case stderrors.Is(err, errEvmRowGone):
		return client.resolveEvmState(ctx, st, fence, scope, operation, depth+1)
	default:
		st.stashTerminal(fence, scope, *pending, terminal)
		return "", true, fmt.Errorf("EVM transaction %s reached a terminal state that could not be recorded: %w", pending.TxHash.Hex(), err)
	}
	if terminal.Kind == terminalSuccessConsume {
		return pending.TxHash.String(), true, nil
	}
	return "", false, nil
}

// consumeOutcome hands a succeeded outcome to the request for its operation.
func (client *MocaChainSignClient) consumeOutcome(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	operation ethcmn.Hash, outcome outcomeRecord, depth int,
) (string, bool, error) {
	err := st.store.ConsumeOutcome(ctx, fence, outcome)
	switch {
	case err == nil:
		return outcome.ResultTxHash.String(), true, nil
	case stderrors.Is(err, errEvmRowGone):
		return client.resolveEvmState(ctx, st, fence, scope, operation, depth+1)
	case isEvmCommitUnknown(err):
		consumed, readErr := st.readBackConsumed(ctx, outcome)
		if readErr == nil && consumed {
			return outcome.ResultTxHash.String(), true, nil
		}
		mem := st.memory(scope)
		st.mu.Lock()
		mem.consumeUnknown = &outcome
		st.mu.Unlock()
	}
	return "", true, fmt.Errorf("failed to take EVM outcome %d: %w", outcome.ID, err)
}

// readBackConsumed reports whether the consume of outcome committed: the row
// is gone and its outcome_consumed audit exists.
func (st *evmState) readBackConsumed(ctx context.Context, outcome outcomeRecord) (bool, error) {
	current, err := st.store.GetOutcome(ctx, st.chainID, outcome.Scope, outcome.Operation)
	if err != nil {
		return false, err
	}
	if current != nil && current.ID == outcome.ID {
		return false, nil
	}
	return st.store.HasAudit(ctx, st.chainID, evmAuditOutcomeConsumed, ethcmn.Hash{}, outcome.ID)
}

// applyTerminal runs ResolvePending and, when its commit result is unknown,
// reads the state back: the row gone with the transition's audit present
// counts as done.
func (st *evmState) applyTerminal(ctx context.Context, fence evmFence, pending pendingRecord, terminal pendingTerminal) error {
	_, err := st.store.ResolvePending(ctx, fence, pending, terminal)
	if isEvmCommitUnknown(err) {
		done, readErr := st.readBackResolved(ctx, pending, terminal)
		if readErr == nil && done {
			err = nil
		} else if readErr == nil && !done {
			log.CtxErrorw(ctx, "EVM state commit outcome unknown and not visible on read back", "tx_hash", pending.TxHash.Hex(), "error", err)
		}
	}
	if err == nil {
		st.clearLastSeen(pending.Scope, pending.ID)
	}
	return err
}

func (st *evmState) readBackResolved(ctx context.Context, pending pendingRecord, terminal pendingTerminal) (bool, error) {
	current, err := st.store.GetPending(ctx, st.chainID, pending.Scope)
	if err != nil {
		return false, err
	}
	if current != nil && current.ID == pending.ID {
		return false, nil
	}
	return st.store.HasAudit(ctx, st.chainID, terminal.Kind.doneEvent(), pending.TxHash, 0)
}

// landedTerminal finds which transition of pending committed, by its audit:
// for a success either kind, since a retry may have tried the other one.
func (st *evmState) landedTerminal(ctx context.Context, pending pendingRecord, kind pendingTerminalKind) (pendingTerminalKind, bool, error) {
	kinds := []pendingTerminalKind{kind}
	if kind == terminalSuccessConsume || kind == terminalSuccessKeep {
		kinds = []pendingTerminalKind{terminalSuccessConsume, terminalSuccessKeep}
	}
	for _, k := range kinds {
		landed, err := st.store.HasAudit(ctx, st.chainID, k.doneEvent(), pending.TxHash, 0)
		if err != nil {
			return kind, false, err
		}
		if landed {
			return k, true, nil
		}
	}
	return kind, false, nil
}

func (st *evmState) stashTerminal(fence evmFence, scope SignType, pending pendingRecord, terminal pendingTerminal) {
	mem := st.memory(scope)
	st.mu.Lock()
	mem.terminalRetry = &evmTerminalRetry{pending: pending, terminal: terminal, epoch: fence.Epoch}
	st.mu.Unlock()
}

// retryEvmMemory is step 2: before anything else in the scope, retry the
// terminal transition or consume whose write failed, and hand over results
// already taken from the DB.
func (client *MocaChainSignClient) retryEvmMemory(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	operation ethcmn.Hash,
) (string, bool, error) {
	mem := st.memory(scope)
	st.mu.Lock()
	retry, consume := mem.terminalRetry, mem.consumeUnknown
	if retry != nil && retry.terminal.Kind.notAdmitted() && retry.epoch != fence.Epoch {
		// The refusal was seen under an earlier lease. Another holder may
		// have re-sent the recorded bytes since, so the row is resolved
		// from the chain like any other pending row (step 4).
		mem.terminalRetry, retry = nil, nil
	}
	st.mu.Unlock()

	if retry != nil {
		terminal := retry.terminal
		if terminal.Kind == terminalSuccessConsume && retry.pending.Operation != operation {
			// Only a request for the row's own operation may take the hash
			// without a kept row. A stashed keep is never turned into a
			// consume: it may have failed on an active outcome (I1), and
			// step 3 consumes the kept row anyway.
			terminal.Kind = terminalSuccessKeep
		}
		applied := true
		err := st.applyTerminal(ctx, fence, retry.pending, terminal)
		if stderrors.Is(err, errEvmRowGone) {
			// Either an earlier unknown commit did land, or the CLI handled
			// the row. Only our own audit tells which; an earlier attempt
			// may have been either success kind.
			landed, ok, readErr := st.landedTerminal(ctx, retry.pending, retry.terminal.Kind)
			if readErr != nil {
				return "", true, fmt.Errorf("failed to read back EVM state: %w", readErr)
			}
			err = nil
			switch {
			case !ok:
				// Handled outside the signer; step 4 notices the vanished row.
				applied = false
			case landed == terminalSuccessConsume:
				// The earlier commit took the hash out of the DB.
				st.mu.Lock()
				mem.awaiting[retry.pending.Operation] = retry.pending.TxHash
				st.mu.Unlock()
				applied = false
			default:
				terminal.Kind = landed
			}
		}
		if err != nil {
			return "", true, fmt.Errorf("EVM transaction %s reached a terminal state that still cannot be recorded: %w",
				retry.pending.TxHash.Hex(), err)
		}
		st.mu.Lock()
		mem.terminalRetry = nil
		st.mu.Unlock()
		if applied {
			switch terminal.Kind {
			case terminalSuccessConsume:
				return retry.pending.TxHash.String(), true, nil
			case terminalNotAdmittedNonceLow, terminalNotAdmittedNonceHigh:
				// The node refused the nonce: refresh before signing again.
				if err := client.refreshNonceFromChain(ctx, scope); err != nil {
					return "", true, err
				}
			}
		}
	}

	if consume != nil {
		current, err := st.store.GetOutcome(ctx, st.chainID, consume.Scope, consume.Operation)
		if err != nil {
			return "", true, fmt.Errorf("failed to read back EVM outcome %d: %w", consume.ID, err)
		}
		if current == nil || current.ID != consume.ID {
			consumed, err := st.store.HasAudit(ctx, st.chainID, evmAuditOutcomeConsumed, ethcmn.Hash{}, consume.ID)
			if err != nil {
				return "", true, fmt.Errorf("failed to read back EVM outcome %d: %w", consume.ID, err)
			}
			if consumed {
				st.mu.Lock()
				mem.awaiting[consume.Operation] = consume.ResultTxHash
				st.mu.Unlock()
			}
		}
		st.mu.Lock()
		mem.consumeUnknown = nil
		st.mu.Unlock()
	}

	if operation != (ethcmn.Hash{}) {
		st.mu.Lock()
		hash, ok := mem.awaiting[operation]
		delete(mem.awaiting, operation)
		st.mu.Unlock()
		if ok {
			return hash.String(), true, nil
		}
	}
	return "", false, nil
}

func (client *MocaChainSignClient) rebroadcastPendingEvmTx(ctx context.Context, scope SignType, pending pendingRecord,
	signedTx *types.Transaction,
) {
	if signedTx == nil || ctx.Err() != nil {
		return
	}
	resendCtx, cancel := context.WithTimeout(ctx, evmResendTimeout)
	defer cancel()
	err := sendEvmTxFn(resendCtx, client.evmClient, signedTx)
	log.CtxWarnw(ctx, "re-broadcast unconfirmed EVM tx; check the hash on chain before any manual action",
		"scope", scope, "nonce", pending.Nonce, "tx_hash", pending.TxHash.Hex(), "operation", pending.Operation.Hex(), "error", err)
}

// recordPendingEvmTx persists the evidence and the pending row before the
// first send. Nothing may be sent unless this returns nil.
func (client *MocaChainSignClient) recordPendingEvmTx(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	operation ethcmn.Hash, signedTx *types.Transaction, raw []byte,
) (pendingRecord, error) {
	if signedTx.ChainId() == nil || !signedTx.ChainId().IsUint64() {
		return pendingRecord{}, fmt.Errorf("EVM transaction has no chain id")
	}
	from, err := types.Sender(types.LatestSignerForChainID(signedTx.ChainId()), signedTx)
	if err != nil {
		return pendingRecord{}, fmt.Errorf("failed to recover the EVM transaction sender: %w", err)
	}
	block, err := blockNumberFn(ctx, client.evmClient)
	if err != nil {
		return pendingRecord{}, fmt.Errorf("failed to read the latest block before recording the EVM transaction: %w", err)
	}
	txHash := signedTx.Hash()
	evidence := signedTxEvidence{
		ChainID: st.chainID, EvmChainID: signedTx.ChainId().Uint64(), Scope: scope, Operation: operation,
		From: from, Nonce: signedTx.Nonce(), TxHash: txHash, Raw: raw, RecordedBlock: block,
	}
	pending := pendingRecord{
		ChainID: st.chainID, Scope: scope, From: from, Nonce: signedTx.Nonce(), TxHash: txHash, Operation: operation,
	}
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	recorded, err := st.store.Record(wctx, fence, evidence, pending)
	if isEvmCommitUnknown(err) {
		current, readErr := st.store.GetPending(wctx, st.chainID, scope)
		if readErr == nil && current != nil && current.TxHash == txHash {
			recorded, err = *current, nil
		}
	}
	if err != nil {
		return pendingRecord{}, fmt.Errorf("failed to record the EVM transaction before sending: %w", err)
	}
	st.setLastSeen(scope, recorded.ID)
	return recorded, nil
}

// markPendingEvmTxAmbiguous is informational: the scope stays blocked by the
// pending row whether or not the flag is written.
func (client *MocaChainSignClient) markPendingEvmTxAmbiguous(ctx context.Context, st *evmState, fence evmFence,
	pending pendingRecord,
) {
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	if err := st.store.MarkAmbiguous(wctx, fence, pending); err != nil && !isEvmCommitUnknown(err) {
		log.CtxErrorw(ctx, "failed to mark the EVM pending transaction ambiguous", "tx_hash", pending.TxHash.Hex(), "error", err)
	}
}

// finishPendingEvmTx ends a pending row from submitEvmTx. On failure the
// evidence is kept for the next request in the scope.
func (client *MocaChainSignClient) finishPendingEvmTx(ctx context.Context, st *evmState, fence evmFence,
	pending pendingRecord, terminal pendingTerminal,
) error {
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	err := st.applyTerminal(wctx, fence, pending, terminal)
	if err != nil {
		st.stashTerminal(fence, pending.Scope, pending, terminal)
		log.CtxErrorw(ctx, "failed to record the EVM transaction's terminal state; the scope stays blocked until it is recorded",
			"scope", pending.Scope, "tx_hash", pending.TxHash.Hex(), "error", err)
	}
	return err
}
