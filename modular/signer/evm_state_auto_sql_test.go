package signer

import (
	"context"
	"errors"
	"testing"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
)

// The automatic transitions on MySQL: fenced by the lease, audited under its
// epoch, and the evidence lookup by nonce stays within its id window.
func TestSignerEvmMySQLAutoTransitions(t *testing.T) {
	ctx := context.Background()
	env := newMySQLEvmEnv(t, true)
	original := signTx(t, 7, plainTransferTo, 1, nil, 1_000_000_000)
	pending := env.recordPending(t, original, ethcmn.HexToHash("0x0d"))
	fence := env.lease(t, env.sql, "signer", time.Hour)

	stale := fence
	stale.Epoch++
	event := pending.audit(evmAuditAutoBlockedDropped)
	if err := env.sql.AutoResolvePending(ctx, stale, pending, autoBlocked(evmReasonDropped), event); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("a stale fence must be refused, got %v", err)
	}
	if env.count(t, "signer_evm_state", "id = ? AND state = 'pending'", pending.ID) != 1 {
		t.Fatal("the refused transition changed the pending row")
	}

	if err := env.sql.AutoResolvePending(ctx, fence, pending, autoBlocked(evmReasonDropped), event); err != nil {
		t.Fatal(err)
	}
	env.verifyOutcomeEvidence(t, pending.ID, evmOutcomeBlockedUnknown, evmReasonDropped)
	if env.count(t, "signer_evm_audit", "event = ? AND lease_epoch = ?", evmAuditAutoBlockedDropped, fence.Epoch) != 1 {
		t.Fatal("the transition must be audited under the lease epoch")
	}

	// The next transaction of the key takes the same nonce.
	next := signTx(t, 7, plainTransferTo, 2, nil, 1_000_000_000)
	successor := env.recordPending(t, next, ethcmn.HexToHash("0x0e"))
	found, err := env.sql.ListSignedTxAtNonce(ctx, fakeEvmChainID, pending.From, 7, pending.SignedTxID, evmSupersedeWindow)
	if err != nil || len(found) != 1 || found[0].TxHash != successor.TxHash {
		t.Fatalf("want only the successor at nonce 7, got %+v err=%v", found, err)
	}
	if found, err = env.sql.ListSignedTxAtNonce(ctx, fakeEvmChainID, pending.From, 7, successor.SignedTxID, evmSupersedeWindow); err != nil || len(found) != 0 {
		t.Fatalf("rows at or below afterID must not be listed, got %+v err=%v", found, err)
	}

	// Recording took the lease again, under a new epoch.
	fence = env.lease(t, env.sql, "signer", time.Hour)
	stale = fence
	stale.Epoch--
	outcome, err := env.sql.GetOutcomeByID(ctx, fakeEvmChainID, pending.ID)
	if err != nil || outcome == nil {
		t.Fatalf("outcome: %+v err=%v", outcome, err)
	}
	settle := outcome.audit(evmAuditAutoOutcomeSuperseded)
	if err := env.sql.AutoSettleOutcome(ctx, stale, *outcome, []evmOutcomeState{evmOutcomeBlockedUnknown}, nil, settle); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("a stale fence must be refused, got %v", err)
	}
	if err := env.sql.AutoSettleOutcome(ctx, fence, *outcome, []evmOutcomeState{evmOutcomeBlockedUnknown}, nil, settle); err != nil {
		t.Fatal(err)
	}
	if env.count(t, "signer_evm_state", "id = ?", outcome.ID) != 0 ||
		env.count(t, "signer_evm_audit", "event = ? AND outcome_id = ? AND lease_epoch = ?", evmAuditAutoOutcomeSuperseded, outcome.ID, fence.Epoch) != 1 {
		t.Fatal("the outcome must be deleted and audited under the lease epoch")
	}
	if err := env.sql.AutoSettleOutcome(ctx, fence, *outcome, []evmOutcomeState{evmOutcomeBlockedUnknown}, nil, settle); !errors.Is(err, errEvmRowGone) {
		t.Fatalf("a second settle must find the row gone, got %v", err)
	}
}
