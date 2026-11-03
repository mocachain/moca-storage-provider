package signer

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"

	"github.com/mocachain/moca-storage-provider/pkg/log"
	evmostypes "github.com/mocachain/moca/v2/types"
)

// attachEvmState wires the EVM state store into the client. Without it no
// EVM transaction is signed.
func (client *MocaChainSignClient) attachEvmState(store evmStateStore, chainID string, leaseTTL time.Duration) error {
	evmChainID, err := evmostypes.ParseChainID(chainID)
	if err != nil {
		return fmt.Errorf("failed to derive the EVM chain id from %q: %w", chainID, err)
	}
	if !evmChainID.IsUint64() {
		return fmt.Errorf("EVM chain id of %q does not fit in uint64", chainID)
	}
	lease := newEvmLease(store, chainID, leaseTTL)
	st := newEvmState(store, chainID, evmChainID.Uint64(), lease)
	lease.onAcquire = func(ctx context.Context, fence evmFence) {
		// Off the lease goroutine: recovery takes scope locks, which an
		// in-flight request may hold while it waits for a receipt.
		st.spawn(func() { client.recoverEvmState(ctx, st, fence) })
	}
	client.evm = st
	return nil
}

// checkEvmStateOnStart refuses to start against state this signer cannot
// trust: rows of another chain, or a pending row whose evidence fails V(e).
// An outcome with invalid evidence does not stop the signer; it stays
// blocked for its operation.
func (client *MocaChainSignClient) checkEvmStateOnStart(ctx context.Context) error {
	st := client.evm
	if st == nil {
		return errEvmStateNotConfigured
	}
	foreign, err := st.store.ForeignChainIDs(ctx, st.chainID)
	if err != nil {
		return fmt.Errorf("failed to read signer EVM state: %w", err)
	}
	if len(foreign) != 0 {
		sort.Strings(foreign)
		return fmt.Errorf("signer EVM state holds rows of chain ids %v, but this signer is configured for %s", foreign, st.chainID)
	}
	pendings, err := st.store.ListPending(ctx, st.chainID)
	if err != nil {
		return fmt.Errorf("failed to read signer EVM pending state: %w", err)
	}
	var invalid []string
	for _, pending := range pendings {
		if _, err := st.loadEvidence(ctx, nil, "pending", pending.ID, pending.ref(), pending.audit("")); err != nil {
			if !stderrors.Is(err, errEvmEvidenceInvalid) {
				return err
			}
			invalid = append(invalid, fmt.Sprintf("pending %d (scope %s, hash %s): %v", pending.ID, pending.Scope, pending.TxHash.Hex(), err))
		}
	}
	outcomes, err := st.store.ListOutcomes(ctx, st.chainID)
	if err != nil {
		return fmt.Errorf("failed to read signer EVM outcome state: %w", err)
	}
	for _, outcome := range outcomes {
		if _, err := st.loadEvidence(ctx, nil, "outcome", outcome.ID, outcome.ref(), outcome.audit("")); err != nil &&
			!stderrors.Is(err, errEvmEvidenceInvalid) {
			return err
		}
	}
	if len(invalid) != 0 {
		return fmt.Errorf("signer EVM pending state has corrupt or foreign rows, inspect them with 'signer-evm list': %v", invalid)
	}
	return nil
}

// recoverEvmState runs after each new lease epoch: audit what was loaded,
// sync each scope to the epoch (syncEvmScopeEpoch), and resolve every
// pending row in the background. Requests do not wait for it: each one
// syncs its scope itself.
func (client *MocaChainSignClient) recoverEvmState(ctx context.Context, st *evmState, fence evmFence) {
	rctx, cancel := evmStateCtx(ctx)
	defer cancel()
	pendings, err := st.store.ListPending(rctx, st.chainID)
	if err != nil {
		log.CtxErrorw(ctx, "failed to load signer EVM pending state after acquiring the lease", "error", err)
		return
	}
	byScope := make(map[SignType]pendingRecord, len(pendings))
	for _, pending := range pendings {
		byScope[pending.Scope] = pending
		if _, err := st.loadEvidence(rctx, &fence, "pending", pending.ID, pending.ref(), pending.audit("")); err != nil {
			continue
		}
		event := pending.audit(evmAuditLoadedOnStart)
		if addr, ok := client.keyAddress(pending.Scope); ok && addr != pending.From {
			event.Detail = fmt.Sprintf("recorded sender %s differs from the current %s key %s; the scope stays blocked",
				pending.From.Hex(), pending.Scope, addr.Hex())
		}
		if err := st.store.AppendAudit(rctx, &fence, st.chainID, event); err != nil {
			log.CtxErrorw(ctx, "failed to audit a loaded EVM pending row", "tx_hash", pending.TxHash.Hex(), "error", err)
		}
	}
	if outcomes, err := st.store.ListOutcomes(rctx, st.chainID); err == nil {
		for _, outcome := range outcomes {
			_, _ = st.loadEvidence(rctx, &fence, "outcome", outcome.ID, outcome.ref(), outcome.audit(""))
		}
	}

	for _, scope := range []SignType{SignOperator, SignSeal, SignGc} {
		lock := client.scopeLock(scope)
		lock.Lock()
		var known *pendingRecord
		if pending, ok := byScope[scope]; ok {
			known = &pending
		}
		// Its own timeout: waiting for the scope lock may have used up rctx.
		// On failure the scope's next request syncs it instead.
		if err := client.syncEvmScopeEpoch(ctx, st, fence, scope, known); err != nil {
			log.CtxErrorw(ctx, "failed to sync the scope after acquiring the lease", "scope", scope, "error", err)
		}
		lock.Unlock()
	}

	for _, scope := range []SignType{SignOperator, SignSeal, SignGc} {
		if _, ok := byScope[scope]; ok {
			st.spawn(func() { client.backgroundResolveEvm(ctx, scope) })
		}
	}
}

// backgroundResolveEvm resolves a scope's pending row once: it re-sends the
// recorded bytes or, on a receipt, ends the row. It passes no operation, so
// it never takes a result. Skipping it does not affect correctness.
func (client *MocaChainSignClient) backgroundResolveEvm(ctx context.Context, scope SignType) {
	lock := client.scopeLock(scope)
	if lock == nil {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	rctx, cancel := context.WithTimeout(ctx, evmStateBackgroundResolveWait)
	defer cancel()
	hash, handled, err := client.resolvePendingEvmTx(rctx, scope, ethcmn.Hash{})
	log.CtxWarnw(ctx, "background resolution of the EVM pending transaction", "scope", scope,
		"tx_hash", hash, "still_blocked", handled, "error", err)
}

// startEvmState checks the state and starts the lease loop.
func (client *MocaChainSignClient) startEvmState(ctx context.Context) error {
	if err := client.checkEvmStateOnStart(ctx); err != nil {
		client.closeEvmState(ctx)
		return err
	}
	client.evm.lease.start(context.Background())
	return nil
}

// stopEvmState stops the lease loop and releases the lease.
func (client *MocaChainSignClient) stopEvmState(ctx context.Context) {
	if client.evm == nil {
		return
	}
	client.evm.lease.stop(ctx)
	wctx, cancel := context.WithTimeout(ctx, evmStateWriteTimeout)
	defer cancel()
	if err := client.evm.waitWorkers(wctx); err != nil {
		log.CtxErrorw(ctx, "signer EVM recovery still running at stop; closing the DB under it", "error", err)
	}
	client.closeEvmState(ctx)
}

func (client *MocaChainSignClient) closeEvmState(ctx context.Context) {
	if client.evmCloser == nil {
		return
	}
	if err := client.evmCloser(); err != nil {
		log.CtxErrorw(ctx, "failed to close the signer EVM state DB", "error", err)
	}
	client.evmCloser = nil
}
