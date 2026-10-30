package signer

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// Automatic disposition of EVM state the chain decides.
//
// A pending row blocks its scope: nothing else is signed with the key while
// the recorded bytes may still be included. The checks here end a pending
// row that has no receipt, or settle a blocked_unknown outcome, when the
// chain shows how the transaction ended. None of them lets an operation be
// signed again while it may still execute. A row is deleted only when the
// chain proves the transaction did not run (it was included and failed, or
// another transaction of this signer used its nonce). Every other automatic
// step turns the pending row into a blocked_unknown outcome: that blocks the
// one operation, as `signer-evm resolve --accept-unknown` does by hand, and
// frees the scope, whose next transaction takes the chain nonce.
//
// The steps, for a pending transaction at nonce N with no receipt, with the
// sender's nonce C at the latest height and P including the node's mempool:
//
//   - C > N and the transaction is in a block with a non-zero code: it failed
//     and left no state (V1). The row is deleted; the operation may be signed
//     again.
//   - C > N and no Cosmos transaction carries the hash: blocked_unknown
//     (receipt_missing).
//   - C = N, P = C (nothing of this sender in the mempool) and the node
//     refuses the re-sent bytes for a reason other than the nonce:
//     blocked_unknown (dropped). The next transaction takes nonce N, so the
//     recorded bytes can no longer be included once it lands.
//   - C < N, P = C and the node refuses the re-sent bytes as nonce too high:
//     blocked_unknown (nonce_gap), as `--accept-gap` does.
//
// Anything else (our transaction waits in the mempool, the node is
// unreachable, the index is disabled) leaves the row pending and re-sends the
// bytes, as before. Conclusions that rest on an absence (nothing found,
// refused) are acted on only when a check at a higher height repeats them.
//
// A blocked_unknown outcome is settled when its operation is requested again
// and on each new lease: its own receipt (succeeded, or failed and deleted),
// its inclusion as a failed Cosmos transaction (deleted), or a receipt of
// another transaction this signer signed at the same nonce (deleted: that one
// used the nonce, so the original never ran).

const (
	evmReasonDropped = "dropped"

	// evmChainQueryTimeout bounds the chain reads of one check.
	evmChainQueryTimeout = 10 * time.Second
	// evmSupersedeWindow bounds how many evidence ids after an outcome's own
	// row are searched for the transaction that took its nonce. That
	// transaction is signed right after the outcome is kept.
	evmSupersedeWindow = 1000
	// evmOutcomeCheckInterval spaces the chain checks of one outcome, so a
	// request retried in a loop does not query the chain each time.
	evmOutcomeCheckInterval = 10 * time.Second
)

// evmAutoChain is the chain the automatic checks read. cosmos may be nil:
// then failed transactions without a receipt are not recognised and absent
// ones stay pending.
type evmAutoChain struct {
	evm    EvmDisposeChain
	cosmos CosmosDisposeChain
}

// evmAutoObservation is the first sighting of a conclusion that waits for a
// second one at a higher height.
type evmAutoObservation struct {
	txHash ethcmn.Hash
	reason string
	height uint64
}

// observe records reason for txHash at height. It reports true when the same
// conclusion was already seen for the same transaction at a lower height.
func (st *evmState) observe(scope SignType, txHash ethcmn.Hash, reason string, height uint64) bool {
	mem := st.memory(scope)
	st.mu.Lock()
	defer st.mu.Unlock()
	seen := mem.autoSeen
	if seen != nil && seen.txHash == txHash && seen.reason == reason {
		return height > seen.height
	}
	mem.autoSeen = &evmAutoObservation{txHash: txHash, reason: reason, height: height}
	return false
}

func (st *evmState) forgetObservation(scope SignType) {
	mem := st.memory(scope)
	st.mu.Lock()
	mem.autoSeen = nil
	st.mu.Unlock()
}

// outcomeCheckDue reports whether outcome id may be checked on chain now, and
// if so marks it checked.
func (st *evmState) outcomeCheckDue(id uint64, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if last, ok := st.outcomeChecked[id]; ok && now.Sub(last) < evmOutcomeCheckInterval {
		return false
	}
	st.outcomeChecked[id] = now
	return true
}

// autoDisposePending runs the checks above for a pending row without a
// receipt. moved means the row is gone (ended here, or by someone else) and
// the caller reads the state again; sent means the recorded bytes were
// re-sent here. Without an automatic chain it does nothing.
func (client *MocaChainSignClient) autoDisposePending(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	pending pendingRecord, signedTx *types.Transaction,
) (moved, sent bool, err error) {
	chain := client.autoChain
	if chain == nil || chain.evm == nil {
		return false, false, nil
	}
	qctx, cancel := context.WithTimeout(ctx, evmChainQueryTimeout)
	defer cancel()
	height, err := chain.evm.BlockNumber(qctx)
	if err != nil {
		return false, false, fmt.Errorf("eth_blockNumber: %w", err)
	}
	chainNonce, err := chain.evm.NonceAt(qctx, pending.From, heightArg(height))
	if err != nil {
		return false, false, fmt.Errorf("nonce of %s at %d: %w", pending.From.Hex(), height, err)
	}
	base := pending.audit("")
	base.ChainNonce, base.ChainHeight = uint64Ptr(chainNonce), uint64Ptr(height)

	if chainNonce > pending.Nonce {
		// The nonce is used: the recorded bytes can never be included again.
		if chain.cosmos == nil {
			return false, false, nil
		}
		// A transaction found by its ethereum_tx event got through the ante
		// handler, which emits that event: its nonce increment persists even
		// when its execution fails, so this transaction is the one that used N.
		view, err := chain.cosmos.SearchEthereumTx(qctx, pending.TxHash, height)
		switch {
		case stderrors.Is(err, errCosmosIndexDisabled):
			return false, false, nil
		case err != nil:
			return false, false, fmt.Errorf("search for the cosmos transaction of %s: %w", pending.TxHash.Hex(), err)
		case view != nil && view.Code != 0:
			if !evmFailedTxRollsBack {
				return false, false, nil
			}
			event := withCosmosEvidence(base, view)
			event.Event = evmAuditAutoResolvedFailed
			event.Detail = fmt.Sprintf("included in cosmos tx %s at height %d with code %d and no EVM receipt; it left no state",
				view.Hash, view.Height, view.Code)
			moved, err := client.applyAutoPending(ctx, st, fence, scope, pending, nil, event, chainNonce)
			return moved, false, err
		case view != nil:
			// Included without a failure code: its receipt decides, wait for it.
			st.forgetObservation(scope)
			return false, false, nil
		}
		if !st.observe(scope, pending.TxHash, evmReasonReceiptMissing, height) {
			return false, false, nil
		}
		event := base
		event.Event = evmAuditAutoBlockedMissing
		event.Detail = fmt.Sprintf("nonce %d is used (account nonce %d at height %d) and no transaction carries %s",
			pending.Nonce, chainNonce, height, pending.TxHash.Hex())
		moved, err := client.applyAutoPending(ctx, st, fence, scope, pending, autoBlocked(evmReasonReceiptMissing), event, chainNonce)
		return moved, false, err
	}

	pendingNonce, err := chain.evm.PendingNonceAt(qctx, pending.From)
	if err != nil {
		return false, false, fmt.Errorf("pending nonce of %s: %w", pending.From.Hex(), err)
	}
	if pendingNonce != chainNonce {
		// A transaction of this sender waits in the node's mempool.
		st.forgetObservation(scope)
		return false, false, nil
	}
	resendCtx, cancelResend := context.WithTimeout(ctx, evmResendTimeout)
	sendErr := sendEvmTxFn(resendCtx, client.evmClient, signedTx)
	cancelResend()
	log.CtxWarnw(ctx, "re-broadcast unconfirmed EVM tx; check the hash on chain before any manual action",
		"scope", scope, "nonce", pending.Nonce, "tx_hash", pending.TxHash.Hex(), "operation", pending.Operation.Hex(), "error", sendErr)
	classified := classifyEvmSendError(ctx, sendErr)
	reason, event := "", base
	switch {
	case chainNonce == pending.Nonce && classified == evmSendRejected:
		reason, event.Event = evmReasonDropped, evmAuditAutoBlockedDropped
	case chainNonce < pending.Nonce && classified == evmSendNonceRejected && isEvmNonceHighError(sendErr.Error()):
		reason, event.Event = evmReasonNonceGap, evmAuditAutoBlockedNonceGap
	default:
		st.forgetObservation(scope)
		return false, true, nil
	}
	if !st.observe(scope, pending.TxHash, reason, height) {
		return false, true, nil
	}
	event.Detail = fmt.Sprintf("not in the mempool (account nonce %d at height %d, pending nonce %d) and the node refuses the bytes: %v",
		chainNonce, height, pendingNonce, sendErr)
	moved, err = client.applyAutoPending(ctx, st, fence, scope, pending, autoBlocked(reason), event, chainNonce)
	return moved, true, err
}

func autoBlocked(reason string) *outcomeRecord {
	return &outcomeRecord{State: evmOutcomeBlockedUnknown, Reason: reason, Source: evmOutcomeSourceAuto}
}

func withCosmosEvidence(event evmAuditEvent, view *CosmosTxView) evmAuditEvent {
	event.EvidenceTxHash, event.EvidenceKind = view.Hash, evmEvidenceKindCosmos
	if view.Height != 0 {
		event.ReceiptBlock = uint64Ptr(view.Height)
	}
	return event
}

// applyAutoPending deletes the pending row (result nil) or turns it into the
// outcome result, then lets the scope sign from nextNonce. moved is true when
// the row is gone, including when someone else removed it first.
func (client *MocaChainSignClient) applyAutoPending(ctx context.Context, st *evmState, fence evmFence, scope SignType,
	pending pendingRecord, result *outcomeRecord, event evmAuditEvent, nextNonce uint64,
) (bool, error) {
	if result != nil {
		event.OutcomeID, event.OutcomeState, event.OutcomeReason = uint64Ptr(pending.ID), result.State, result.Reason
	}
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	err := st.store.AutoResolvePending(wctx, fence, pending, result, event)
	if isEvmCommitUnknown(err) {
		current, readErr := st.store.GetPending(wctx, st.chainID, scope)
		if readErr == nil && (current == nil || current.ID != pending.ID) {
			if done, readErr := st.store.HasAudit(wctx, st.chainID, event.Event, pending.TxHash, 0); readErr == nil && done {
				err = nil
			}
		}
	}
	switch {
	case stderrors.Is(err, errEvmRowGone):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("failed to record %s for %s: %w", event.Event, pending.TxHash.Hex(), err)
	}
	st.clearLastSeen(scope, pending.ID)
	st.forgetObservation(scope)
	// No transaction of the key is in flight any more: the next one takes the
	// chain nonce. A row of an earlier key does not touch the current key's.
	if addr, ok := client.keyAddress(scope); ok && addr == pending.From {
		if cache := client.nonceCache(scope); cache != nil {
			*cache = nextNonce
		}
	}
	log.CtxWarnw(ctx, "EVM pending transaction ended from chain evidence", "scope", scope, "event", event.Event,
		"tx_hash", pending.TxHash.Hex(), "operation", pending.Operation.Hex(), "nonce", pending.Nonce, "next_nonce", nextNonce,
		"detail", event.Detail)
	return true, nil
}

// autoSettleOutcome settles a blocked_unknown outcome the chain decides.
// settled means the outcome was deleted or became succeeded; the caller reads
// the state again.
func (client *MocaChainSignClient) autoSettleOutcome(ctx context.Context, st *evmState, fence evmFence,
	outcome outcomeRecord,
) (bool, error) {
	chain := client.autoChain
	if chain == nil || chain.evm == nil || outcome.State != evmOutcomeBlockedUnknown {
		return false, nil
	}
	if !st.outcomeCheckDue(outcome.ID, time.Now()) {
		return false, nil
	}
	qctx, cancel := context.WithTimeout(ctx, evmChainQueryTimeout)
	defer cancel()
	height, err := chain.evm.BlockNumber(qctx)
	if err != nil {
		return false, fmt.Errorf("eth_blockNumber: %w", err)
	}
	base := outcome.audit("")
	base.ChainHeight = uint64Ptr(height)

	// The original itself.
	var view *CosmosTxView
	if chain.cosmos != nil {
		view, err = chain.cosmos.SearchEthereumTx(qctx, outcome.TxHash, height)
		switch {
		case stderrors.Is(err, errCosmosIndexDisabled):
			view = nil
		case err != nil:
			return false, fmt.Errorf("search for the cosmos transaction of %s: %w", outcome.TxHash.Hex(), err)
		}
		if view != nil && view.Code != 0 && evmFailedTxRollsBack {
			event := withCosmosEvidence(base, view)
			event.Event = evmAuditAutoOutcomeFailed
			event.Detail = fmt.Sprintf("included in cosmos tx %s at height %d with code %d; it left no state", view.Hash, view.Height, view.Code)
			return client.applyAutoOutcome(ctx, st, fence, outcome, nil, event)
		}
	}
	if chain.cosmos == nil || view != nil {
		receipt, err := chain.evm.TransactionReceipt(qctx, outcome.TxHash)
		switch {
		case stderrors.Is(err, ethereum.NotFound) || (err == nil && receipt == nil):
		case err != nil:
			return false, fmt.Errorf("receipt of %s: %w", outcome.TxHash.Hex(), err)
		case receipt.BlockNumber == nil || receipt.BlockNumber.Uint64() > height:
		default:
			event := base
			event.ReceiptStatus, event.ReceiptBlock = uint64Ptr(receipt.Status), uint64Ptr(receipt.BlockNumber.Uint64())
			if receipt.Status != types.ReceiptStatusSuccessful {
				event.Event = evmAuditAutoOutcomeFailed
				return client.applyAutoOutcome(ctx, st, fence, outcome, nil, event)
			}
			event.Event = evmAuditAutoOutcomeSucceeded
			to := &outcomeRecord{State: evmOutcomeSucceeded, ResultTxHash: outcome.TxHash, ReceiptBlock: receipt.BlockNumber.Uint64(),
				Source: evmOutcomeSourceAuto}
			return client.applyAutoOutcome(ctx, st, fence, outcome, to, event)
		}
	}

	// Another transaction of this signer at the same nonce.
	rows, err := st.store.ListSignedTxAtNonce(qctx, st.chainID, outcome.From, outcome.Nonce, outcome.SignedTxID, evmSupersedeWindow)
	if err != nil {
		return false, fmt.Errorf("failed to read the evidence at nonce %d: %w", outcome.Nonce, err)
	}
	for _, row := range rows {
		if row.TxHash == outcome.TxHash {
			continue
		}
		ref := evidenceRef{SignedTxID: row.ID, ChainID: row.ChainID, Scope: row.Scope, Operation: row.Operation,
			From: row.From, Nonce: row.Nonce, TxHash: row.TxHash}
		if _, err := st.verify(&row, ref); err != nil {
			continue
		}
		used, evidence, err := client.usedNonce(qctx, chain, row.TxHash, height)
		if err != nil {
			return false, err
		}
		if !used {
			continue
		}
		event := base
		event.Event = evmAuditAutoOutcomeSuperseded
		event.EvidenceTxHash, event.EvidenceKind = evidence, evmEvidenceKindEVM
		event.Detail = fmt.Sprintf("nonce %d was used by %s, signed by this signer for operation %s", outcome.Nonce,
			row.TxHash.Hex(), row.Operation.Hex())
		return client.applyAutoOutcome(ctx, st, fence, outcome, nil, event)
	}
	return false, nil
}

// usedNonce reports whether the transaction hash was included at or below
// height: with a receipt, or as a Cosmos transaction.
func (client *MocaChainSignClient) usedNonce(ctx context.Context, chain *evmAutoChain, hash ethcmn.Hash, height uint64) (bool, string, error) {
	receipt, err := chain.evm.TransactionReceipt(ctx, hash)
	switch {
	case stderrors.Is(err, ethereum.NotFound) || (err == nil && receipt == nil):
	case err != nil:
		return false, "", fmt.Errorf("receipt of %s: %w", hash.Hex(), err)
	case receipt.BlockNumber != nil && receipt.BlockNumber.Uint64() <= height:
		return true, hash.Hex(), nil
	}
	if chain.cosmos == nil {
		return false, "", nil
	}
	view, err := chain.cosmos.SearchEthereumTx(ctx, hash, height)
	switch {
	case stderrors.Is(err, errCosmosIndexDisabled):
		return false, "", nil
	case err != nil:
		return false, "", fmt.Errorf("search for the cosmos transaction of %s: %w", hash.Hex(), err)
	}
	return view != nil, hash.Hex(), nil
}

// applyAutoOutcome deletes the outcome (to nil) or updates it to to.
func (client *MocaChainSignClient) applyAutoOutcome(ctx context.Context, st *evmState, fence evmFence, outcome outcomeRecord,
	to *outcomeRecord, event evmAuditEvent,
) (bool, error) {
	if to != nil {
		event.OutcomeState = to.State
	}
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	err := st.store.AutoSettleOutcome(wctx, fence, outcome, []evmOutcomeState{evmOutcomeBlockedUnknown}, to, event)
	if isEvmCommitUnknown(err) {
		if done, readErr := st.store.HasAudit(wctx, st.chainID, event.Event, ethcmn.Hash{}, outcome.ID); readErr == nil && done {
			err = nil
		}
	}
	switch {
	case stderrors.Is(err, errEvmRowGone), stderrors.Is(err, errEvmStateConflict):
		// Settled or changed by someone else; the caller reads it again.
		return true, nil
	case err != nil:
		return false, fmt.Errorf("failed to record %s for outcome %d: %w", event.Event, outcome.ID, err)
	}
	st.mu.Lock()
	delete(st.outcomeChecked, outcome.ID)
	st.mu.Unlock()
	log.CtxWarnw(ctx, "EVM outcome settled from chain evidence", "scope", outcome.Scope, "event", event.Event,
		"outcome_id", outcome.ID, "tx_hash", outcome.TxHash.Hex(), "operation", outcome.Operation.Hex(), "detail", event.Detail)
	return true, nil
}

// backgroundSettleOutcomes tries to settle every blocked_unknown outcome once,
// under each scope's lock. It never takes a result.
func (client *MocaChainSignClient) backgroundSettleOutcomes(ctx context.Context, st *evmState, fence evmFence) {
	rctx, cancel := evmStateCtx(ctx)
	outcomes, err := st.store.ListOutcomes(rctx, st.chainID)
	cancel()
	if err != nil {
		log.CtxErrorw(ctx, "failed to list EVM outcomes for settlement", "error", err)
		return
	}
	for _, outcome := range outcomes {
		if outcome.State != evmOutcomeBlockedUnknown {
			continue
		}
		lock := client.scopeLock(outcome.Scope)
		if lock == nil {
			continue
		}
		lock.Lock()
		if _, err := st.loadEvidence(ctx, &fence, "outcome", outcome.ID, outcome.ref(), outcome.audit("")); err == nil {
			if _, err := client.autoSettleOutcome(ctx, st, fence, outcome); err != nil {
				log.CtxWarnw(ctx, "failed to settle a blocked EVM outcome", "outcome_id", outcome.ID, "error", err)
			}
		}
		lock.Unlock()
	}
}
