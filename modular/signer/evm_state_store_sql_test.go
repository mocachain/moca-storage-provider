package signer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/go-sql-driver/mysql"

	"github.com/mocachain/moca-storage-provider/modular/blocksyncer/test"
	"github.com/mocachain/moca-storage-provider/store/config"
	"github.com/mocachain/moca-storage-provider/store/sqldb"
)

// M1–M5, M7(a) and M8 of the SOL-486 plan run against a real MySQL: the
// server "make test-mysql" provides on 127.0.0.1:3306 (root/root), or the one
// SIGNER_EVM_TEST_DB_ADDRESS/_USER/_PASSWORD point at.

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

type mysqlEvmEnv struct {
	user, password, address, database string
	root                              *sql.DB
	store                             *sqldb.SignerEvmStore
	sql                               *sqlEvmStateStore
}

func newMySQLEvmEnv(t *testing.T, migrate bool) *mysqlEvmEnv {
	t.Helper()
	env := &mysqlEvmEnv{
		user:     envOr("SIGNER_EVM_TEST_DB_USER", envOr("BLOCKSYNCER_TEST_DB_USER", "root")),
		password: envOr("SIGNER_EVM_TEST_DB_PASSWORD", envOr("BLOCKSYNCER_TEST_DB_PASSWORD", "root")),
		address:  envOr("SIGNER_EVM_TEST_DB_ADDRESS", envOr("BLOCKSYNCER_TEST_DB_ADDRESS", "127.0.0.1:3306")),
	}
	env.database = test.CreateDatabase(t, env.user, env.password, env.address, "signer_evm")
	root, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", env.user, env.password, env.address, env.database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	env.root = root
	env.store = env.open(t, env.user, env.password)
	env.sql = newSQLEvmStateStore(env.store.DB())
	if migrate {
		if err := env.store.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	return env
}

func (env *mysqlEvmEnv) open(t *testing.T, user, password string) *sqldb.SignerEvmStore {
	t.Helper()
	store, err := sqldb.NewSignerEvmStore(&config.SQLDBConfig{User: user, Passwd: password, Address: env.address, Database: env.database})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func (env *mysqlEvmEnv) count(t *testing.T, table, where string, args ...interface{}) int {
	t.Helper()
	var n int
	query := "SELECT COUNT(*) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	if err := env.root.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (env *mysqlEvmEnv) exec(t *testing.T, query string, args ...interface{}) {
	t.Helper()
	if _, err := env.root.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (env *mysqlEvmEnv) lease(t *testing.T, store *sqlEvmStateStore, owner string, ttl time.Duration) evmFence {
	t.Helper()
	epoch, ok, err := store.AcquireLease(context.Background(), fakeEvmChainID, owner, ttl)
	if err != nil || !ok {
		t.Fatalf("acquire lease: ok=%t err=%v", ok, err)
	}
	return evmFence{ChainID: fakeEvmChainID, Owner: owner, Epoch: epoch}
}

// failAuditTrigger makes MySQL itself reject the next audit insert of event
// for hash, so the transition's other statements must roll back with it.
func (env *mysqlEvmEnv) failAuditTrigger(t *testing.T, event string, hash ethcmn.Hash) {
	t.Helper()
	env.exec(t, "DROP TRIGGER IF EXISTS signer_evm_audit_fail")
	env.exec(t, fmt.Sprintf(`CREATE TRIGGER signer_evm_audit_fail BEFORE INSERT ON signer_evm_audit FOR EACH ROW
BEGIN
  IF NEW.event = '%s' AND NEW.tx_hash = '%s' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected audit failure';
  END IF;
END`, event, hash.Hex()))
	t.Cleanup(func() { _, _ = env.root.Exec("DROP TRIGGER IF EXISTS signer_evm_audit_fail") })
}

func recordArgs(t *testing.T, tx *ethtypes.Transaction, scope SignType, operation ethcmn.Hash) (signedTxEvidence, pendingRecord) {
	t.Helper()
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	from, err := ethtypes.Sender(ethtypes.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		t.Fatal(err)
	}
	evidence := signedTxEvidence{ChainID: fakeEvmChainID, EvmChainID: tx.ChainId().Uint64(), Scope: scope, Operation: operation,
		From: from, Nonce: tx.Nonce(), TxHash: tx.Hash(), Raw: raw, RecordedBlock: 100}
	pending := pendingRecord{ChainID: fakeEvmChainID, Scope: scope, From: from, Nonce: tx.Nonce(), TxHash: tx.Hash(), Operation: operation}
	return evidence, pending
}

func mysqlErrNumber(err error) uint16 {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number
	}
	return 0
}

func TestSignerEvmSchemaCheck(t *testing.T) {
	env := newMySQLEvmEnv(t, false)
	ctx := context.Background()
	if err := env.store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "signer_evm_state is missing") {
		t.Fatalf("an empty database must fail the check, got %v", err)
	}
	if err := env.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.store.Migrate(ctx); err != nil {
		t.Fatalf("migrate must be idempotent: %v", err)
	}
	if err := env.store.CheckSchema(ctx); err != nil {
		t.Fatalf("a migrated database must pass: %v", err)
	}
	env.exec(t, "ALTER TABLE signer_evm_state MODIFY pending_scope VARCHAR(16) GENERATED ALWAYS AS (NULL) STORED")
	if err := env.store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "generated constraint column pending_scope") {
		t.Fatalf("invalid generated expression accepted: %v", err)
	}
	env.exec(t, "ALTER TABLE signer_evm_state MODIFY pending_scope VARCHAR(16) GENERATED ALWAYS AS (CASE WHEN state = 'pending' THEN scope ELSE NULL END) STORED")
	env.exec(t, "ALTER TABLE signer_evm_state MODIFY outcome_operation CHAR(66) GENERATED ALWAYS AS (NULL) STORED")
	if err := env.store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "generated constraint column outcome_operation") {
		t.Fatalf("invalid outcome expression accepted: %v", err)
	}
	env.exec(t, "ALTER TABLE signer_evm_state MODIFY outcome_operation CHAR(66) GENERATED ALWAYS AS (CASE WHEN state <> 'pending' THEN operation ELSE NULL END) STORED")
	env.exec(t, "ALTER TABLE signer_evm_state DROP FOREIGN KEY fk_signer_evm_state_signed_tx")
	if err := env.store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "signer_evm_state lacks the RESTRICT foreign key") {
		t.Fatalf("a missing foreign key must fail the check, got %v", err)
	}
}

// M1: Record writes evidence, pending and audit in one transaction; a failed
// audit insert rolls back the rest.
func TestSignerEvmMySQLRecordIsAtomicWithAudit(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	fence := env.lease(t, env.sql, "a", time.Minute)
	tx := signFakeTx(t, 7, 1)
	evidence, pending := recordArgs(t, tx, SignOperator, ethcmn.HexToHash("0x01"))

	env.failAuditTrigger(t, evmAuditRecorded, tx.Hash())
	if _, err := env.sql.Record(context.Background(), fence, evidence, pending); err == nil {
		t.Fatal("the audit failure must fail Record")
	}
	for _, table := range []string{"signer_evm_state", "signer_evm_signed_tx"} {
		if n := env.count(t, table, ""); n != 0 {
			t.Fatalf("%s kept %d rows after the rollback", table, n)
		}
	}
	if n := env.count(t, "signer_evm_audit", "event = ?", evmAuditRecorded); n != 0 {
		t.Fatal("audit row left behind")
	}

	env.exec(t, "DROP TRIGGER signer_evm_audit_fail")
	recorded, err := env.sql.Record(context.Background(), fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	if env.count(t, "signer_evm_state", "id = ? AND signed_tx_id = ?", recorded.ID, recorded.SignedTxID) != 1 ||
		env.count(t, "signer_evm_signed_tx", "id = ? AND tx_hash = ?", recorded.SignedTxID, tx.Hash().Hex()) != 1 ||
		env.count(t, "signer_evm_audit", "event = ? AND tx_hash = ? AND signed_tx_id = ?", evmAuditRecorded, tx.Hash().Hex(), recorded.SignedTxID) != 1 {
		t.Fatal("Record must write the evidence, the pending row and its audit together")
	}
	loaded, err := env.sql.GetSignedTx(context.Background(), recorded.SignedTxID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifySignedTxEvidence(loaded, recorded.ref()); err != nil {
		t.Fatalf("stored evidence must pass V(e): %v", err)
	}
}

// M2: two concurrent Records for one scope; the unique key lets one in.
func TestSignerEvmMySQLConcurrentRecordsConflict(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	fence := env.lease(t, env.sql, "a", time.Minute)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx := signFakeTx(t, 7, int64(i+1))
			evidence, pending := recordArgs(t, tx, SignSeal, ethcmn.BigToHash(big.NewInt(int64(i+1))))
			_, errs[i] = env.sql.Record(context.Background(), fence, evidence, pending)
		}()
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errEvmStateConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("expected one success and one conflict, got %v", errs)
	}
	if env.count(t, "signer_evm_state", "") != 1 || env.count(t, "signer_evm_signed_tx", "") != 1 {
		t.Fatal("the losing Record must leave nothing behind")
	}
}

// M3: the lease row lock serializes writers across connections, and expiry
// is judged by the DB clock.
func TestSignerEvmMySQLLeaseFencing(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	other := newSQLEvmStateStore(env.open(t, env.user, env.password).DB())
	ctx := context.Background()
	fenceA := env.lease(t, env.sql, "a", 400*time.Millisecond)
	if _, ok, err := other.AcquireLease(ctx, fakeEvmChainID, "b", time.Minute); err != nil || ok {
		t.Fatalf("b must not acquire a live lease: ok=%t err=%v", ok, err)
	}

	// a write blocks while another connection holds the lease row
	lockTx, err := env.root.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec("SELECT owner FROM signer_evm_lease WHERE chain_id = ? FOR UPDATE", fakeEvmChainID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- env.sql.AppendAudit(ctx, &fenceA, fakeEvmChainID, evmAuditEvent{Event: "probe"})
	}()
	select {
	case err := <-done:
		t.Fatalf("the fenced write did not wait for the lease row lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the fenced write must pass once the lock is released: %v", err)
	}

	time.Sleep(500 * time.Millisecond) // past expires_at by the DB clock
	if err := env.sql.AppendAudit(ctx, &fenceA, fakeEvmChainID, evmAuditEvent{Event: "probe"}); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("an expired lease must be fenced, got %v", err)
	}
	if renewed, err := env.sql.RenewLease(ctx, fenceA, time.Minute); err != nil || renewed {
		t.Fatalf("an expired lease must not renew: renewed=%t err=%v", renewed, err)
	}
	epoch, ok, err := other.AcquireLease(ctx, fakeEvmChainID, "b", time.Minute)
	if err != nil || !ok || epoch != fenceA.Epoch+1 {
		t.Fatalf("b must take the expired lease at the next epoch: epoch=%d ok=%t err=%v", epoch, ok, err)
	}
	tx := signFakeTx(t, 7, 1)
	evidence, pending := recordArgs(t, tx, SignOperator, ethcmn.HexToHash("0x01"))
	if _, err := env.sql.Record(ctx, fenceA, evidence, pending); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("the old holder must be fenced, got %v", err)
	}
	if env.count(t, "signer_evm_signed_tx", "") != 0 {
		t.Fatal("a fenced Record must leave no evidence")
	}
}

// M4: with the D2 runtime grants the signer works, and the audit and
// evidence tables cannot be updated or deleted.
func TestSignerEvmMySQLRuntimeAccountPrivileges(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	user := "sevm_rt_" + env.database[len(env.database)-6:]
	env.exec(t, fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY 'rtpass'", user))
	t.Cleanup(func() { _, _ = env.root.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user)) })
	for _, grant := range signerEvmRuntimeGrants(env.database, user) {
		env.exec(t, grant)
	}

	runtime := newSQLEvmStateStore(env.open(t, user, "rtpass").DB())
	ctx := context.Background()
	fence := env.lease(t, runtime, "a", time.Minute)
	tx := signFakeTx(t, 7, 1)
	operation := ethcmn.HexToHash("0x01")
	evidence, pending := recordArgs(t, tx, SignOperator, operation)
	recorded, err := runtime.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatalf("Record with runtime grants: %v", err)
	}
	if err := runtime.MarkAmbiguous(ctx, fence, recorded); err != nil {
		t.Fatalf("MarkAmbiguous with runtime grants: %v", err)
	}
	outcome, err := runtime.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep, Source: evmOutcomeSourceResolve})
	if err != nil {
		t.Fatalf("ResolvePending keep with runtime grants: %v", err)
	}
	if err := runtime.ConsumeOutcome(ctx, fence, *outcome); err != nil {
		t.Fatalf("ConsumeOutcome with runtime grants: %v", err)
	}
	if _, err := runtime.RenewLease(ctx, fence, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ReleaseLease(ctx, fence); err != nil {
		t.Fatal(err)
	}

	db := runtime.db
	for _, statement := range []string{
		"UPDATE signer_evm_audit SET detail = 'x'",
		"DELETE FROM signer_evm_audit",
		"UPDATE signer_evm_signed_tx SET raw = 'x'",
		"DELETE FROM signer_evm_signed_tx",
	} {
		err := db.Exec(statement).Error
		if code := mysqlErrNumber(err); code != 1142 {
			t.Fatalf("%q must be denied (1142), got %v", statement, err)
		}
	}
	if n := env.count(t, "signer_evm_signed_tx", ""); n != 1 {
		t.Fatalf("evidence rows: %d", n)
	}
}

// signerEvmRuntimeGrants are the D2 grants of the signer and CLI runtime
// account, as the runbook lists them.
func signerEvmRuntimeGrants(database, user string) []string {
	var grants []string
	for _, table := range []string{sqldb.SignerEvmStateTableName, sqldb.SignerEvmLeaseTableName} {
		grants = append(grants, fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON `%s`.`%s` TO '%s'@'%%'", database, table, user))
	}
	for _, table := range []string{sqldb.SignerEvmAuditTableName, sqldb.SignerEvmSignedTxTableName} {
		grants = append(grants, fmt.Sprintf("GRANT SELECT, INSERT ON `%s`.`%s` TO '%s'@'%%'", database, table, user))
	}
	// the startup schema check reads information_schema, which needs no grant
	return grants
}

// M5: T17 on MySQL. uniq_op only covers active rows; a keep onto an existing
// active row rolls back the pending delete with it.
func TestSignerEvmMySQLOutcomeLifecycle(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	fence := env.lease(t, env.sql, "a", time.Minute)
	ctx := context.Background()
	operation := ethcmn.HexToHash("0x01")
	var outcomeIDs []uint64
	for round, nonce := range []uint64{7, 8} {
		tx := signFakeTx(t, nonce, 1)
		evidence, pending := recordArgs(t, tx, SignOperator, operation)
		recorded, err := env.sql.Record(ctx, fence, evidence, pending)
		if err != nil {
			t.Fatalf("round %d Record: %v", round, err)
		}
		outcome, err := env.sql.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep, ReceiptBlock: 10})
		if err != nil {
			t.Fatalf("round %d keep: %v", round, err)
		}
		if err := env.sql.ConsumeOutcome(ctx, fence, *outcome); err != nil {
			t.Fatalf("round %d consume: %v", round, err)
		}
		if err := env.sql.ConsumeOutcome(ctx, fence, *outcome); !errors.Is(err, errEvmRowGone) {
			t.Fatalf("round %d: a consumed outcome must be gone, got %v", round, err)
		}
		if outcome.ID != recorded.ID {
			t.Fatal("retaining an outcome must preserve the active row ID")
		}
		outcomeIDs = append(outcomeIDs, outcome.ID)
	}
	if outcomeIDs[0] == outcomeIDs[1] {
		t.Fatal("outcome ids must not be reused")
	}
	for _, id := range outcomeIDs {
		if env.count(t, "signer_evm_audit", "outcome_id = ? AND event IN (?, ?)", id, evmAuditOutcomeCreated, evmAuditOutcomeConsumed) != 2 {
			t.Fatalf("outcome %d history is incomplete", id)
		}
	}

	// a keep onto an active row of the same operation conflicts and rolls back
	first := signFakeTx(t, 9, 1)
	evidence, pending := recordArgs(t, first, SignOperator, operation)
	recorded, err := env.sql.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO signer_evm_state (signed_tx_id, chain_id, scope, operation, from_address, nonce, tx_hash, state, source, created_at)
VALUES (?, ?, 'operator', ?, ?, 9, ?, 'blocked_unknown', 'manual', 0)`, recorded.SignedTxID, fakeEvmChainID, operation.Hex(),
		recorded.From.Hex(), first.Hash().Hex())
	if _, err := env.sql.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep}); !errors.Is(err, errEvmStateConflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
	if env.count(t, "signer_evm_state", "id = ? AND state = 'pending'", recorded.ID) != 1 {
		t.Fatal("the pending transition must roll back on an outcome conflict")
	}
	if env.count(t, "signer_evm_state", "state = 'blocked_unknown'") != 1 {
		t.Fatal("the existing outcome must not be overwritten")
	}
}

// M7(a): when the pending insert or the audit insert of Record fails, no
// evidence row is left.
func TestSignerEvmMySQLRecordLeavesNoEvidenceOnFailure(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	fence := env.lease(t, env.sql, "a", time.Minute)
	ctx := context.Background()
	held := signFakeTx(t, 7, 1)
	evidence, pending := recordArgs(t, held, SignOperator, ethcmn.HexToHash("0x01"))
	if _, err := env.sql.Record(ctx, fence, evidence, pending); err != nil {
		t.Fatal(err)
	}

	second := signFakeTx(t, 7, 2)
	evidence, pending = recordArgs(t, second, SignOperator, ethcmn.HexToHash("0x02"))
	if _, err := env.sql.Record(ctx, fence, evidence, pending); !errors.Is(err, errEvmStateConflict) {
		t.Fatalf("a second pending row for the scope must conflict, got %v", err)
	}
	if env.count(t, "signer_evm_signed_tx", "tx_hash = ?", second.Hash().Hex()) != 0 {
		t.Fatal("the conflicting Record left its evidence row")
	}

	third := signFakeTx(t, 8, 1)
	evidence, pending = recordArgs(t, third, SignSeal, ethcmn.HexToHash("0x03"))
	env.failAuditTrigger(t, evmAuditRecorded, third.Hash())
	if _, err := env.sql.Record(ctx, fence, evidence, pending); err == nil {
		t.Fatal("the audit failure must fail Record")
	}
	if env.count(t, "signer_evm_signed_tx", "tx_hash = ?", third.Hash().Hex()) != 0 ||
		env.count(t, "signer_evm_state", "scope = 'seal'") != 0 {
		t.Fatal("the failed Record left rows behind")
	}
}

// M8: an evidence row referenced by a pending or outcome row cannot be
// deleted, even by the migration account.
func TestSignerEvmMySQLEvidenceReferencedCannotBeDeleted(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	fence := env.lease(t, env.sql, "a", time.Minute)
	ctx := context.Background()

	pendingTx := signFakeTx(t, 7, 1)
	evidence, pending := recordArgs(t, pendingTx, SignOperator, ethcmn.HexToHash("0x01"))
	referencedByPending, err := env.sql.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	outcomeTx := signFakeTx(t, 8, 1)
	evidence, pending = recordArgs(t, outcomeTx, SignSeal, ethcmn.HexToHash("0x02"))
	recorded, err := env.sql.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.sql.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep}); err != nil {
		t.Fatal(err)
	}
	freeTx := signFakeTx(t, 9, 1)
	evidence, pending = recordArgs(t, freeTx, SignGc, ethcmn.HexToHash("0x03"))
	unreferenced, err := env.sql.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.sql.ResolvePending(ctx, fence, unreferenced, pendingTerminal{Kind: terminalFailed}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []uint64{referencedByPending.SignedTxID, recorded.SignedTxID} {
		_, err := env.root.Exec("DELETE FROM signer_evm_signed_tx WHERE id = ?", id)
		if code := mysqlErrNumber(err); code != 1451 {
			t.Fatalf("deleting referenced evidence %d must hit the foreign key (1451), got %v", id, err)
		}
	}
	if _, err := env.root.Exec("DELETE FROM signer_evm_signed_tx WHERE id = ?", unreferenced.SignedTxID); err != nil {
		t.Fatalf("unreferenced evidence may be removed by the migration account: %v", err)
	}
}

func TestSignerEvmLegacySchemaRejected(t *testing.T) {
	for _, table := range []string{"signer_evm_pending", "signer_evm_outcome"} {
		t.Run(table, func(t *testing.T) {
			env := newMySQLEvmEnv(t, false)
			env.exec(t, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY)")
			env.exec(t, "INSERT INTO "+table+" VALUES (1)")
			ctx := context.Background()
			for _, check := range []func(context.Context) error{env.store.Migrate, env.store.CheckSchema} {
				if err := check(ctx); err == nil || !strings.Contains(err.Error(), "legacy signer EVM") {
					t.Fatalf("expected legacy rejection, got %v", err)
				}
			}
			if env.count(t, table, "") != 1 {
				t.Fatal("legacy state was modified")
			}
		})
	}
}

func TestSignerEvmMySQLRetainedOutcomesShareScope(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	ctx := context.Background()
	fence := env.lease(t, env.sql, "a", time.Minute)
	for i := uint64(1); i <= 3; i++ {
		operation := ethcmn.BigToHash(new(big.Int).SetUint64(i))
		evidence, pending := recordArgs(t, signFakeTx(t, i, 1), SignOperator, operation)
		recorded, err := env.sql.Record(ctx, fence, evidence, pending)
		if err != nil {
			t.Fatal(err)
		}
		if outcome, err := env.sql.GetOutcome(ctx, fakeEvmChainID, SignOperator, operation); err != nil || outcome != nil {
			t.Fatalf("pending exposed as outcome: %v %v", outcome, err)
		}
		if i < 3 {
			if _, err := env.sql.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep}); err != nil {
				t.Fatal(err)
			}
			if pending, err := env.sql.GetPending(ctx, fakeEvmChainID, SignOperator); err != nil || pending != nil {
				t.Fatalf("outcome exposed as pending: %v %v", pending, err)
			}
		}
	}
	pending, err := env.sql.ListPending(ctx, fakeEvmChainID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}
	outcomes, err := env.sql.ListOutcomes(ctx, fakeEvmChainID)
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes: %v %v", outcomes, err)
	}
	if err := env.sql.ConsumeOutcome(ctx, fence, outcomes[0]); err != nil {
		t.Fatal(err)
	}
	if env.count(t, "signer_evm_state", "state = 'pending'") != 1 {
		t.Fatal("consuming outcome removed pending")
	}
}

func TestSignerEvmMySQLRetainRollsBackWithAudit(t *testing.T) {
	env := newMySQLEvmEnv(t, true)
	ctx := context.Background()
	fence := env.lease(t, env.sql, "a", time.Minute)
	tx := signFakeTx(t, 7, 1)
	evidence, pending := recordArgs(t, tx, SignOperator, ethcmn.HexToHash("0x01"))
	recorded, err := env.sql.Record(ctx, fence, evidence, pending)
	if err != nil {
		t.Fatal(err)
	}
	env.failAuditTrigger(t, evmAuditOutcomeCreated, tx.Hash())
	if _, err := env.sql.ResolvePending(ctx, fence, recorded, pendingTerminal{Kind: terminalSuccessKeep}); err == nil {
		t.Fatal("audit failure must roll back transition")
	}
	if env.count(t, "signer_evm_state", "id = ? AND state = 'pending' AND result_tx_hash = ''", recorded.ID) != 1 {
		t.Fatal("pending was not restored")
	}
	if env.count(t, "signer_evm_audit", "event = ?", evmAuditOutcomeCreated) != 0 {
		t.Fatal("outcome audit survived rollback")
	}
}
