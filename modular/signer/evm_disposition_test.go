package signer

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// Scenarios T4–T9, the CLI part of T11c, T18–T19c of the SOL-486 plan
// (revision 4): 'signer-evm resolve', 'resolve-outcome', 'release' and
// 'dispose-outcome' decide from the signed transaction evidence and the
// chain, and the signer then follows the DB.

var plainTransferTo = ethcmn.HexToAddress("0x1111111111111111111111111111111111111111")

// expectResigned checks the operation is signed again at a new nonce and
// executes once in total.
func (env *disposeEnv) expectResigned(amount int64, original *ethtypes.Transaction) {
	env.t.Helper()
	hash, err := deposit(scenarioCtx(env.t), env.client, amount)
	if err != nil || hash == original.Hash().String() {
		env.t.Fatalf("expected a new signature, got hash=%q err=%v; %s", hash, err, env.node.describe())
	}
	assertExecutedOnce(env.t, env.node, original)
}

// expectBlocked checks a request for the operation is refused without signing.
func (env *disposeEnv) expectBlocked(amount int64, want string) {
	env.t.Helper()
	_, sends, _ := env.node.snapshot()
	hash, err := deposit(scenarioCtx(env.t), env.client, amount)
	_, after, _ := env.node.snapshot()
	if err == nil || hash != "" || len(after) != len(sends) || !strings.Contains(err.Error(), want) {
		env.t.Fatalf("expected a refusal containing %q without sends, got hash=%q err=%v sends %d->%d", want, hash, err, len(sends), len(after))
	}
}

// T4: nonce N used by an unrelated transaction: (i) one this signer signed
// for another operation Y (branch b), (ii) a plain transfer (branch c).
func TestDisposeUnrelatedOccupier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch string
		setup  func(env *disposeEnv, n uint64) *ethtypes.Transaction
	}{
		{"own other operation", EvmBranchOwnOtherOp, func(env *disposeEnv, n uint64) *ethtypes.Transaction {
			other := signTx(env.t, n, ethcmn.HexToAddress("0x2000"), 0, []byte{0x01, 0x02}, 1_000_000_000)
			env.putOwnEvidence(other, depositOperation(env.t, 555))
			return other
		}},
		{"plain transfer", EvmBranchPlainTransfer, func(env *disposeEnv, n uint64) *ethtypes.Transaction {
			return signTx(env.t, n, plainTransferTo, 5, nil, 1_000_000_000)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newDisposeEnv(t)
			pending, original := env.stuckDeposit(1000)
			occupier := tc.setup(env, pending.Nonce)
			env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)

			// the signer keeps the scope blocked and never clears it itself
			hash, err := deposit(scenarioCtx(t), env.client, 1000)
			if err == nil || hash != pending.TxHash.String() {
				t.Fatalf("scope must stay blocked: hash=%q err=%v", hash, err)
			}
			if _, ok := env.store.pendingOf(SignOperator); !ok {
				t.Fatal("the signer must not clear the pending row")
			}

			report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
			expectExit(t, report, EvmDisposeExitOK, tc.branch)
			if _, ok := env.store.pendingOf(SignOperator); ok || len(env.store.outcomeRows()) != 0 {
				t.Fatalf("pending must be deleted without an outcome")
			}
			audits := env.store.auditEvents(evmAuditManualResolveOtherUnrelated)
			if len(audits) != 1 || audits[0].EvidenceTxHash != occupier.Hash().Hex() || audits[0].Actor == "" ||
				audits[0].ChainHeight == nil || audits[0].ChainNonce == nil {
				t.Fatalf("unexpected audit: %+v", audits)
			}
			env.expectResigned(1000, original)
		})
	}
}

// precompileCall is a call to the virtual group precompile with other calldata.
func precompileCall(t *testing.T, n uint64) *ethtypes.Transaction {
	return signTx(t, n, ethcmn.HexToAddress("0x0000000000000000000000000000000000002000"), 0, []byte{0xde, 0xad}, 1_000_000_000)
}

// T4c: the occupier has the original call with another gas price (branch d).
// The result is kept: the operation gets the occupier's hash and is not
// signed again. resolve-outcome reaches the same result.
func TestDisposeSameCallOccupier(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, original := env.stuckDeposit(1000)
		occupier := sameCall(t, original)
		env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)

		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{EvidenceTx: occupier.Hash()})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchSameCall)
		outcomes := env.store.outcomeRows()
		if len(outcomes) != 1 || outcomes[0].State != evmOutcomeSucceeded || outcomes[0].ResultTxHash != occupier.Hash() ||
			outcomes[0].EvidenceTxHash != occupier.Hash().Hex() || env.auditCount(evmAuditManualResolveSameCall) != 1 {
			t.Fatalf("unexpected outcome: %+v", outcomes)
		}
		hash, err := deposit(scenarioCtx(t), env.client, 1000)
		if err != nil || hash != occupier.Hash().String() {
			t.Fatalf("the operation must get the occupier's hash, got %q %v", hash, err)
		}
		assertExecutedOnce(t, env.node, original)
	})
	t.Run("resolve-outcome", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, original := env.stuckDeposit(1000)
		occupier := sameCall(t, original)
		env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)
		env.chain.hide(occupier.Hash().Hex(), true)
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchOccupierMissing)
		env.chain.hide(occupier.Hash().Hex(), false)

		report = env.outcomeCmd(EvmDisposeResolveOutcome, pending.ID, EvmDisposeRequest{EvidenceTx: occupier.Hash()})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchSameCall)
		hash, err := deposit(scenarioCtx(t), env.client, 1000)
		if err != nil || hash != occupier.Hash().String() {
			t.Fatalf("the operation must get the occupier's hash, got %q %v", hash, err)
		}
		assertExecutedOnce(t, env.node, original)
	})
}

// T4d: the occupier failed. Before V1 is verified it is undetermined
// (branch e); with V1 it proves the operation did not run (branch a).
func TestDisposeFailedOccupier(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "V1 unverified", true: "V1 verified"}[verified], func(t *testing.T) {
			original := evmFailedTxRollsBack
			evmFailedTxRollsBack = verified
			t.Cleanup(func() { evmFailedTxRollsBack = original })
			env := newDisposeEnv(t)
			pending, originalTx := env.stuckDeposit(1000)
			env.chain.addEvmTx(sameCall(t, originalTx), ethtypes.ReceiptStatusFailed)

			report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
			if !verified {
				expectExit(t, report, EvmDisposeExitNotProven, EvmBranchUndetermined)
				if _, ok := env.store.pendingOf(SignOperator); !ok || env.auditCount(evmAuditManualRejected) != 1 {
					t.Fatal("an undetermined occupier must leave the pending row and audit the rejection")
				}
				return
			}
			expectExit(t, report, EvmDisposeExitOK, EvmBranchOtherTxFailed)
			if env.auditCount(evmAuditManualResolveOtherTxFailed) != 1 {
				t.Fatal("missing manual_resolve_other_tx_failed")
			}
			env.expectResigned(1000, originalTx)
		})
	}
}

// T4e: the occupier is a different precompile call that, unknown to the
// CLI, executed the same business. It is undetermined: refused by default,
// blocked_unknown with --accept-unknown, and the operation stays blocked.
func TestDisposeUndeterminedOccupier(t *testing.T) {
	env := newDisposeEnv(t)
	pending, original := env.stuckDeposit(1000)
	env.chain.addEvmTx(precompileCall(t, pending.Nonce), ethtypes.ReceiptStatusSuccessful)
	env.node.mu.Lock()
	env.node.executions[executionKey(original)]++
	env.node.mu.Unlock()

	report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
	expectExit(t, report, EvmDisposeExitNotProven, EvmBranchUndetermined)
	report = env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true})
	expectExit(t, report, EvmDisposeExitOK, EvmBranchUndetermined)
	outcomes := env.store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].State != evmOutcomeBlockedUnknown || outcomes[0].Reason != evmReasonOtherTxUnverified ||
		env.auditCount(evmAuditManualResolveUnknown) != 1 {
		t.Fatalf("unexpected outcome: %+v", outcomes)
	}
	env.expectBlocked(1000, "blocked")
	assertExecutedOnce(t, env.node, original)

	report = env.outcomeCmd(EvmDisposeResolveOutcome, outcomes[0].ID, EvmDisposeRequest{ScanBlocks: 100})
	expectExit(t, report, EvmDisposeExitNotProven, EvmBranchUndetermined)
	for _, req := range []EvmDisposeRequest{{BusinessEvidence: "deposit balance unchanged"}, {DecisionRef: "https://board/decision/1"}} {
		report = env.outcomeCmd(EvmDisposeRelease, outcomes[0].ID, req)
		expectExit(t, report, EvmDisposeExitError, "")
	}
	if rows := env.store.outcomeRows(); len(rows) != 1 || rows[0].State != evmOutcomeBlockedUnknown {
		t.Fatalf("the outcome must stay blocked: %+v", rows)
	}
}

// T4f: nonce N used by a Cosmos transaction: (i) a message with the
// operation's fingerprint (branch d'), (ii) none (branch e), (iii) the node
// does not index transactions and no hash is given (branch f).
func TestDisposeCosmosOccupier(t *testing.T) {
	t.Run("same operation", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, original := env.stuckDeposit(1000)
		view := env.chain.addCosmosTx("C05A", 0, []sdk.Msg{depositMsgFor(t, 1000)}, original)

		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchCosmosSameOp)
		outcomes := env.store.outcomeRows()
		if len(outcomes) != 1 || outcomes[0].State != evmOutcomeExecutedElsewhere || outcomes[0].EvidenceKind != evmEvidenceKindCosmos ||
			outcomes[0].EvidenceTxHash != view.Hash || outcomes[0].ResultTxHash != (ethcmn.Hash{}) {
			t.Fatalf("unexpected outcome: %+v", outcomes)
		}
		env.expectBlocked(1000, view.Hash)
		assertExecutedOnce(t, env.node, original)
	})
	t.Run("other messages", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		env.chain.addCosmosTx("C05B", 0, []sdk.Msg{depositMsgFor(t, 999)}, nil)
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{EvidenceCosmosTx: "c05b"})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchUndetermined)
	})
	t.Run("no CometBFT RPC", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		env.chain.addCosmosTx("C05B", 0, []sdk.Msg{depositMsgFor(t, 1000)}, nil)
		req := EvmDisposeRequest{Command: EvmDisposeResolve, Scope: string(SignOperator), TxHash: pending.TxHash,
			EvidenceCosmosTx: "c05b", ChainID: fakeEvmChainID, Operator: "ops-test", Reason: "test",
			BlockWait: time.Second, Poll: time.Millisecond}
		report := disposeEvmState(context.Background(), env.store, req, env.chain, nil)
		expectExit(t, report, EvmDisposeExitError, "")
		if !strings.Contains(report.Error, "--evidence-cosmos-tx needs a CometBFT RPC") {
			t.Fatalf("the given Cosmos hash must not be skipped: %+v", report)
		}
		if current, ok := env.store.pendingOf(SignOperator); !ok || current.TxHash != pending.TxHash || len(env.store.outcomeRows()) != 0 {
			t.Fatalf("the pending row must stay: %+v %+v", current, env.store.outcomeRows())
		}
	})
	t.Run("no index", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		env.chain.addCosmosTx("C05C", 0, []sdk.Msg{depositMsgFor(t, 1000)}, nil)
		env.chain.index = false
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchOccupierMissing)
		if !strings.Contains(strings.Join(report.Samples[1].Notes, ";"), "acc_seq search unavailable") {
			t.Fatalf("the report must say the index is unavailable: %+v", report.Samples)
		}
	})
}

// T4b: the original executed, the nonce moved to N+1, but no receipt and no
// transaction is visible. It can only become blocked_unknown(receipt_missing);
// once the receipt appears, resolve-outcome keeps the original hash.
func TestDisposeOriginalExecutedButInvisible(t *testing.T) {
	env := newDisposeEnv(t)
	original := ambiguousDeposit(t, env.node, env.client)
	env.chain.hide(original.Hash().Hex(), true)
	pending, _ := env.store.pendingOf(SignOperator)

	report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
	expectExit(t, report, EvmDisposeExitNotProven, EvmBranchOccupierMissing)
	report = env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true})
	expectExit(t, report, EvmDisposeExitOK, EvmBranchOccupierMissing)
	outcomes := env.store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].Reason != evmReasonReceiptMissing {
		t.Fatalf("unexpected outcome: %+v", outcomes)
	}
	env.expectBlocked(1000, "blocked")
	assertExecutedOnce(t, env.node, original)
	env.node.setHideReceipts(false)
	if hash, err := deposit(scenarioCtx(t), env.client, 2000); err != nil || hash == "" {
		t.Fatalf("another operation must be signed: %q %v", hash, err)
	}
	report = env.outcomeCmd(EvmDisposeRelease, outcomes[0].ID, EvmDisposeRequest{BusinessEvidence: "none"})
	expectExit(t, report, EvmDisposeExitError, "")

	env.chain.hide(original.Hash().Hex(), false)
	report = env.outcomeCmd(EvmDisposeResolveOutcome, outcomes[0].ID, EvmDisposeRequest{})
	expectExit(t, report, EvmDisposeExitOK, EvmBranchOriginalSucceeded)
	hash, err := deposit(scenarioCtx(t), env.client, 1000)
	if err != nil || hash != original.Hash().String() {
		t.Fatalf("the operation must get the original hash, got %q %v", hash, err)
	}
	assertExecutedOnce(t, env.node, original)
}

// T5: no receipt and the nonce did not move: resolve is refused and the
// signer keeps re-sending the same bytes.
func TestDisposeNonceUnusedIsRefused(t *testing.T) {
	env := newDisposeEnv(t)
	pending, _ := env.stuckDeposit(1000)
	report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true})
	expectExit(t, report, EvmDisposeExitNotProven, EvmBranchNonceUnused)
	if _, ok := env.store.pendingOf(SignOperator); !ok {
		t.Fatal("the pending row must stay")
	}
	env.node.script = scriptAll(fakeEvmAction{kind: fakeEvmDropOnly})
	_, _ = deposit(scenarioCtx(t), env.client, 1000)
	if n := env.node.distinctSentHashes(); n != 1 {
		t.Fatalf("only the original bytes may be sent, saw %d hashes", n)
	}
}

// T6: the bytes cannot be included for lack of funds: resolve is refused;
// once funded, the same bytes go through and the operation runs once.
func TestDisposeInsufficientFunds(t *testing.T) {
	env := newDisposeEnv(t)
	pending, original := env.stuckDeposit(1000)
	env.node.script = scriptAll(fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: "insufficient funds for gas * price + value"})
	if _, err := deposit(scenarioCtx(t), env.client, 1000); err == nil {
		t.Fatal("an unfunded re-send must not succeed")
	}
	report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
	expectExit(t, report, EvmDisposeExitNotProven, EvmBranchNonceUnused)

	env.node.script = nil
	_, _ = deposit(scenarioCtx(t), env.client, 1000) // re-sent and included
	hash, err := deposit(scenarioCtx(t), env.client, 1000)
	if err != nil || hash != original.Hash().String() {
		t.Fatalf("the funded original must be the result, got %q %v", hash, err)
	}
	assertExecutedOnce(t, env.node, original)
}

// T7: a success receipt resolved by the CLI while the signer runs, and while
// it is stopped; the operation gets the original hash either way.
func TestDisposeOriginalSucceeded(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "signer running", true: "signer stopped"}[stopped], func(t *testing.T) {
			env := newDisposeEnv(t)
			original := ambiguousDeposit(t, env.node, env.client)
			report := env.resolve(SignOperator, original.Hash(), EvmDisposeRequest{})
			expectExit(t, report, EvmDisposeExitOK, EvmBranchOriginalSucceeded)
			audits := env.store.auditEvents(evmAuditManualResolveSuccess)
			if len(audits) != 1 || audits[0].OutcomeID == nil || audits[0].ReceiptBlock == nil || audits[0].Reason != "test" ||
				!strings.HasPrefix(audits[0].Actor, "ops-test (os user ") {
				t.Fatalf("unexpected audit: %+v", audits)
			}
			if stopped {
				env.restart()
			}
			hash, err := deposit(scenarioCtx(t), env.client, 1000)
			if err != nil || hash != original.Hash().String() {
				t.Fatalf("the operation must get the original hash, got %q %v", hash, err)
			}
			assertExecutedOnce(t, env.node, original)
		})
	}
}

// T8: refusals leave the DB as it was; only hash or scope mismatches go
// unaudited.
func TestDisposeRefusals(t *testing.T) {
	setup := func(t *testing.T) (*disposeEnv, pendingRecord) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		return env, pending
	}
	t.Run("hash mismatch", func(t *testing.T) {
		env, _ := setup(t)
		before := env.snapshotState()
		expectExit(t, env.resolve(SignOperator, ethcmn.HexToHash("0x99"), EvmDisposeRequest{}), EvmDisposeExitPrecondition, "")
		if env.snapshotState() != before {
			t.Fatal("a hash mismatch must change nothing, not even the audit")
		}
	})
	t.Run("scope mismatch", func(t *testing.T) {
		env, pending := setup(t)
		before := env.snapshotState()
		expectExit(t, env.resolve(SignSeal, pending.TxHash, EvmDisposeRequest{}), EvmDisposeExitPrecondition, "")
		if env.snapshotState() != before {
			t.Fatal("a scope mismatch must change nothing, not even the audit")
		}
	})
	for _, tc := range []struct {
		name  string
		code  int
		apply func(env *disposeEnv, pending pendingRecord)
	}{
		{"chain id", EvmDisposeExitError, func(env *disposeEnv, _ pendingRecord) { env.chain.chainID = 1 }},
		{"rpc error", EvmDisposeExitError, func(env *disposeEnv, _ pendingRecord) { env.chain.failNonceAt = errMemInjected }},
		{"heights disagree", EvmDisposeExitNotProven, func(env *disposeEnv, pending pendingRecord) {
			env.chain.onBlockNumber[2] = func() {
				env.chain.addEvmTx(signTx(env.t, pending.Nonce, plainTransferTo, 1, nil, 1_000_000_000), ethtypes.ReceiptStatusSuccessful)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, pending := setup(t)
			tc.apply(env, pending)
			pendingBefore, outcomesBefore := env.store.pendingRows(), env.store.outcomeRows()
			expectExit(t, env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true}), tc.code, "")
			if env.auditCount(evmAuditManualRejected) != 1 || len(env.store.pendingRows()) != len(pendingBefore) ||
				len(env.store.outcomeRows()) != len(outcomesBefore) {
				t.Fatal("a refusal must audit manual_rejected and change nothing else")
			}
		})
	}
}

// blockedOutcome leaves the deposit as a blocked_unknown(receipt_missing)
// outcome whose nonce is used by a hidden plain transfer.
func (env *disposeEnv) blockedOutcome() (outcomeRecord, *ethtypes.Transaction, *ethtypes.Transaction) {
	env.t.Helper()
	pending, original := env.stuckDeposit(1000)
	occupier := signTx(env.t, pending.Nonce, plainTransferTo, 1, nil, 1_000_000_000)
	env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)
	env.chain.hide(occupier.Hash().Hex(), true)
	expectExit(env.t, env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true}),
		EvmDisposeExitOK, EvmBranchOccupierMissing)
	outcomes := env.store.outcomeRows()
	if len(outcomes) != 1 {
		env.t.Fatalf("expected one outcome, got %+v", outcomes)
	}
	return outcomes[0], original, occupier
}

// T9: --dry-run of every command changes nothing, audits nothing and sends
// nothing; a nonce gap is reported as not probed with exit code 2.
func TestDisposeDryRun(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		env.chain.addEvmTx(signTx(t, pending.Nonce, plainTransferTo, 1, nil, 1_000_000_000), ethtypes.ReceiptStatusSuccessful)
		before := env.snapshotState()
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, DryRun: true})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchPlainTransfer)
		if report.Applied || report.Action == "" || env.snapshotState() != before || len(env.chain.sends) != 0 {
			t.Fatalf("a dry run must change nothing: %+v", report)
		}
	})
	t.Run("nonce gap", func(t *testing.T) {
		env := newGapEnv(t)
		pending := env.refusedTooHigh()
		before := env.snapshotState()
		_, sendsBefore, _ := env.node.snapshot()
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{AcceptGap: true, DryRun: true})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchGapNotProbed)
		_, sendsAfter, _ := env.node.snapshot()
		if env.snapshotState() != before || len(env.chain.sends) != 0 || len(sendsAfter) != len(sendsBefore) ||
			!strings.Contains(report.Error, "not probed") {
			t.Fatalf("a dry run must not probe: %+v", report)
		}
	})
	t.Run("outcome commands", func(t *testing.T) {
		env := newDisposeEnv(t)
		outcome, _, occupier := env.blockedOutcome()
		env.chain.hide(occupier.Hash().Hex(), false)
		before := env.snapshotState()
		for _, report := range []*EvmDisposeReport{
			env.outcomeCmd(EvmDisposeResolveOutcome, outcome.ID, EvmDisposeRequest{ScanBlocks: 100, DryRun: true}),
			env.outcomeCmd(EvmDisposeRelease, outcome.ID, EvmDisposeRequest{DecisionRef: "board/1", BusinessEvidence: "x", DryRun: true}),
		} {
			expectExit(t, report, EvmDisposeExitOK, "")
			if report.Applied || report.Action == "" {
				t.Fatalf("dry run applied: %+v", report)
			}
		}
		// dispose-outcome needs a succeeded row; a dry run still checks the state
		expectExit(t, env.outcomeCmd(EvmDisposeDisposeOutcome, outcome.ID, EvmDisposeRequest{DryRun: true}), EvmDisposeExitPrecondition, "")
		if env.snapshotState() != before || len(env.chain.sends) != 0 {
			t.Fatal("dry runs must change nothing")
		}
	})
}

// newGapEnv starts the signer one nonce ahead of the chain, so the first
// send is refused as nonce too high.
func newGapEnv(t *testing.T) *disposeEnv {
	t.Helper()
	node := newFakeEvmNode(t, scenarioNonce)
	installFakeEvmSeams(t, node)
	client := newFakeEvmSignClient(t, node, scenarioNonce)
	client.operatorAccNonce = scenarioNonce + 1
	return &disposeEnv{t: t, node: node, chain: newFakeDisposeChain(t, node), store: memStoreOf(t, client), client: client}
}

// refusedTooHigh: the first send is refused as nonce too high, recording
// the refusal fails, and the signer restarts, so the refusal is lost and the
// row is an ordinary pending row (T11c-high (3)).
func (env *disposeEnv) refusedTooHigh() pendingRecord {
	env.t.Helper()
	env.store.inject("resolve", memFaultFail)
	if _, err := deposit(scenarioCtx(env.t), env.client, 1000); err == nil || isEvmNonceRejection(err) {
		env.t.Fatalf("the refusal must not be reported as a nonce rejection while unrecorded: %v", err)
	}
	env.restart()
	pending, ok := env.store.pendingOf(SignOperator)
	if !ok || pending.Nonce != scenarioNonce+1 || pending.Ambiguous {
		env.t.Fatalf("expected the lost refusal as an ordinary pending row: %+v", pending)
	}
	return pending
}

// T11c-high (CLI) and T19b: a nonce gap is never resolved by looking for an
// occupier; --accept-gap turns it into blocked_unknown(nonce_gap), the scope
// signs again from the chain nonce, and once N is used resolve-outcome
// settles X from the chain.
func TestDisposeNonceGap(t *testing.T) {
	t.Run("refused without accept-gap", func(t *testing.T) {
		env := newGapEnv(t)
		pending := env.refusedTooHigh()
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchNonceGap)
		if report.Samples[0].Occupier != nil || len(env.chain.sends) != 2 || env.auditCount(evmAuditManualRejected) != 1 {
			t.Fatalf("expected two probes, no occupier and a rejection: %+v", report)
		}
	})
	t.Run("pending view disagrees", func(t *testing.T) {
		env := newGapEnv(t)
		pending := env.refusedTooHigh()
		env.chain.base-- // the nonce at the height is below the node's pending nonce
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{AcceptGap: true})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchGapUnproven)
		if _, ok := env.store.pendingOf(SignOperator); !ok || len(env.chain.sends) != 0 {
			t.Fatal("the pending row must stay, unprobed")
		}
	})
	t.Run("probe not refused as too high", func(t *testing.T) {
		env := newGapEnv(t)
		pending := env.refusedTooHigh()
		env.chain.sendOverride = func(*ethtypes.Transaction) error {
			return &fakeRPCError{code: -32000, msg: "insufficient funds"}
		}
		report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{AcceptGap: true})
		expectExit(t, report, EvmDisposeExitNotProven, EvmBranchGapUnproven)
		if _, ok := env.store.pendingOf(SignOperator); !ok {
			t.Fatal("the pending row must stay")
		}
	})
	t.Run("gap filled before manual action", func(t *testing.T) {
		env := newGapEnv(t)
		pending := env.refusedTooHigh()
		env.chain.addEvmTx(signTx(t, scenarioNonce, plainTransferTo, 1, nil, 1_000_000_000), ethtypes.ReceiptStatusSuccessful)
		_, _ = deposit(scenarioCtx(t), env.client, 1000) // the re-send is included now
		hash, err := deposit(scenarioCtx(t), env.client, 1000)
		if err != nil || hash != pending.TxHash.String() {
			t.Fatalf("the original must be the result, got %q %v", hash, err)
		}
		assertExecutedOnce(t, env.node, env.node.sentTx(t, 0))
	})
	for _, tc := range []struct {
		name   string
		fill   func(env *disposeEnv, original *ethtypes.Transaction)
		code   int
		branch string
	}{
		{"own other operation", func(env *disposeEnv, _ *ethtypes.Transaction) {
			if _, err := deposit(scenarioCtx(env.t), env.client, 3000); err != nil {
				env.t.Fatal(err)
			}
		}, EvmDisposeExitOK, EvmBranchOwnOtherOp},
		{"same call", func(env *disposeEnv, original *ethtypes.Transaction) {
			env.chain.addEvmTx(sameCall(env.t, original), ethtypes.ReceiptStatusSuccessful)
		}, EvmDisposeExitOK, EvmBranchSameCall},
		{"chain id", func(env *disposeEnv, _ *ethtypes.Transaction) {
			if _, err := deposit(scenarioCtx(env.t), env.client, 3000); err != nil {
				env.t.Fatal(err)
			}
			env.chain.chainID = 1
		}, EvmDisposeExitError, ""},
	} {
		t.Run("accept-gap then "+tc.name, func(t *testing.T) {
			env := newGapEnv(t)
			pending := env.refusedTooHigh()
			original := env.node.sentTx(t, 0)
			report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{AcceptGap: true})
			expectExit(t, report, EvmDisposeExitOK, EvmBranchNonceGap)
			report = env.outcomeCmd(EvmDisposeResolveOutcome, env.store.outcomeRows()[0].ID, EvmDisposeRequest{})
			expectExit(t, report, EvmDisposeExitNotProven, EvmBranchNonceNotReached)
			outcomes := env.store.outcomeRows()
			if len(outcomes) != 1 || outcomes[0].Reason != evmReasonNonceGap || env.auditCount(evmAuditManualResolveNonceGap) != 1 {
				t.Fatalf("unexpected outcome: %+v", outcomes)
			}
			// the scope signs again from the chain nonce M; X stays blocked
			hash, err := deposit(scenarioCtx(t), env.client, 2000)
			if err != nil || env.node.sentTx(t, len(env.sentAll())-1).Nonce() != scenarioNonce {
				t.Fatalf("another operation must be signed at M: %q %v", hash, err)
			}
			if env.auditCount(evmAuditPendingVanished) != 1 {
				t.Fatal("the signer must notice the removed pending row")
			}
			env.expectBlocked(1000, "blocked")
			// N is now the next nonce: still unproven
			report = env.outcomeCmd(EvmDisposeResolveOutcome, outcomes[0].ID, EvmDisposeRequest{ScanBlocks: 100})
			expectExit(t, report, EvmDisposeExitNotProven, EvmBranchNonceUnused)

			env.restart()
			tc.fill(env, original)
			report = env.outcomeCmd(EvmDisposeResolveOutcome, outcomes[0].ID, EvmDisposeRequest{ScanBlocks: 100})
			expectExit(t, report, tc.code, tc.branch)
			for _, h := range env.chain.byHashCalls {
				if h == pending.TxHash {
					t.Fatal("the original must never be fetched from the RPC")
				}
			}
			switch tc.branch {
			case EvmBranchOwnOtherOp:
				env.expectResigned(1000, original)
			case EvmBranchSameCall:
				hash, err := deposit(scenarioCtx(t), env.client, 1000)
				if err != nil || hash == pending.TxHash.String() || hash == "" {
					t.Fatalf("the operation must get the occupier's hash: %q %v", hash, err)
				}
				assertExecutedOnce(t, env.node, original)
			default:
				if rows := env.store.outcomeRows(); len(rows) != 1 || rows[0].State != evmOutcomeBlockedUnknown {
					t.Fatalf("the outcome must stay: %+v", rows)
				}
			}
		})
	}
}

func (env *disposeEnv) sentAll() []fakeEvmSend {
	_, sends, _ := env.node.snapshot()
	return sends
}

// T11c-low (CLI): the first send is refused as nonce too low because N is
// already used, recording the refusal fails and the signer restarts. The row
// loads as an ordinary pending row and the CLI decides from the occupier with
// the T4 / T4c / T4e fixtures; the signer then follows what the CLI stored.
func TestDisposeLostNonceLowRefusal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		occupier func(t *testing.T, env *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction
		req      EvmDisposeRequest
		code     int
		branch   string
	}{
		{"own other operation", func(t *testing.T, env *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction {
			other := signTx(t, original.Nonce(), ethcmn.HexToAddress("0x2000"), 0, []byte{0x03}, 1_000_000_000)
			env.putOwnEvidence(other, depositOperation(t, 555))
			return other
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchOwnOtherOp},
		{"plain transfer", func(t *testing.T, _ *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction {
			return signTx(t, original.Nonce(), plainTransferTo, 5, nil, 1_000_000_000)
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchPlainTransfer},
		{"same call", func(t *testing.T, _ *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction {
			return sameCall(t, original)
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchSameCall},
		{"undetermined", func(t *testing.T, _ *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction {
			return precompileCall(t, original.Nonce())
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitNotProven, EvmBranchUndetermined},
		{"not found", func(t *testing.T, _ *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction {
			return precompileCall(t, original.Nonce())
		}, EvmDisposeRequest{}, EvmDisposeExitNotProven, EvmBranchOccupierMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newDisposeEnv(t)
			// the occupier lands just before the node sees the deposit
			var occupier *ethtypes.Transaction
			env.node.script = func(send int, tx *ethtypes.Transaction) fakeEvmAction {
				if send == 1 {
					occupier = tc.occupier(t, env, tx)
					env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)
				}
				return fakeEvmAction{}
			}
			env.store.inject("resolve", memFaultFail)
			if _, err := deposit(scenarioCtx(t), env.client, 1000); err == nil || isEvmNonceRejection(err) {
				t.Fatalf("an unrecorded refusal must be a plain error: %v", err)
			}
			env.node.script = nil
			original := env.node.sentTx(t, 0)
			env.restart()
			pending, ok := env.store.pendingOf(SignOperator)
			if !ok || pending.Ambiguous {
				t.Fatalf("expected an ordinary pending row: %+v", pending)
			}
			report := env.resolve(SignOperator, pending.TxHash, tc.req)
			expectExit(t, report, tc.code, tc.branch)

			switch tc.branch {
			case EvmBranchOwnOtherOp, EvmBranchPlainTransfer:
				if _, ok := env.store.pendingOf(SignOperator); ok || len(env.store.outcomeRows()) != 0 ||
					env.auditCount(evmAuditManualResolveOtherUnrelated) != 1 {
					t.Fatal("the pending row must be deleted without an outcome, with its audit")
				}
				env.expectResigned(1000, original)
			case EvmBranchSameCall:
				outcomes := env.store.outcomeRows()
				if _, ok := env.store.pendingOf(SignOperator); ok || len(outcomes) != 1 ||
					outcomes[0].State != evmOutcomeSucceeded || outcomes[0].ResultTxHash != occupier.Hash() {
					t.Fatalf("unexpected outcome: %+v", outcomes)
				}
				hash, err := deposit(scenarioCtx(t), env.client, 1000)
				if err != nil || hash != occupier.Hash().String() {
					t.Fatalf("the operation must get the occupier's hash, got %q %v", hash, err)
				}
				assertExecutedOnce(t, env.node, original)
			default:
				// not proven: the pending row stays and keeps the scope blocked
				if after, ok := env.store.pendingOf(SignOperator); !ok || after != pending || len(env.store.outcomeRows()) != 0 {
					t.Fatalf("the pending row must stay unchanged: %+v", after)
				}
				if env.auditCount(evmAuditManualRejected) != 1 {
					t.Fatal("the refusal must audit manual_rejected")
				}
				sends := len(env.sentAll())
				hash, err := deposit(scenarioCtx(t), env.client, 1000)
				if err == nil || hash != pending.TxHash.String() {
					t.Fatalf("scope must stay blocked: hash=%q err=%v", hash, err)
				}
				// at most the pending bytes go out again; nothing is signed anew
				for _, send := range env.sentAll()[sends:] {
					if send.hash != pending.TxHash {
						t.Fatalf("a new transaction %s was signed", send.hash.Hex())
					}
				}
				if n := env.node.executionsOf(original); n != 0 {
					t.Fatalf("operation executed %d times; %s", n, env.node.describe())
				}
			}
		})
	}
}

// T18: the outcome commands act only on the row and state they were run
// for: a stale id, a second run, or a row the signer consumed meanwhile is
// refused with exit code 3 and manual_rejected, and nothing else changes.
func TestDisposeOutcomePreconditions(t *testing.T) {
	succeeded := func(t *testing.T) (*disposeEnv, outcomeRecord) {
		env := newDisposeEnv(t)
		original := ambiguousDeposit(t, env.node, env.client)
		expectExit(t, env.resolve(SignOperator, original.Hash(), EvmDisposeRequest{}), EvmDisposeExitOK, EvmBranchOriginalSucceeded)
		return env, env.store.outcomeRows()[0]
	}
	t.Run("stale id and second run", func(t *testing.T) {
		env, outcome := succeeded(t)
		expectExit(t, env.outcomeCmd(EvmDisposeDisposeOutcome, outcome.ID, EvmDisposeRequest{}), EvmDisposeExitOK, "")
		if env.auditCount(evmAuditManualDisposeOutcome) != 1 || len(env.store.outcomeRows()) != 0 {
			t.Fatal("dispose-outcome must delete the row with its audit")
		}
		// the next round of the same operation gets a new row with a new id
		next := ambiguousDeposit2(t, env)
		expectExit(t, env.resolve(SignOperator, next.Hash(), EvmDisposeRequest{}), EvmDisposeExitOK, EvmBranchOriginalSucceeded)
		before := env.store.outcomeRows()
		for _, command := range []string{EvmDisposeDisposeOutcome, EvmDisposeResolveOutcome, EvmDisposeRelease} {
			report := env.outcomeCmd(command, outcome.ID, EvmDisposeRequest{DecisionRef: map[bool]string{true: "board/1"}[command == EvmDisposeRelease],
				BusinessEvidence: map[bool]string{true: "x"}[command == EvmDisposeRelease]})
			expectExit(t, report, EvmDisposeExitPrecondition, "")
		}
		if after := env.store.outcomeRows(); len(after) != 1 || after[0] != before[0] || before[0].ID == outcome.ID {
			t.Fatalf("the next round's row must be untouched: %+v", after)
		}
		if env.auditCount(evmAuditManualRejected) != 3 {
			t.Fatal("each stale command must audit manual_rejected")
		}
		rejected := env.outcomeCmd(EvmDisposeDisposeOutcome, outcome.ID, EvmDisposeRequest{})
		if audits := env.store.auditEvents(evmAuditManualRejected); rejected.Event != evmAuditManualRejected ||
			rejected.Applied || rejected.Actor != audits[len(audits)-1].Actor || rejected.OperatorReason != audits[len(audits)-1].Reason {
			t.Fatalf("the report must match the rejection audit: %+v", rejected)
		}
		// wrong state: release needs blocked_unknown
		expectExit(t, env.outcomeCmd(EvmDisposeRelease, before[0].ID, EvmDisposeRequest{DecisionRef: "b", BusinessEvidence: "x"}),
			EvmDisposeExitPrecondition, "")
	})
	t.Run("dry run", func(t *testing.T) { // T9 dispose-outcome
		env, outcome := succeeded(t)
		before := env.snapshotState()
		report := env.outcomeCmd(EvmDisposeDisposeOutcome, outcome.ID, EvmDisposeRequest{DryRun: true})
		expectExit(t, report, EvmDisposeExitOK, "")
		if report.Applied || report.Action == "" || env.snapshotState() != before || len(env.chain.sends) != 0 {
			t.Fatalf("a dry run must change nothing: %+v", report)
		}
	})
	t.Run("signer consumes concurrently", func(t *testing.T) {
		env, outcome := succeeded(t)
		env.store.hookOnce("manual_settle", func(m *memEvmStateStore) { delete(m.outcomes, outcome.ID) })
		report := env.outcomeCmd(EvmDisposeDisposeOutcome, outcome.ID, EvmDisposeRequest{})
		expectExit(t, report, EvmDisposeExitPrecondition, "")
		if env.auditCount(evmAuditManualDisposeOutcome) != 0 || env.auditCount(evmAuditManualRejected) != 1 {
			t.Fatal("only one side may take the row")
		}
	})
	t.Run("pending handled concurrently", func(t *testing.T) {
		env := newDisposeEnv(t)
		pending, _ := env.stuckDeposit(1000)
		env.chain.addEvmTx(signTx(t, pending.Nonce, plainTransferTo, 1, nil, 1_000_000_000), ethtypes.ReceiptStatusSuccessful)
		env.store.hookOnce("manual_resolve", func(m *memEvmStateStore) { delete(m.pending, pending.ID) })
		expectExit(t, env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100}), EvmDisposeExitPrecondition, "")
		if env.auditCount(evmAuditManualResolveOtherUnrelated) != 0 {
			t.Fatal("the transition must not commit")
		}
	})
}

// ambiguousDeposit2 is a second identical deposit, committed with its
// response lost, after the first round's outcome was disposed.
func ambiguousDeposit2(t *testing.T, env *disposeEnv) *ethtypes.Transaction {
	t.Helper()
	_, before, _ := env.node.snapshot()
	env.node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == len(before)+1 {
			return fakeEvmAction{kind: fakeEvmAdmitThenDrop}
		}
		return fakeEvmAction{}
	}
	hash, err := deposit(scenarioCtx(t), env.client, 1000)
	env.node.script = nil
	tx := env.node.sentTx(t, len(before))
	if err == nil || hash != tx.Hash().String() {
		t.Fatalf("expected the second round pending: %q %v", hash, err)
	}
	return tx
}

// T18b: release deletes a blocked_unknown row on a Board decision; the
// operation is then signed again and the audit keeps the decision and the
// business evidence.
func TestDisposeRelease(t *testing.T) {
	env := newDisposeEnv(t)
	outcome, original, _ := env.blockedOutcome()
	report := env.outcomeCmd(EvmDisposeRelease, outcome.ID, EvmDisposeRequest{
		DecisionRef: "https://board.example/decisions/42", BusinessEvidence: "GVG 1 deposit balance unchanged at height 120",
	})
	expectExit(t, report, EvmDisposeExitOK, "")
	audits := env.store.auditEvents(evmAuditManualReleaseUnknown)
	if len(audits) != 1 || audits[0].DecisionRef != "https://board.example/decisions/42" ||
		!strings.Contains(audits[0].Detail, "GVG 1 deposit balance unchanged") || *audits[0].OutcomeID != outcome.ID {
		t.Fatalf("unexpected audit: %+v", audits)
	}
	// the report carries what the CLI logs next to the audit row
	if report.Event != evmAuditManualReleaseUnknown || report.Actor != audits[0].Actor ||
		report.DecisionRef != audits[0].DecisionRef || report.OperatorReason != audits[0].Reason || !report.Applied {
		t.Fatalf("the report must match the audit: %+v", report)
	}
	env.expectResigned(1000, original)
}

// T19: a blocked_unknown(receipt_missing) row is settled after a full
// restart from its evidence alone; the RPC never returns the original.
func TestDisposeResolveOutcomeAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		occupier func(t *testing.T, env *disposeEnv, original *ethtypes.Transaction) *ethtypes.Transaction
		change   func(env *disposeEnv)
		req      EvmDisposeRequest
		code     int
		branch   string
	}{
		{"(i) same call", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return sameCall(t, o)
		},
			nil, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchSameCall},
		{"(ii) own other operation", func(t *testing.T, env *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			other := signTx(t, o.Nonce(), ethcmn.HexToAddress("0x2000"), 0, []byte{0x04}, 1_000_000_000)
			env.putOwnEvidence(other, depositOperation(t, 556))
			return other
		}, nil, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchOwnOtherOp},
		{"(iii) plain transfer", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return signTx(t, o.Nonce(), plainTransferTo, 1, nil, 1_000_000_000)
		}, nil, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchPlainTransfer},
		// the occupier is at block 101: code deployed later does not matter,
		// code present then (and self-destructed since) does
		{"(iii) contract deployed after the transfer", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return signTx(t, o.Nonce(), plainTransferTo, 1, nil, 1_000_000_000)
		}, func(env *disposeEnv) {
			env.chain.codeAt = func(_ ethcmn.Address, height uint64) ([]byte, error) {
				if height >= 102 {
					return []byte{0x60}, nil
				}
				return nil, nil
			}
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitOK, EvmBranchPlainTransfer},
		{"(iii) contract self-destructed after the call", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return signTx(t, o.Nonce(), plainTransferTo, 1, nil, 1_000_000_000)
		}, func(env *disposeEnv) {
			env.chain.codeAt = func(_ ethcmn.Address, height uint64) ([]byte, error) {
				if height <= 101 {
					return []byte{0x60}, nil
				}
				return nil, nil
			}
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitNotProven, EvmBranchUndetermined},
		{"(iii) no historical state", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return signTx(t, o.Nonce(), plainTransferTo, 1, nil, 1_000_000_000)
		}, func(env *disposeEnv) {
			env.chain.codeAt = func(ethcmn.Address, uint64) ([]byte, error) { return nil, errMemInjected }
		}, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitNotProven, EvmBranchUndetermined},
		{"(iv) other precompile call", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return precompileCall(t, o.Nonce())
		}, nil, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitNotProven, EvmBranchUndetermined},
		{"(v) chain id", func(t *testing.T, _ *disposeEnv, o *ethtypes.Transaction) *ethtypes.Transaction {
			return sameCall(t, o)
		},
			func(env *disposeEnv) { env.chain.chainID = 9 }, EvmDisposeRequest{ScanBlocks: 100}, EvmDisposeExitError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newDisposeEnv(t)
			pending, original := env.stuckDeposit(1000)
			occupier := tc.occupier(t, env, original)
			env.chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)
			env.chain.hide(occupier.Hash().Hex(), true)
			expectExit(t, env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true}),
				EvmDisposeExitOK, EvmBranchOccupierMissing)
			if _, ok := env.store.pendingOf(SignOperator); ok {
				t.Fatal("the pending row must be gone")
			}
			outcome := env.store.outcomeRows()[0]

			env.restart()
			env.chain.hide(occupier.Hash().Hex(), false)
			env.chain.blockRequests = nil
			if tc.change != nil {
				tc.change(env)
			}
			report := env.outcomeCmd(EvmDisposeResolveOutcome, outcome.ID, tc.req)
			expectExit(t, report, tc.code, tc.branch)
			for _, h := range env.chain.byHashCalls {
				if h == pending.TxHash {
					t.Fatal("the original must never be fetched from the RPC")
				}
			}
			if tc.code == EvmDisposeExitOK && env.chain.blockRequests[0] != 100 {
				t.Fatalf("the scan must start at the recorded block 100, started at %d", env.chain.blockRequests[0])
			}
			switch tc.branch {
			case EvmBranchSameCall:
				hash, err := deposit(scenarioCtx(t), env.client, 1000)
				if err != nil || hash != occupier.Hash().String() {
					t.Fatalf("the operation must get the occupier's hash: %q %v", hash, err)
				}
				assertExecutedOnce(t, env.node, original)
			case EvmBranchOwnOtherOp, EvmBranchPlainTransfer:
				env.expectResigned(1000, original)
			default:
				if rows := env.store.outcomeRows(); len(rows) != 1 || rows[0] != outcome {
					t.Fatalf("the outcome must stay: %+v", rows)
				}
				env.expectBlocked(1000, "blocked")
			}
		})
	}
}

// T19c: the CLI checks the evidence before deciding: a corrupt or
// inconsistent row exits 4 and audits evidence_invalid, a DB read error
// exits 1 without audit, and the rows stay as they were.
func TestDisposeInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(env *disposeEnv, outcome outcomeRecord)
		code   int
	}{
		{"raw byte flipped", func(env *disposeEnv, o outcomeRecord) {
			env.store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.Raw[len(e.Raw)-1] ^= 0xff })
		}, EvmDisposeExitEvidenceInvalid},
		{"sender", func(env *disposeEnv, o outcomeRecord) {
			env.store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.From = ethcmn.HexToAddress("0x01") })
		}, EvmDisposeExitEvidenceInvalid},
		{"evm chain id", func(env *disposeEnv, o outcomeRecord) {
			env.store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.EvmChainID = 1 })
		}, EvmDisposeExitEvidenceInvalid},
		{"outcome nonce", func(env *disposeEnv, o outcomeRecord) {
			env.store.tamperOutcome(o.ID, func(r *outcomeRecord) { r.Nonce++ })
		}, EvmDisposeExitEvidenceInvalid},
		{"db read error", func(env *disposeEnv, _ outcomeRecord) {
			env.store.inject("get_signed_tx", memFaultFail, memFaultFail)
		}, EvmDisposeExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newDisposeEnv(t)
			outcome, _, occupier := env.blockedOutcome()
			env.chain.hide(occupier.Hash().Hex(), false)
			tc.damage(env, outcome)
			rows := env.store.outcomeRows()
			for _, report := range []*EvmDisposeReport{
				env.outcomeCmd(EvmDisposeResolveOutcome, outcome.ID, EvmDisposeRequest{ScanBlocks: 100}),
				env.outcomeCmd(EvmDisposeRelease, outcome.ID, EvmDisposeRequest{DecisionRef: "board/1", BusinessEvidence: "x"}),
			} {
				expectExit(t, report, tc.code, "")
			}
			if after := env.store.outcomeRows(); len(after) != 1 || after[0] != rows[0] {
				t.Fatalf("the row must stay: %+v", after)
			}
			want := 2
			if tc.code == EvmDisposeExitError {
				want = 0
			}
			if n := env.auditCount(evmAuditEvidenceInvalid); n != want {
				t.Fatalf("want %d evidence_invalid audits, got %d", want, n)
			}
			env.restart()
			env.expectBlocked(1000, "")
		})
	}
	// resolve checks the evidence of the pending row the same way
	for _, tc := range []struct {
		name   string
		damage func(env *disposeEnv, pending pendingRecord)
		code   int
	}{
		{"raw byte flipped", func(env *disposeEnv, p pendingRecord) {
			env.store.tamper(p.SignedTxID, func(e *signedTxEvidence) { e.Raw[len(e.Raw)-1] ^= 0xff })
		}, EvmDisposeExitEvidenceInvalid},
		{"sender", func(env *disposeEnv, p pendingRecord) {
			env.store.tamper(p.SignedTxID, func(e *signedTxEvidence) { e.From = ethcmn.HexToAddress("0x01") })
		}, EvmDisposeExitEvidenceInvalid},
		{"evm chain id", func(env *disposeEnv, p pendingRecord) {
			env.store.tamper(p.SignedTxID, func(e *signedTxEvidence) { e.EvmChainID = 1 })
		}, EvmDisposeExitEvidenceInvalid},
		{"referenced nonce", func(env *disposeEnv, p pendingRecord) {
			env.store.tamperPending(p.ID, func(r *pendingRecord) { r.Nonce++ })
		}, EvmDisposeExitEvidenceInvalid},
		{"db read error", func(env *disposeEnv, _ pendingRecord) {
			env.store.inject("get_signed_tx", memFaultFail)
		}, EvmDisposeExitError},
	} {
		t.Run("pending "+tc.name, func(t *testing.T) {
			env := newDisposeEnv(t)
			pending, _ := env.stuckDeposit(1000)
			tc.damage(env, pending)
			before := env.store.pendingRows()
			sends := len(env.sentAll())
			expectExit(t, env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{ScanBlocks: 100, AcceptUnknown: true}), tc.code, "")
			if after := env.store.pendingRows(); len(after) != 1 || after[0] != before[0] || len(env.store.outcomeRows()) != 0 {
				t.Fatalf("the pending row must stay: %+v", after)
			}
			want := 1
			if tc.code == EvmDisposeExitError {
				want = 0
			}
			if n := env.auditCount(evmAuditEvidenceInvalid); n != want {
				t.Fatalf("want %d evidence_invalid audits, got %d", want, n)
			}
			if len(env.sentAll()) != sends || len(env.chain.sends) != 0 || len(env.chain.blockRequests) != 0 {
				t.Fatal("nothing may be sent or scanned on the chain")
			}
		})
	}
}

// T11c-high: a nonce gap holds only when the account nonce M is the same at
// both heights; samples that see M move disagree.
func TestDisposeGapSamplesAgreeOnNonce(t *testing.T) {
	for _, branch := range []string{EvmBranchNonceGap, EvmBranchGapUnproven, EvmBranchGapNotProbed} {
		a := EvmDisposeSample{Height: 101, ChainNonce: 5, Branch: branch}
		b := EvmDisposeSample{Height: 102, ChainNonce: 6, Branch: branch}
		if a.key() == b.key() {
			t.Fatalf("%s: samples at nonces 5 and 6 must disagree", branch)
		}
		b.ChainNonce = 5
		if a.key() != b.key() {
			t.Fatalf("%s: samples at the same nonce must agree", branch)
		}
	}
}
