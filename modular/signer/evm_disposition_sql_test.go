package signer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// M6 and M7(b)–(d) of the SOL-486 plan run the CLI's manual transitions
// against a real MySQL (see evm_state_store_sql_test.go for the server).

// cliAccount creates a user with the D2 runtime grants, which the CLI
// account shares, and returns a store connected as it.
func (env *mysqlEvmEnv) cliAccount(t *testing.T) *sqlEvmStateStore {
	t.Helper()
	user := "sevm_cli_" + env.database[len(env.database)-6:]
	env.exec(t, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY 'clipass'", user))
	t.Cleanup(func() { _, _ = env.root.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user)) })
	for _, grant := range signerEvmRuntimeGrants(env.database, user) {
		env.exec(t, grant)
	}
	return newSQLEvmStateStore(env.open(t, user, "clipass").DB())
}

// recordPending records tx as the operator scope's pending row.
func (env *mysqlEvmEnv) recordPending(t *testing.T, tx *ethtypes.Transaction, operation ethcmn.Hash) pendingRecord {
	t.Helper()
	fence := env.lease(t, env.sql, "signer", time.Hour)
	evidence, pending := recordArgs(t, tx, SignOperator, operation)
	recorded, err := env.sql.Record(context.Background(), fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	return recorded
}

// mysqlDisposeEnv runs the CLI decision on MySQL against a fake chain.
func mysqlDisposeRun(t *testing.T, store evmStateStore, node *fakeEvmNode, chain *fakeDisposeChain, req EvmDisposeRequest) *EvmDisposeReport {
	t.Helper()
	req.ChainID, req.Operator, req.Reason = fakeEvmChainID, "ops-test", "mysql"
	req.BlockWait, req.Poll = time.Second, time.Millisecond
	_ = node
	return disposeEvmState(context.Background(), store, req, chain, fakeCosmosChain{chain})
}

// verifyOutcomeEvidence reads the outcome on a new connection and checks its
// evidence row exists and passes V(e).
func (env *mysqlEvmEnv) verifyOutcomeEvidence(t *testing.T, id uint64, state evmOutcomeState, reason string) {
	t.Helper()
	fresh := newSQLEvmStateStore(env.open(t, env.user, env.password).DB())
	ctx := context.Background()
	outcome, err := fresh.GetOutcomeByID(ctx, fakeEvmChainID, id)
	if err != nil || outcome == nil || outcome.State != state || outcome.Reason != reason {
		t.Fatalf("outcome %d on a new connection: %+v %v", id, outcome, err)
	}
	evidence, err := fresh.GetSignedTx(ctx, outcome.SignedTxID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifySignedTxEvidence(evidence, outcome.ref()); err != nil {
		t.Fatalf("the outcome's evidence must pass V(e): %v", err)
	}
}

// M7(b): the --accept-unknown and --accept-gap transitions, run as the CLI
// account, commit an outcome whose evidence a new connection reads and
// verifies. resolve-outcome and release work with the same grants.
func TestSignerEvmMySQLManualTransitions(t *testing.T) {
	t.Run("accept-unknown, resolve-outcome", func(t *testing.T) {
		env := newMySQLEvmEnv(t, true)
		cli := env.cliAccount(t)
		node := newFakeEvmNode(t, 7)
		chain := newFakeDisposeChain(t, node)
		original := signTx(t, 7, ethcmn.HexToAddress("0x2000"), 0, []byte{0x01}, 1_000_000_000)
		pending := env.recordPending(t, original, ethcmn.HexToHash("0x0a"))
		occupier := signTx(t, 7, plainTransferTo, 1, nil, 1_000_000_000)
		chain.addEvmTx(occupier, ethtypes.ReceiptStatusSuccessful)
		chain.hide(occupier.Hash().Hex(), true)

		report := mysqlDisposeRun(t, cli, node, chain, EvmDisposeRequest{Command: EvmDisposeResolve, Scope: "operator",
			TxHash: pending.TxHash, ScanBlocks: 100, AcceptUnknown: true})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchOccupierMissing)
		env.verifyOutcomeEvidence(t, pending.ID, evmOutcomeBlockedUnknown, evmReasonReceiptMissing)
		if env.count(t, "signer_evm_state", "state = 'pending'") != 0 ||
			env.count(t, "signer_evm_audit", "event = ? AND outcome_id = ? AND actor LIKE 'ops-test (os user %'", evmAuditManualResolveUnknown, pending.ID) != 1 {
			t.Fatal("the transition must delete the pending state and audit with the operator")
		}

		chain.hide(occupier.Hash().Hex(), false)
		report = mysqlDisposeRun(t, cli, node, chain, EvmDisposeRequest{Command: EvmDisposeResolveOutcome, OutcomeID: pending.ID,
			ScanBlocks: 100})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchPlainTransfer)
		if env.count(t, "signer_evm_state", "") != 0 || env.count(t, "signer_evm_signed_tx", "id = ?", pending.SignedTxID) != 1 {
			t.Fatal("resolve-outcome must delete the outcome and keep the evidence")
		}
	})
	t.Run("accept-gap, release", func(t *testing.T) {
		env := newMySQLEvmEnv(t, true)
		cli := env.cliAccount(t)
		node := newFakeEvmNode(t, 7)
		chain := newFakeDisposeChain(t, node)
		original := signTx(t, 9, ethcmn.HexToAddress("0x2000"), 0, []byte{0x02}, 1_000_000_000)
		pending := env.recordPending(t, original, ethcmn.HexToHash("0x0b"))

		report := mysqlDisposeRun(t, cli, node, chain, EvmDisposeRequest{Command: EvmDisposeResolve, Scope: "operator",
			TxHash: pending.TxHash, AcceptGap: true})
		expectExit(t, report, EvmDisposeExitOK, EvmBranchNonceGap)
		env.verifyOutcomeEvidence(t, pending.ID, evmOutcomeBlockedUnknown, evmReasonNonceGap)

		report = mysqlDisposeRun(t, cli, node, chain, EvmDisposeRequest{Command: EvmDisposeRelease, OutcomeID: pending.ID,
			DecisionRef: "https://board.example/d/7", BusinessEvidence: "no effect"})
		expectExit(t, report, EvmDisposeExitOK, "")
		if env.count(t, "signer_evm_state", "") != 0 ||
			env.count(t, "signer_evm_audit", "event = ? AND decision_ref = 'https://board.example/d/7'", evmAuditManualReleaseUnknown) != 1 {
			t.Fatal("release must delete the outcome and audit the decision")
		}
		// a second release is refused: the row is gone
		report = mysqlDisposeRun(t, cli, node, chain, EvmDisposeRequest{Command: EvmDisposeRelease, OutcomeID: pending.ID,
			DecisionRef: "https://board.example/d/7", BusinessEvidence: "no effect"})
		expectExit(t, report, EvmDisposeExitPrecondition, "")
	})
}

// M7(c): when the outcome cannot be created (an active outcome of the same
// operation exists) or the audit insert fails, the pending row and its
// evidence stay exactly as they were.
func TestSignerEvmMySQLManualTransitionRollsBack(t *testing.T) {
	ctx := context.Background()
	operation := ethcmn.HexToHash("0x0c")
	blocked := &outcomeRecord{State: evmOutcomeBlockedUnknown, Reason: evmReasonReceiptMissing, Source: evmOutcomeSourceManual}
	t.Run("outcome conflict", func(t *testing.T) {
		env := newMySQLEvmEnv(t, true)
		cli := env.cliAccount(t)
		pending := env.recordPending(t, signTx(t, 7, plainTransferTo, 1, nil, 1), operation)
		other := signTx(t, 6, plainTransferTo, 1, nil, 1)
		env.exec(t, `INSERT INTO signer_evm_state (signed_tx_id, chain_id, scope, operation, from_address, nonce, tx_hash, state, source, created_at)
VALUES (?, ?, 'operator', ?, ?, 6, ?, 'succeeded', 'manual', 0)`, pending.SignedTxID, fakeEvmChainID, operation.Hex(), pending.From.Hex(), other.Hash().Hex())
		err := cli.ManualResolvePending(ctx, pending, blocked, pending.audit(evmAuditManualResolveUnknown))
		if !errors.Is(err, errEvmStateConflict) {
			t.Fatalf("expected a conflict, got %v", err)
		}
		if env.count(t, "signer_evm_state", "id = ? AND state = 'pending'", pending.ID) != 1 ||
			env.count(t, "signer_evm_signed_tx", "id = ?", pending.SignedTxID) != 1 ||
			env.count(t, "signer_evm_audit", "event = ?", evmAuditManualResolveUnknown) != 0 {
			t.Fatal("the pending row and its evidence must stay, without audit")
		}
	})
	t.Run("audit failure", func(t *testing.T) {
		env := newMySQLEvmEnv(t, true)
		cli := env.cliAccount(t)
		pending := env.recordPending(t, signTx(t, 7, plainTransferTo, 1, nil, 1), operation)
		env.failAuditTrigger(t, evmAuditManualResolveUnknown, pending.TxHash)
		if err := cli.ManualResolvePending(ctx, pending, blocked, pending.audit(evmAuditManualResolveUnknown)); err == nil {
			t.Fatal("the audit failure must fail the transition")
		}
		if env.count(t, "signer_evm_state", "id = ? AND state = 'pending'", pending.ID) != 1 ||
			env.count(t, "signer_evm_signed_tx", "id = ?", pending.SignedTxID) != 1 {
			t.Fatal("the pending row and its evidence must stay")
		}
	})
}

// M7(d): the transition's connection is killed mid-transaction, while it
// waits for the evidence lock and while it writes the audit row. Read back,
// the state is pending+evidence or outcome+evidence, never in between.
func TestSignerEvmMySQLManualTransitionKilled(t *testing.T) {
	for _, phase := range []string{"evidence lock", "audit insert"} {
		t.Run(phase, func(t *testing.T) {
			env := newMySQLEvmEnv(t, true)
			cli := env.cliAccount(t)
			ctx := context.Background()
			pending := env.recordPending(t, signTx(t, 7, plainTransferTo, 1, nil, 1), ethcmn.HexToHash("0x0d"))
			marker := "FOR SHARE"
			var unlock func()
			if phase == "evidence lock" {
				holder, err := env.root.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := holder.Exec("SELECT id FROM signer_evm_signed_tx WHERE id = ? FOR UPDATE", pending.SignedTxID); err != nil {
					t.Fatal(err)
				}
				unlock = func() { _ = holder.Rollback() }
			} else {
				marker = "SLEEP(30)"
				env.exec(t, "DROP TRIGGER IF EXISTS signer_evm_audit_sleep")
				env.exec(t, fmt.Sprintf(`CREATE TRIGGER signer_evm_audit_sleep BEFORE INSERT ON signer_evm_audit FOR EACH ROW
BEGIN
  IF NEW.event = '%s' THEN
    DO SLEEP(30);
  END IF;
END`, evmAuditManualResolveUnknown))
				unlock = func() { _, _ = env.root.Exec("DROP TRIGGER IF EXISTS signer_evm_audit_sleep") }
			}
			defer unlock()

			var wg sync.WaitGroup
			var transitionErr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				transitionErr = cli.ManualResolvePending(ctx, pending,
					&outcomeRecord{State: evmOutcomeBlockedUnknown, Reason: evmReasonReceiptMissing, Source: evmOutcomeSourceManual},
					pending.audit(evmAuditManualResolveUnknown))
			}()
			killed := false
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !killed; time.Sleep(20 * time.Millisecond) {
				var ids []int64
				rows, err := env.root.Query(`SELECT ID FROM information_schema.PROCESSLIST WHERE DB = ? AND ID <> CONNECTION_ID() AND INFO LIKE ?`,
					env.database, "%"+marker+"%")
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					var id int64
					if err := rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
				_ = rows.Close()
				for _, id := range ids {
					if _, err := env.root.Exec(fmt.Sprintf("KILL %d", id)); err == nil {
						killed = true
					}
				}
			}
			if !killed {
				t.Fatal("the transition never reached the phase")
			}
			wg.Wait()
			if transitionErr == nil {
				t.Fatal("a killed transition must report an error")
			}
			unlock()

			pendingRows := env.count(t, "signer_evm_state", "id = ? AND state = 'pending'", pending.ID)
			outcomeRows := env.count(t, "signer_evm_state", "id = ? AND state = 'blocked_unknown'", pending.ID)
			audits := env.count(t, "signer_evm_audit", "event = ?", evmAuditManualResolveUnknown)
			evidence := env.count(t, "signer_evm_signed_tx", "id = ?", pending.SignedTxID)
			whole := (pendingRows == 1 && outcomeRows == 0 && audits == 0) || (pendingRows == 0 && outcomeRows == 1 && audits == 1)
			if !whole || evidence != 1 {
				t.Fatalf("partial state: pending=%d outcome=%d audit=%d evidence=%d", pendingRows, outcomeRows, audits, evidence)
			}
		})
	}
}

// M6: two connections settle the same outcome at once (release twice on a
// blocked_unknown row; the signer's ConsumeOutcome and dispose-outcome on a
// succeeded one). Exactly one commits; the other finds the row gone.
func TestSignerEvmMySQLConcurrentSettle(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		state evmOutcomeState
		a, b  func(env *mysqlEvmEnv, a, b *sqlEvmStateStore, fence evmFence, o outcomeRecord) error
		event string
	}{
		{"release and release", evmOutcomeBlockedUnknown,
			func(_ *mysqlEvmEnv, a, _ *sqlEvmStateStore, _ evmFence, o outcomeRecord) error {
				return a.ManualSettleOutcome(ctx, o, []evmOutcomeState{evmOutcomeBlockedUnknown}, nil, o.audit(evmAuditManualReleaseUnknown))
			},
			func(_ *mysqlEvmEnv, _, b *sqlEvmStateStore, _ evmFence, o outcomeRecord) error {
				return b.ManualSettleOutcome(ctx, o, []evmOutcomeState{evmOutcomeBlockedUnknown}, nil, o.audit(evmAuditManualReleaseUnknown))
			}, evmAuditManualReleaseUnknown},
		{"consume and dispose", evmOutcomeSucceeded,
			func(_ *mysqlEvmEnv, a, _ *sqlEvmStateStore, fence evmFence, o outcomeRecord) error {
				return a.ConsumeOutcome(ctx, fence, o)
			},
			func(_ *mysqlEvmEnv, _, b *sqlEvmStateStore, _ evmFence, o outcomeRecord) error {
				return b.ManualSettleOutcome(ctx, o, []evmOutcomeState{evmOutcomeSucceeded, evmOutcomeExecutedElsewhere}, nil,
					o.audit(evmAuditManualDisposeOutcome))
			}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newMySQLEvmEnv(t, true)
			signerStore := env.sql
			cli := env.cliAccount(t)
			pending := env.recordPending(t, signTx(t, 7, plainTransferTo, 1, nil, 1), ethcmn.HexToHash("0x0e"))
			fence := env.lease(t, signerStore, "signer", time.Hour)
			result := &outcomeRecord{State: tc.state, ResultTxHash: pending.TxHash, Source: evmOutcomeSourceManual}
			if err := cli.ManualResolvePending(ctx, pending, result, pending.audit(evmAuditManualResolveSuccess)); err != nil {
				t.Fatal(err)
			}
			outcome, err := cli.GetOutcomeByID(ctx, fakeEvmChainID, pending.ID)
			if err != nil || outcome == nil {
				t.Fatal(outcome, err)
			}

			// both start while a third connection holds the row
			holder, err := env.root.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := holder.Exec("SELECT id FROM signer_evm_state WHERE id = ? FOR UPDATE", outcome.ID); err != nil {
				t.Fatal(err)
			}
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i, fn := range []func(env *mysqlEvmEnv, a, b *sqlEvmStateStore, fence evmFence, o outcomeRecord) error{tc.a, tc.b} {
				wg.Add(1)
				go func(i int, fn func(env *mysqlEvmEnv, a, b *sqlEvmStateStore, fence evmFence, o outcomeRecord) error) {
					defer wg.Done()
					errs[i] = fn(env, signerStore, cli, fence, *outcome)
				}(i, fn)
			}
			time.Sleep(300 * time.Millisecond)
			_ = holder.Rollback()
			wg.Wait()

			succeeded := 0
			for _, err := range errs {
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, errEvmRowGone):
				default:
					t.Fatalf("the losing side must find the row gone, got %v", err)
				}
			}
			if succeeded != 1 || env.count(t, "signer_evm_state", "id = ?", outcome.ID) != 0 {
				t.Fatalf("exactly one side must commit: %v", errs)
			}
			settled := env.count(t, "signer_evm_audit", "outcome_id = ? AND event IN (?, ?, ?)", outcome.ID,
				evmAuditManualReleaseUnknown, evmAuditOutcomeConsumed, evmAuditManualDisposeOutcome)
			if settled != 1 {
				t.Fatalf("exactly one settling audit row, got %d", settled)
			}
		})
	}
}
