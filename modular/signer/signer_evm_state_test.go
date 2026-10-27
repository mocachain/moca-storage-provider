package signer

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/sdk/client"
)

// Scenarios T1–T3c, T10–T14 (with T13c/T13d for lease epochs), T17, T17b and the signer part of T19c of the
// SOL-486 plan (revision 4): EVM pending state persisted in the state store.
// A "restart" is a new client on the same store with empty memory; the
// fake's execution count stands in for a non-idempotent effect (a deposit).

func deposit(ctx context.Context, c *MocaChainSignClient, amount int64) (string, error) {
	return c.DepositEvm(ctx, SignOperator, fakeDepositMsg(amount))
}

// restartClient models a process restart: the old process is gone and its
// lease has expired, and a new client starts on the same store.
func restartClient(t *testing.T, node *fakeEvmNode, store *memEvmStateStore) *MocaChainSignClient {
	t.Helper()
	store.expireLease(fakeEvmChainID)
	_, _, nonce := node.snapshot()
	return newFakeEvmSignClientWithStore(t, node, nonce, store)
}

func withSendFn(t *testing.T, fn func(ctx context.Context, evmClient *ethclient.Client, tx *ethtypes.Transaction) error) {
	t.Helper()
	original := sendEvmTxFn
	t.Cleanup(func() { sendEvmTxFn = original })
	sendEvmTxFn = fn
}

func assertExecutedOnce(t *testing.T, node *fakeEvmNode, tx *ethtypes.Transaction) {
	t.Helper()
	if n := node.executionsOf(tx); n != 1 {
		t.Fatalf("operation executed %d times; %s", n, node.describe())
	}
}

// ambiguousDeposit submits a deposit that is committed but whose response is
// lost and whose receipt is hidden, leaving an ambiguous pending row.
func ambiguousDeposit(t *testing.T, node *fakeEvmNode, c *MocaChainSignClient) *ethtypes.Transaction {
	t.Helper()
	node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
	node.setHideReceipts(true)
	hash, err := deposit(scenarioCtx(t), c, 1000)
	tx := node.sentTx(t, 0)
	if err == nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash with an error, got hash=%q err=%v", hash, err)
	}
	return tx
}

// T1: recorded, then the process dies before any byte reaches the node.
func TestEvmStateRecordedNotBroadcastSurvivesRestart(t *testing.T) {
	node, first := newScenario(t)
	store := memStoreOf(t, first)
	withSendFn(t, func(context.Context, *ethclient.Client, *ethtypes.Transaction) error {
		return context.DeadlineExceeded // never reaches the node
	})
	if _, err := deposit(scenarioCtx(t), first, 1000); err == nil {
		t.Fatal("expected an unconfirmed error")
	}
	if _, sends, _ := node.snapshot(); len(sends) != 0 {
		t.Fatalf("bytes reached the node; %s", node.describe())
	}
	row, ok := store.pendingOf(SignOperator)
	if !ok {
		t.Fatal("recorded transaction is not pending")
	}
	evidence, _ := store.signedRow(row.SignedTxID)
	sendEvmTxFn = func(ctx context.Context, evmClient *ethclient.Client, tx *ethtypes.Transaction) error {
		return evmClient.SendTransaction(ctx, tx)
	}

	second := restartClient(t, node, store) // background resolution re-sends
	_, sends, _ := node.snapshot()
	if len(sends) != 1 || string(sends[0].raw) != string(evidence.Raw) {
		t.Fatalf("restart must re-send exactly the recorded bytes; %s", node.describe())
	}
	hash, err := deposit(scenarioCtx(t), second, 1000)
	if err != nil || hash != row.TxHash.String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	assertExecutedOnce(t, node, node.sentTx(t, 0))
	if node.distinctSentHashes() != 1 {
		t.Fatalf("the node saw more than one transaction; %s", node.describe())
	}
}

// T2: broadcast, no receipt, restart.
func TestEvmStateUnconfirmedSurvivesRestart(t *testing.T) {
	node, first := newScenario(t)
	tx := ambiguousDeposit(t, node, first)

	second := restartClient(t, node, memStoreOf(t, first))
	hash, err := deposit(scenarioCtx(t), second, 1000)
	if err == nil || !strings.Contains(err.Error(), "unconfirmed") || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash with an unconfirmed error, got hash=%q err=%v", hash, err)
	}
	node.setHideReceipts(false)
	if hash, err = deposit(scenarioCtx(t), second, 1000); err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	assertSignedOnce(t, node)
	assertExecutedOnce(t, node, tx)
}

// confirmedBeforeRestart leaves a committed deposit whose receipt becomes
// visible before the restart, and restarts: background resolution keeps a
// succeeded outcome.
func confirmedBeforeRestart(t *testing.T) (*fakeEvmNode, *memEvmStateStore, *ethtypes.Transaction, *MocaChainSignClient) {
	t.Helper()
	node, first := newScenario(t)
	tx := ambiguousDeposit(t, node, first)
	node.setHideReceipts(false)
	store := memStoreOf(t, first)
	second := restartClient(t, node, store)
	outcomes := store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].State != evmOutcomeSucceeded || outcomes[0].ResultTxHash != tx.Hash() ||
		outcomes[0].Source != evmOutcomeSourceResolve {
		t.Fatalf("background resolution must keep a succeeded outcome, got %+v", outcomes)
	}
	if _, ok := store.pendingOf(SignOperator); ok {
		t.Fatal("resolved transaction is still pending")
	}
	return node, store, tx, second
}

// T3: already mined before the restart.
func TestEvmStateMinedBeforeRestartReturnsOriginalHash(t *testing.T) {
	node, store, tx, second := confirmedBeforeRestart(t)

	hash, err := deposit(scenarioCtx(t), second, 1000)
	if err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	if len(store.outcomeRows()) != 0 || len(store.auditEvents(evmAuditOutcomeConsumed)) != 1 {
		t.Fatal("the outcome must be consumed exactly once")
	}
	assertExecutedOnce(t, node, tx)

	// another operation signs at the next nonce
	if _, err := deposit(scenarioCtx(t), second, 2000); err != nil {
		t.Fatal(err)
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 2 || accepted[1].Nonce() != scenarioNonce+1 {
		t.Fatalf("expected the next operation at nonce %d; %s", scenarioNonce+1, node.describe())
	}
}

// T3a: restart again between background resolution and the business retry.
func TestEvmStateOutcomeSurvivesSecondRestart(t *testing.T) {
	node, store, tx, _ := confirmedBeforeRestart(t)
	third := restartClient(t, node, store)
	hash, err := deposit(scenarioCtx(t), third, 1000)
	if err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	assertExecutedOnce(t, node, tx)
}

// T3b: mined with a failed receipt before the restart.
func TestEvmStateFailedReceiptAfterRestartPermitsResign(t *testing.T) {
	node, first := newScenario(t)
	tx := ambiguousDeposit(t, node, first)
	node.setReceiptStatus(tx.Hash(), ethtypes.ReceiptStatusFailed)
	node.setHideReceipts(false)
	store := memStoreOf(t, first)

	second := restartClient(t, node, store)
	if _, ok := store.pendingOf(SignOperator); ok || len(store.outcomeRows()) != 0 {
		t.Fatal("a failed receipt must delete the pending row without keeping an outcome")
	}
	hash, err := deposit(scenarioCtx(t), second, 1000)
	if err != nil || hash == tx.Hash().String() {
		t.Fatalf("expected a new signature, got hash=%q err=%v", hash, err)
	}
	accepted, _, _ := node.snapshot()
	if accepted[len(accepted)-1].Nonce() != scenarioNonce+1 {
		t.Fatalf("expected the re-signed operation at nonce %d; %s", scenarioNonce+1, node.describe())
	}
	assertExecutedOnce(t, node, tx)
}

// T3c: another operation arrives first and resolves the successful pending row.
func TestEvmStateOtherOperationKeepsSuccessForRetry(t *testing.T) {
	node, c := newScenario(t)
	tx := ambiguousDeposit(t, node, c)
	node.setHideReceipts(false)

	if _, err := deposit(scenarioCtx(t), c, 2000); err != nil {
		t.Fatal(err)
	}
	store := memStoreOf(t, c)
	if len(store.auditEvents(evmAuditOutcomeCreated)) != 1 {
		t.Fatal("the original success was not kept")
	}
	hash, err := deposit(scenarioCtx(t), c, 1000)
	if err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	assertExecutedOnce(t, node, tx)
	accepted, _, _ := node.snapshot()
	if len(accepted) != 2 {
		t.Fatalf("expected exactly the two operations on chain; %s", node.describe())
	}
}

// T10: the Record transaction fails or its commit result is unknown.
func TestEvmStateRecordFailures(t *testing.T) {
	t.Run("rolled back", func(t *testing.T) {
		node, c := newScenario(t)
		memStoreOf(t, c).inject("record", memFaultFail)
		hash, err := deposit(scenarioCtx(t), c, 1000)
		if err == nil || hash != "" {
			t.Fatalf("expected no hash and an error, got hash=%q err=%v", hash, err)
		}
		if _, sends, _ := node.snapshot(); len(sends) != 0 || c.operatorAccNonce != scenarioNonce {
			t.Fatalf("nothing may be sent and the nonce must not move; nonce=%d %s", c.operatorAccNonce, node.describe())
		}
	})
	t.Run("commit unknown, row present", func(t *testing.T) {
		node, c := newScenario(t)
		memStoreOf(t, c).inject("record", memFaultCommitUnknownApplied)
		if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
			t.Fatalf("a visible row counts as recorded: %v", err)
		}
		assertExecutedOnce(t, node, node.sentTx(t, 0))
	})
	t.Run("commit unknown, row absent", func(t *testing.T) {
		node, c := newScenario(t)
		memStoreOf(t, c).inject("record", memFaultCommitUnknownLost)
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("expected no hash and an error, got hash=%q err=%v", hash, err)
		}
		if _, sends, _ := node.snapshot(); len(sends) != 0 || c.operatorAccNonce != scenarioNonce {
			t.Fatalf("nothing may be sent; %s", node.describe())
		}
	})
	t.Run("commit unknown, read back fails", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.inject("record", memFaultCommitUnknownApplied)
		store.inject("get_pending", memFaultNone, memFaultFail) // step 4 read, then the read back
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("expected no hash and an error, got hash=%q err=%v", hash, err)
		}
		if _, sends, _ := node.snapshot(); len(sends) != 0 {
			t.Fatalf("nothing may be sent; %s", node.describe())
		}
		// the row did commit, so the scope is blocked by it
		hash, err := deposit(scenarioCtx(t), c, 2000)
		row, _ := store.pendingOf(SignOperator)
		if err == nil || hash != row.TxHash.String() {
			t.Fatalf("the committed row must block the scope, got hash=%q err=%v", hash, err)
		}
	})
}

// T11a: success receipt, then the terminal transaction fails.
func TestEvmStateSuccessWriteFailureIsRetried(t *testing.T) {
	t.Run("next request", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.inject("resolve", memFaultFail)
		hash, err := deposit(scenarioCtx(t), c, 1000)
		tx := node.sentTx(t, 0)
		if err == nil || hash != tx.Hash().String() {
			t.Fatalf("expected the hash with an error, got hash=%q err=%v", hash, err)
		}
		if _, ok := store.pendingOf(SignOperator); !ok {
			t.Fatal("pending row must stay until the success is recorded")
		}
		if hash, err = deposit(scenarioCtx(t), c, 1000); err != nil || hash != tx.Hash().String() {
			t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
		}
		if _, sends, _ := node.snapshot(); len(sends) != 1 {
			t.Fatalf("nothing may be re-sent; %s", node.describe())
		}
		assertExecutedOnce(t, node, tx)
	})
	t.Run("other operation first", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.inject("resolve", memFaultFail)
		_, _ = deposit(scenarioCtx(t), c, 1000)
		tx := node.sentTx(t, 0)
		if _, err := deposit(scenarioCtx(t), c, 2000); err != nil {
			t.Fatal(err)
		}
		hash, err := deposit(scenarioCtx(t), c, 1000)
		if err != nil || hash != tx.Hash().String() {
			t.Fatalf("the retried success must be kept for its operation, got hash=%q err=%v", hash, err)
		}
		assertExecutedOnce(t, node, tx)
	})
	t.Run("restart before the write", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.inject("resolve", memFaultFail)
		_, _ = deposit(scenarioCtx(t), c, 1000)
		tx := node.sentTx(t, 0)
		second := restartClient(t, node, store)
		hash, err := deposit(scenarioCtx(t), second, 1000)
		if err != nil || hash != tx.Hash().String() {
			t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
		}
		assertExecutedOnce(t, node, tx)
	})
}

// T11b: failed receipt, then the terminal transaction fails.
func TestEvmStateFailedReceiptWriteFailureIsRetried(t *testing.T) {
	node, c := newScenario(t)
	node.script = func(send int, tx *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			node.setReceiptStatus(tx.Hash(), ethtypes.ReceiptStatusFailed)
		}
		return fakeEvmAction{}
	}
	if _, err := deposit(scenarioCtx(t), c, 1000); err == nil {
		t.Fatal("a failed transaction must be reported")
	}
	store := memStoreOf(t, c)
	store.inject("resolve", memFaultFail)
	if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
		t.Fatalf("an unrecorded failure must block, got hash=%q err=%v", hash, err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 1 {
		t.Fatalf("nothing may be signed while the failure is unrecorded; %s", node.describe())
	}
	if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
		t.Fatalf("after recording the failure the operation is re-signed: %v", err)
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 2 || accepted[1].Nonce() != scenarioNonce+1 {
		t.Fatalf("expected the retry at nonce %d; %s", scenarioNonce+1, node.describe())
	}
}

// T11c and T11d: the first send is refused; recording the refusal fails.
func TestEvmStateRefusalWriteFailures(t *testing.T) {
	type refusal struct {
		name        string
		cachedNonce uint64
		event       string
	}
	for _, rc := range []refusal{
		{"nonce low", scenarioNonce - 1, evmAuditNotAdmittedNonceLow},
		{"nonce high", scenarioNonce + 1, evmAuditNotAdmittedNonceHigh},
	} {
		setup := func(t *testing.T) (*fakeEvmNode, *MocaChainSignClient, *memEvmStateStore) {
			node, c := newScenario(t)
			c.operatorAccNonce = rc.cachedNonce
			return node, c, memStoreOf(t, c)
		}
		assertRecoveredOnce := func(t *testing.T, node *fakeEvmNode, store *memEvmStateStore) {
			t.Helper()
			accepted, _, _ := node.snapshot()
			if len(accepted) != 1 || accepted[0].Nonce() != scenarioNonce {
				t.Fatalf("expected exactly one accepted signature at %d; %s", scenarioNonce, node.describe())
			}
			if len(store.auditEvents(rc.event)) != 1 {
				t.Fatalf("expected one %s audit", rc.event)
			}
		}
		t.Run(rc.name+"/rolled back", func(t *testing.T) {
			node, c, store := setup(t)
			store.inject("resolve", memFaultFail)
			hash, err := deposit(scenarioCtx(t), c, 1000)
			if err == nil || hash != "" || isEvmNonceRejection(err) {
				t.Fatalf("expected a plain error, got hash=%q err=%v", hash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 1 {
				t.Fatalf("an unrecorded refusal must not be re-signed; %s", node.describe())
			}
			if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
				t.Fatalf("next request records the refusal, refreshes the nonce and signs: %v", err)
			}
			assertRecoveredOnce(t, node, store)
		})
		t.Run(rc.name+"/commit unknown, done", func(t *testing.T) {
			node, c, store := setup(t)
			store.inject("resolve", memFaultCommitUnknownApplied)
			if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
				t.Fatalf("a refusal visible on read back follows the normal path: %v", err)
			}
			assertRecoveredOnce(t, node, store)
		})
		t.Run(rc.name+"/commit unknown, row still there", func(t *testing.T) {
			node, c, store := setup(t)
			store.inject("resolve", memFaultCommitUnknownLost)
			if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
				t.Fatalf("expected a plain error, got hash=%q err=%v", hash, err)
			}
			if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
				t.Fatal(err)
			}
			assertRecoveredOnce(t, node, store)
		})
		t.Run(rc.name+"/commit unknown, read back fails", func(t *testing.T) {
			node, c, store := setup(t)
			store.inject("resolve", memFaultCommitUnknownApplied)
			store.inject("get_pending", memFaultNone, memFaultFail)
			if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
				t.Fatalf("expected a plain error, got hash=%q err=%v", hash, err)
			}
			if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
				t.Fatal(err)
			}
			assertRecoveredOnce(t, node, store)
		})
		t.Run(rc.name+"/restart before the write", func(t *testing.T) {
			node, c, store := setup(t)
			store.inject("resolve", memFaultFail)
			_, _ = deposit(scenarioCtx(t), c, 1000)
			refused := node.sentTx(t, 0)

			second := restartClient(t, node, store)
			hash, err := deposit(scenarioCtx(t), second, 1000)
			if err == nil || hash != refused.Hash().String() {
				t.Fatalf("a refusal lost on restart loads as an ordinary pending row, got hash=%q err=%v", hash, err)
			}
			if row, ok := store.pendingOf(SignOperator); !ok || row.Ambiguous {
				t.Fatalf("the refused row must stay pending (ambiguous=0): %+v ok=%t", row, ok)
			}
			if accepted, _, _ := node.snapshot(); len(accepted) != 0 {
				t.Fatalf("nothing may be signed; %s", node.describe())
			}
			if rc.cachedNonce > scenarioNonce {
				// the gap closes before anyone steps in: the original bytes land
				node.commitExternal()
				_, _ = deposit(scenarioCtx(t), second, 1000)
				hash, err := deposit(scenarioCtx(t), second, 1000)
				if err != nil || hash != refused.Hash().String() {
					t.Fatalf("expected the original hash once the gap closed, got hash=%q err=%v", hash, err)
				}
				assertExecutedOnce(t, node, refused)
			}
		})
	}

	t.Run("not admitted/rejected then funded after restart", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		node.script = scriptAll(fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: "insufficient funds for gas * price + value"})
		store.inject("resolve", memFaultFail)
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("expected a plain error, got hash=%q err=%v", hash, err)
		}
		refused := node.sentTx(t, 0)
		second := restartClient(t, node, store)
		if _, err := deposit(scenarioCtx(t), second, 1000); err == nil {
			t.Fatal("an unfunded transaction must keep the scope blocked")
		}
		node.script = nil // funded
		_, _ = deposit(scenarioCtx(t), second, 1000)
		hash, err := deposit(scenarioCtx(t), second, 1000)
		if err != nil || hash != refused.Hash().String() {
			t.Fatalf("expected the original hash after funding, got hash=%q err=%v", hash, err)
		}
		assertExecutedOnce(t, node, refused)
	})
	t.Run("not admitted/rejected, recorded on retry", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		node.script = scriptFirst(fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: "insufficient funds for gas * price + value"})
		store.inject("resolve", memFaultFail)
		if _, err := deposit(scenarioCtx(t), c, 1000); err == nil {
			t.Fatal("expected an error")
		}
		if _, err := deposit(scenarioCtx(t), c, 1000); err != nil {
			t.Fatalf("the refusal is recorded and the operation signed again: %v", err)
		}
		if len(store.auditEvents(evmAuditNotAdmittedRejected)) != 1 {
			t.Fatal("expected one not_admitted_rejected audit")
		}
		// identical bytes re-signed after the refusal reuse the evidence row
		accepted, sends, _ := node.snapshot()
		if len(accepted) != 1 || sends[0].hash != sends[1].hash {
			t.Fatalf("expected the same bytes re-signed and accepted once; %s", node.describe())
		}
	})
}

// T11e: a conditional delete finds the row gone; an audit insert fails.
func TestEvmStateZeroRowsAndAuditFailure(t *testing.T) {
	t.Run("row removed concurrently", func(t *testing.T) {
		node, c := newScenario(t)
		ambiguousDeposit(t, node, c)
		node.setHideReceipts(false)
		store := memStoreOf(t, c)
		store.hookOnce("resolve", func(m *memEvmStateStore) {
			for id := range m.pending {
				delete(m.pending, id)
			}
		})
		if _, err := deposit(scenarioCtx(t), c, 2000); err != nil {
			t.Fatalf("a vanished row is decided again from the DB: %v", err)
		}
		if len(store.outcomeRows()) != 0 {
			t.Fatal("no outcome may be written for a row someone else removed")
		}
		if len(store.auditEvents(evmAuditPendingVanished)) != 1 {
			t.Fatal("expected a pending_vanished audit")
		}
	})
	t.Run("audit insert fails", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.inject("audit", memFaultFail)
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("expected no hash and an error, got hash=%q err=%v", hash, err)
		}
		if _, sends, _ := node.snapshot(); len(sends) != 0 || len(store.pendingRows()) != 0 {
			t.Fatalf("the whole Record must roll back; %s", node.describe())
		}
		if _, ok := store.signedRow(1); ok {
			t.Fatal("the evidence row must roll back with the pending row")
		}
	})
}

// T12: reading the state fails.
func TestEvmStateReadFailureBlocks(t *testing.T) {
	for _, op := range []string{"get_outcome", "get_pending", "get_signed_tx"} {
		t.Run(op, func(t *testing.T) {
			node, c := newScenario(t)
			if op == "get_signed_tx" {
				ambiguousDeposit(t, node, c)
			}
			_, before, _ := node.snapshot()
			memStoreOf(t, c).inject(op, memFaultFail)
			if _, err := deposit(scenarioCtx(t), c, 2000); err == nil {
				t.Fatal("a read failure must block")
			}
			if _, after, _ := node.snapshot(); len(after) != len(before) {
				t.Fatalf("nothing may be sent; %s", node.describe())
			}
		})
	}
}

// T13: two clients share one store; only the lease holder writes.
func TestEvmStateSecondSignerIsFencedUntilLeaseMoves(t *testing.T) {
	node, a := newScenario(t)
	store := memStoreOf(t, a)
	b := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
	if err := b.checkEvmStateOnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.evm.lease.tick(context.Background())
	if _, ok := b.evm.lease.fence(); ok {
		t.Fatal("the second signer must not get the lease")
	}
	if _, err := deposit(scenarioCtx(t), b, 1000); err == nil {
		t.Fatal("the second signer must refuse")
	}
	if _, sends, _ := node.snapshot(); len(sends) != 0 {
		t.Fatalf("the second signer sent; %s", node.describe())
	}
	// even a direct write with a forged fence is rejected
	if _, err := store.Record(context.Background(), evmFence{ChainID: fakeEvmChainID, Owner: b.evm.lease.owner, Epoch: 1},
		signedTxEvidence{}, pendingRecord{}); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("expected a fencing failure, got %v", err)
	}

	tx := ambiguousDeposit(t, node, a)
	node.setHideReceipts(false)
	a.backgroundResolveEvm(context.Background(), SignOperator)
	a.evm.lease.stop(context.Background())

	b.evm.lease.tick(context.Background())
	if _, ok := b.evm.lease.fence(); !ok {
		t.Fatal("the second signer must take over the released lease")
	}
	hash, err := deposit(scenarioCtx(t), b, 1000)
	if err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash on the new holder, got hash=%q err=%v", hash, err)
	}
	assertExecutedOnce(t, node, tx)
}

// T13b: the lease expires after Record and before the send; another signer
// takes over and the old one can no longer write.
func TestEvmStateLeaseLostBetweenRecordAndSend(t *testing.T) {
	node, a := newScenario(t)
	store := memStoreOf(t, a)
	b := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
	first := true
	withSendFn(t, func(ctx context.Context, evmClient *ethclient.Client, tx *ethtypes.Transaction) error {
		if first {
			first = false
			store.expireLease(fakeEvmChainID)
			b.evm.lease.tick(context.Background()) // b loads the row and re-sends it
		}
		return evmClient.SendTransaction(ctx, tx)
	})

	hash, err := deposit(scenarioCtx(t), a, 1000)
	if err == nil {
		t.Fatal("the old holder must not record the result")
	}
	tx := node.sentTx(t, 0)
	if hash != tx.Hash().String() {
		t.Fatalf("expected the recorded hash, got %q", hash)
	}
	assertSignedOnce(t, node)
	if _, err := deposit(scenarioCtx(t), a, 2000); err == nil {
		t.Fatal("every later write of the old holder must be fenced")
	}
	if hash, err = deposit(scenarioCtx(t), b, 1000); err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash on the new holder, got hash=%q err=%v", hash, err)
	}
	assertExecutedOnce(t, node, tx)
}

// T13c: a refusal whose write failed under one lease epoch is not evidence
// under the next one: another holder may have re-sent the recorded bytes
// in between. Neither a request nor the background resolver may act on it.
func TestEvmStateRefusalDoesNotSurviveLeaseEpoch(t *testing.T) {
	// "recovery": the epoch's recovery resolves the row before the request.
	// "request first": the request comes before recovery has run at all.
	for _, entry := range []string{"recovery", "request first"} {
		t.Run(entry, func(t *testing.T) {
			node, a := newScenario(t)
			store := memStoreOf(t, a)
			node.script = scriptFirst(fakeEvmAction{kind: fakeEvmRPCError, code: -32000,
				message: "insufficient funds for gas * price + value"})
			store.inject("resolve", memFaultFail)
			if _, err := deposit(scenarioCtx(t), a, 1000); err == nil {
				t.Fatal("expected the refusal write to fail")
			}
			tx := node.sentTx(t, 0)

			oldFence, _ := a.evm.lease.fence()
			a.evm.lease.lose(context.Background(), oldFence, "test lease handoff")
			node.setHideReceipts(true)
			// b loads the row and re-sends it; the node now admits it
			_ = restartClient(t, node, store)
			if n := node.executionsOf(tx); n != 1 {
				t.Fatalf("expected the re-send to execute once, got %d; %s", n, node.describe())
			}

			store.expireLease(fakeEvmChainID)
			node.setHideReceipts(false)
			if entry == "request first" {
				a.evm.spawn = func(func()) {}
			}
			acquireEvmLeaseForTest(t, a)
			if entry == "recovery" {
				if _, ok := store.pendingOf(SignOperator); ok {
					t.Fatal("recovery must end the row from its receipt")
				}
				if len(store.outcomeRows()) != 1 {
					t.Fatal("recovery must keep the success for its operation")
				}
			}
			hash, err := deposit(scenarioCtx(t), a, 1000)
			if err != nil || hash != tx.Hash().String() {
				t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
			}
			assertExecutedOnce(t, node, tx)
			if n := len(store.auditEvents(evmAuditNotAdmittedRejected)); n != 0 {
				t.Fatalf("a stale refusal was recorded %d times", n)
			}
		})
	}
}

// T13d: a scope whose nonce cannot be reset for a new epoch signs nothing
// new, but still answers from its recorded state.
func TestEvmStateNewEpochNonceSyncFailureFailsClosed(t *testing.T) {
	node, a := newScenario(t)
	store := memStoreOf(t, a)
	tx := ambiguousDeposit(t, node, a)
	fence, _ := a.evm.lease.fence()
	a.evm.lease.lose(context.Background(), fence, "test lease handoff")
	store.expireLease(fakeEvmChainID)
	a.evm.spawn = func(func()) {}
	acquireEvmLeaseForTest(t, a)
	original := getCosmosNonceFn
	t.Cleanup(func() { getCosmosNonceFn = original })
	getCosmosNonceFn = func(*client.MocaClient, context.Context) (uint64, error) { return 0, errors.New("nonce query down") }

	if hash, err := deposit(scenarioCtx(t), a, 1000); err == nil || hash != tx.Hash().String() {
		t.Fatalf("expected the pending hash with its error, got hash=%q err=%v", hash, err)
	}
	node.setHideReceipts(false)
	a.backgroundResolveEvm(context.Background(), SignOperator)
	_, sendsBefore, _ := node.snapshot()
	if _, err := deposit(scenarioCtx(t), a, 2000); err == nil || !strings.Contains(err.Error(), errEvmNonceUnsynced.Error()) {
		t.Fatalf("expected the unsynced nonce to block signing, got %v", err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != len(sendsBefore) {
		t.Fatalf("nothing may be sent with an unsynced nonce; %s", node.describe())
	}
	getCosmosNonceFn = original
	if _, err := deposit(scenarioCtx(t), a, 2000); err != nil {
		t.Fatalf("signing resumes once the nonce syncs: %v", err)
	}
}

// T14: what restart does with rows it cannot simply resume.
func TestEvmStateLoadChecks(t *testing.T) {
	t.Run("key changed", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		oldKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		to := ethcmn.HexToAddress("0x00000000000000000000000000000000000000aa")
		oldTx, err := ethtypes.SignNewTx(oldKey, ethtypes.LatestSignerForChainID(big.NewInt(5151)),
			&ethtypes.LegacyTx{Nonce: 50, GasPrice: big.NewInt(1), Gas: 21000, To: &to, Value: big.NewInt(1)})
		if err != nil {
			t.Fatal(err)
		}
		seedPending(t, c, SignOperator, ethcmn.HexToHash("0x01"), oldTx)

		second := restartClient(t, node, store)
		if second.operatorAccNonce != scenarioNonce {
			t.Fatalf("the old key's pending nonce must not bound the new key: cache=%d", second.operatorAccNonce)
		}
		if _, err := deposit(scenarioCtx(t), second, 1000); err == nil {
			t.Fatal("the old key's pending row must keep the scope blocked")
		}
		loaded := store.auditEvents(evmAuditLoadedOnStart)
		if len(loaded) == 0 || !strings.Contains(loaded[len(loaded)-1].Detail, "differs from the current") {
			t.Fatalf("expected the key mismatch in the loaded_on_start audit, got %+v", loaded)
		}
	})
	t.Run("raw corrupted", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		pending := seedPending(t, c, SignOperator, ethcmn.HexToHash("0x01"), signFakeTx(t, scenarioNonce, 1))
		store.tamper(pending.SignedTxID, func(e *signedTxEvidence) { e.Raw[len(e.Raw)-1] ^= 0xff })
		restarted := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
		if err := restarted.checkEvmStateOnStart(context.Background()); err == nil {
			t.Fatal("a corrupt pending row must stop the signer")
		}
		if len(store.auditEvents(evmAuditEvidenceInvalid)) != 1 {
			t.Fatal("expected an evidence_invalid audit")
		}
	})
	t.Run("other EVM chain", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		key, _ := crypto.HexToECDSA(fakeEvmPrivateKey)
		to := ethcmn.HexToAddress("0x00000000000000000000000000000000000000aa")
		foreignTx, err := ethtypes.SignNewTx(key, ethtypes.LatestSignerForChainID(big.NewInt(9999)),
			&ethtypes.LegacyTx{Nonce: scenarioNonce, GasPrice: big.NewInt(1), Gas: 21000, To: &to, Value: big.NewInt(1)})
		if err != nil {
			t.Fatal(err)
		}
		seedPending(t, c, SignOperator, ethcmn.HexToHash("0x01"), foreignTx)
		restarted := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
		if err := restarted.checkEvmStateOnStart(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "evm_chain_id=9999") {
			t.Fatalf("a pending row of another EVM chain must stop the signer, got %v", err)
		}
	})
	t.Run("other cosmos chain", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		store.putOutcome(outcomeRecord{ChainID: "moca_1-1", Scope: SignOperator, State: evmOutcomeSucceeded})
		restarted := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
		if err := restarted.checkEvmStateOnStart(context.Background()); err == nil {
			t.Fatal("rows of another chain id must stop the signer")
		}
	})
	t.Run("other cosmos chain in evidence or audit", func(t *testing.T) {
		for name, seed := range map[string]func(m *memEvmStateStore){
			"evidence": func(m *memEvmStateStore) { m.signed[999] = signedTxEvidence{ID: 999, ChainID: "moca_1-1"} },
			"audit":    func(m *memEvmStateStore) { m.audits = append(m.audits, memAudit{chainID: "moca_1-1"}) },
		} {
			node, c := newScenario(t)
			store := memStoreOf(t, c)
			store.mu.Lock()
			seed(store)
			store.mu.Unlock()
			restarted := newFakeEvmSignClientNoLease(t, node, scenarioNonce, store)
			if err := restarted.checkEvmStateOnStart(context.Background()); err == nil {
				t.Fatalf("%s rows of another chain id must stop the signer", name)
			}
		}
	})
}

// consumeOnce runs an operation through "background resolve keeps the result,
// the business retry takes it" and returns the transaction.
func consumeOnce(t *testing.T, node *fakeEvmNode, c *MocaChainSignClient, sends int) *ethtypes.Transaction {
	t.Helper()
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == sends+1 {
			return fakeEvmAction{kind: fakeEvmAdmitThenDrop}
		}
		return fakeEvmAction{}
	}
	node.setHideReceipts(true)
	if _, err := deposit(scenarioCtx(t), c, 1000); err == nil {
		t.Fatal("expected an unconfirmed error")
	}
	tx := node.sentTx(t, sends)
	node.setHideReceipts(false)
	c.backgroundResolveEvm(context.Background(), SignOperator)
	hash, err := deposit(scenarioCtx(t), c, 1000)
	if err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected hash %s, got hash=%q err=%v", tx.Hash().Hex(), hash, err)
	}
	return tx
}

// T17: two legitimate operations with the same fingerprint, one after the
// other, each through "kept by background resolve, consumed by its retry".
// The dispose-outcome variant needs the PR-2 CLI and runs there.
func TestEvmStateOutcomeLifecycleAllowsIdenticalOperationsInSequence(t *testing.T) {
	node, c := newScenario(t)
	store := memStoreOf(t, c)
	first := consumeOnce(t, node, c, 0)
	_, sends, _ := node.snapshot()
	second := consumeOnce(t, node, c, len(sends))
	if first.Hash() == second.Hash() || second.Nonce() != first.Nonce()+1 {
		t.Fatalf("expected the second operation at the next nonce; %s", node.describe())
	}
	created := store.auditEvents(evmAuditOutcomeCreated)
	consumed := store.auditEvents(evmAuditOutcomeConsumed)
	if len(created) != 2 || len(consumed) != 2 || *created[0].OutcomeID == *created[1].OutcomeID ||
		created[0].TxHash != first.Hash() || created[1].TxHash != second.Hash() ||
		*consumed[0].OutcomeID != *created[0].OutcomeID || *consumed[1].OutcomeID != *created[1].OutcomeID {
		t.Fatalf("each operation needs its own outcome id and hash in the audit: created=%+v consumed=%+v", created, consumed)
	}
	if n := node.executionsOf(first); n != 2 {
		t.Fatalf("the two operations must have executed once each; %s", node.describe())
	}
}

// T17b: invariant I1 holds even when a caller skips step 3 or rows were
// inserted by hand.
func TestEvmStateOneActiveRowPerOperation(t *testing.T) {
	for _, state := range []evmOutcomeState{evmOutcomeSucceeded, evmOutcomeBlockedUnknown} {
		t.Run("record with active "+string(state), func(t *testing.T) {
			_, c := newScenario(t)
			store := memStoreOf(t, c)
			operation := ethcmn.HexToHash("0x01")
			store.putOutcome(outcomeRecord{ChainID: fakeEvmChainID, Scope: SignOperator, Operation: operation, State: state})
			tx := signFakeTx(t, scenarioNonce, 1)
			raw, _ := tx.MarshalBinary()
			fence, _ := c.evm.lease.fence()
			_, err := c.recordPendingEvmTx(context.Background(), c.evm, fence, SignOperator, operation, tx, raw)
			if !errors.Is(err, errEvmStateConflict) {
				t.Fatalf("expected a state conflict, got %v", err)
			}
			if len(store.pendingRows()) != 0 {
				t.Fatal("Record must roll back")
			}
		})
	}
	t.Run("keep onto an existing row", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		tx := ambiguousDeposit(t, node, c)
		node.setHideReceipts(false)
		row, _ := store.pendingOf(SignOperator)
		store.putOutcome(outcomeRecord{ChainID: fakeEvmChainID, Scope: SignOperator, Operation: row.Operation,
			State: evmOutcomeBlockedUnknown})

		if hash, err := deposit(scenarioCtx(t), c, 2000); err == nil || hash != "" {
			t.Fatalf("a conflicting keep must block, got hash=%q err=%v", hash, err)
		}
		if _, ok := store.pendingOf(SignOperator); !ok {
			t.Fatal("the pending row must survive the rolled back keep")
		}
		if outcomes := store.outcomeRows(); len(outcomes) != 1 || outcomes[0].State != evmOutcomeBlockedUnknown {
			t.Fatalf("the existing row must not be overwritten: %+v", outcomes)
		}
		mem := c.evm.memory(SignOperator)
		if mem.terminalRetry == nil || mem.terminalRetry.pending.TxHash != tx.Hash() {
			t.Fatal("the success evidence must be kept for retry")
		}
		if _, err := deposit(scenarioCtx(t), c, 2000); err == nil {
			t.Fatal("the scope stays blocked while the conflict lasts")
		}
		// The retry for the row's own operation must not slip past the
		// conflict by consuming instead of keeping.
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("the own operation stays blocked by the conflict, got hash=%q err=%v", hash, err)
		}
		if _, ok := store.pendingOf(SignOperator); !ok {
			t.Fatal("the pending row must survive")
		}
		if outcomes := store.outcomeRows(); len(outcomes) != 1 || outcomes[0].State != evmOutcomeBlockedUnknown {
			t.Fatalf("the blocking row must stay: %+v", outcomes)
		}
	})
	t.Run("kept retry lands with an unknown commit", func(t *testing.T) {
		node, c := newScenario(t)
		store := memStoreOf(t, c)
		tx := ambiguousDeposit(t, node, c)
		node.setHideReceipts(false)
		store.inject("resolve", memFaultFail)
		if _, err := deposit(scenarioCtx(t), c, 2000); err == nil {
			t.Fatal("the failed keep must block the other operation")
		}
		// The own operation's retry commits, but the commit is reported
		// unknown and the read back fails.
		store.inject("resolve", memFaultCommitUnknownApplied)
		store.inject("get_pending", memFaultFail)
		if hash, err := deposit(scenarioCtx(t), c, 1000); err == nil || hash != "" {
			t.Fatalf("expected an error while the commit is unknown, got hash=%q err=%v", hash, err)
		}
		hash, err := deposit(scenarioCtx(t), c, 1000)
		if err != nil || hash != tx.Hash().String() {
			t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
		}
		if accepted, _, _ := node.snapshot(); len(accepted) != 1 {
			t.Fatalf("the operation must not be signed again; %s", node.describe())
		}
		assertExecutedOnce(t, node, tx)
	})
}

// I2: the evidence row cannot be removed while a pending or outcome row
// refers to it, and it stays after both are gone.
func TestEvmStateEvidenceOutlivesItsRows(t *testing.T) {
	node, c := newScenario(t)
	store := memStoreOf(t, c)
	tx := ambiguousDeposit(t, node, c)
	pending, _ := store.pendingOf(SignOperator)
	if err := store.deleteSignedTx(pending.SignedTxID); err == nil {
		t.Fatal("evidence referenced by a pending row must not be deletable")
	}

	node.setHideReceipts(false)
	if _, err := deposit(scenarioCtx(t), c, 2000); err != nil {
		t.Fatal(err)
	}
	outcomes := store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].SignedTxID != pending.SignedTxID {
		t.Fatalf("the outcome must take over the pending row's evidence: %+v", outcomes)
	}
	if err := store.deleteSignedTx(pending.SignedTxID); err == nil {
		t.Fatal("evidence referenced by an outcome row must not be deletable")
	}

	if hash, err := deposit(scenarioCtx(t), c, 1000); err != nil || hash != tx.Hash().String() {
		t.Fatalf("expected the original hash, got hash=%q err=%v", hash, err)
	}
	if evidence, ok := store.signedRow(pending.SignedTxID); !ok || evidence.TxHash != tx.Hash() {
		t.Fatal("the evidence row must stay after its outcome is consumed")
	}
}

// T19c, signer part: an outcome whose evidence fails V(e) blocks its
// operation after restart, even when succeeded; nothing is consumed.
func TestEvmStateInvalidOutcomeEvidenceBlocks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(store *memEvmStateStore, outcome outcomeRecord)
	}{
		{"raw byte changed", func(store *memEvmStateStore, o outcomeRecord) {
			store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.Raw[len(e.Raw)-1] ^= 0x01 })
		}},
		{"sender differs", func(store *memEvmStateStore, o outcomeRecord) {
			store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.From = ethcmn.HexToAddress("0x01") })
		}},
		{"evm chain id differs", func(store *memEvmStateStore, o outcomeRecord) {
			store.tamper(o.SignedTxID, func(e *signedTxEvidence) { e.EvmChainID = 1 })
		}},
		{"outcome nonce differs", func(store *memEvmStateStore, o outcomeRecord) {
			store.tamperOutcome(o.ID, func(r *outcomeRecord) { r.Nonce++ })
		}},
		{"evidence row missing", func(store *memEvmStateStore, o outcomeRecord) {
			store.tamperOutcome(o.ID, func(r *outcomeRecord) { r.SignedTxID = 999 })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, store, _, _ := confirmedBeforeRestart(t)
			outcome := store.outcomeRows()[0]
			tc.break_(store, outcome)
			restarted := restartClient(t, node, store)
			_, before, _ := node.snapshot()
			if _, err := deposit(scenarioCtx(t), restarted, 1000); !errors.Is(err, ErrDepositOnChain) || err == nil {
				t.Fatalf("expected the operation to stay blocked, got %v", err)
			}
			if rows := store.outcomeRows(); len(rows) != 1 {
				t.Fatal("an outcome with invalid evidence must not be consumed")
			}
			if _, after, _ := node.snapshot(); len(after) != len(before) {
				t.Fatalf("nothing may be signed; %s", node.describe())
			}
			if len(store.auditEvents(evmAuditEvidenceInvalid)) != 1 {
				t.Fatalf("expected exactly one evidence_invalid audit, got %d", len(store.auditEvents(evmAuditEvidenceInvalid)))
			}
		})
	}
	t.Run("evidence read fails", func(t *testing.T) {
		node, store, _, second := confirmedBeforeRestart(t)
		store.inject("get_signed_tx", memFaultFail)
		if _, err := deposit(scenarioCtx(t), second, 1000); err == nil {
			t.Fatal("a failed evidence read must block")
		}
		if len(store.outcomeRows()) != 1 || len(store.auditEvents(evmAuditEvidenceInvalid)) != 0 {
			t.Fatal("a storage error is not an evidence verdict: nothing consumed, nothing audited")
		}
		if hash, err := deposit(scenarioCtx(t), second, 1000); err != nil || hash == "" {
			t.Fatalf("once readable the outcome is consumed: hash=%q err=%v", hash, err)
		}
		_ = node
	})
}

// D1: without a state store, or without the lease, nothing is signed.
func TestEvmStateFailsClosed(t *testing.T) {
	node := newFakeEvmNode(t, scenarioNonce)
	installFakeEvmSeams(t, node)
	c := newFakeEvmSignClient(t, node, scenarioNonce)
	c.evm = nil
	if _, err := deposit(scenarioCtx(t), c, 1000); err == nil {
		t.Fatal("no state store must refuse")
	}
	c2 := newFakeEvmSignClientNoLease(t, node, scenarioNonce, newMemEvmStateStore())
	if _, err := deposit(scenarioCtx(t), c2, 1000); err == nil {
		t.Fatal("no lease must refuse")
	}
	if _, sends, _ := node.snapshot(); len(sends) != 0 {
		t.Fatalf("nothing may be sent; %s", node.describe())
	}
}
