package command

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/urfave/cli/v2"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	"github.com/mocachain/moca-storage-provider/cmd/utils"
	"github.com/mocachain/moca-storage-provider/modular/blocksyncer/test"
	"github.com/mocachain/moca-storage-provider/modular/signer"
)

// fakePreflightChain is a chain whose height grows by one on every
// eth_blockNumber call after the first.
type fakePreflightChain struct {
	mu       sync.Mutex
	height   uint64
	calls    []string
	nonceAt  func(addr ethcmn.Address, height uint64) (uint64, error)
	pending  func(addr ethcmn.Address, call int) (uint64, error)
	receipts map[ethcmn.Hash]*types.Receipt
	pendingN int
	blockErr error
}

func (c *fakePreflightChain) BlockNumber(context.Context) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "blockNumber")
	if c.blockErr != nil {
		return 0, c.blockErr
	}
	height := c.height
	c.height++
	return height, nil
}

func (c *fakePreflightChain) NonceAt(_ context.Context, addr ethcmn.Address, height *big.Int) (uint64, error) {
	c.mu.Lock()
	c.calls = append(c.calls, fmt.Sprintf("nonceAt@%d", height.Uint64()))
	c.mu.Unlock()
	return c.nonceAt(addr, height.Uint64())
}

func (c *fakePreflightChain) PendingNonceAt(_ context.Context, addr ethcmn.Address) (uint64, error) {
	c.mu.Lock()
	c.calls = append(c.calls, "pending")
	c.pendingN++
	call := c.pendingN
	c.mu.Unlock()
	return c.pending(addr, call)
}

func (c *fakePreflightChain) TransactionReceipt(_ context.Context, hash ethcmn.Hash) (*types.Receipt, error) {
	if receipt, ok := c.receipts[hash]; ok {
		return receipt, nil
	}
	return nil, ethereum.NotFound
}

func stableChain() *fakePreflightChain {
	return &fakePreflightChain{
		height:   100,
		nonceAt:  func(ethcmn.Address, uint64) (uint64, error) { return 7, nil },
		pending:  func(ethcmn.Address, int) (uint64, error) { return 7, nil },
		receipts: map[ethcmn.Hash]*types.Receipt{},
	}
}

var preflightTestAccounts = []preflightAccount{
	{Scope: "operator", Address: ethcmn.HexToAddress("0x01")},
	{Scope: "seal", Address: ethcmn.HexToAddress("0x02")},
	{Scope: "gc", Address: ethcmn.HexToAddress("0x03")},
}

func TestPreflightDrained(t *testing.T) {
	chain := stableChain()
	hash := ethcmn.HexToHash("0xaa")
	chain.receipts[hash] = &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(90)}
	report := runPreflight(context.Background(), chain, preflightTestAccounts, []ethcmn.Hash{hash}, time.Second, time.Millisecond)
	if !report.drained() {
		t.Fatalf("expected drained, got %+v", report)
	}
	// H1 is read first, the nonce is bound to H1, then "pending"; the second
	// sample is at a later height.
	if strings.Join(chain.calls[:3], ",") != "blockNumber,nonceAt@100,pending" {
		t.Fatalf("unexpected read order: %v", chain.calls)
	}
	if second := report.Accounts[0].Second.Height; second < 101 {
		t.Fatalf("second sample must be at H1+1 or later, got %d", second)
	}
}

// T16: the old signer has an ambiguous submission that is on chain but whose
// receipt is not visible, and the new tables are empty. Preflight must say
// do not upgrade.
func TestPreflightRefusesWhenAHashHasNoReceipt(t *testing.T) {
	chain := stableChain()
	committedButInvisible := ethcmn.HexToHash("0xbb")
	report := runPreflight(context.Background(), chain, preflightTestAccounts, []ethcmn.Hash{committedButInvisible},
		time.Second, time.Millisecond)
	if report.drained() || report.CheckB || report.Hashes[0].Receipt != "none" {
		t.Fatalf("a hash without a receipt must fail (b), got %+v", report)
	}
	var out bytes.Buffer
	if err := printPreflightReport(&out, report, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "NOT drained; do not upgrade") {
		t.Fatalf("report must say do not upgrade:\n%s", out.String())
	}
}

func TestPreflightRefusesUnstableNonces(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chain func() preflightChain
	}{
		{"pending ahead of committed", func() preflightChain {
			chain := stableChain()
			chain.pending = func(ethcmn.Address, int) (uint64, error) { return 8, nil }
			return chain
		}},
		{"nonce moved between samples", func() preflightChain {
			chain := stableChain()
			chain.nonceAt = func(_ ethcmn.Address, height uint64) (uint64, error) { return 7 + height - 100, nil }
			chain.pending = func(_ ethcmn.Address, call int) (uint64, error) {
				if call <= len(preflightTestAccounts) {
					return 7, nil
				}
				return 8, nil
			}
			return chain
		}},
		{"read error", func() preflightChain {
			chain := stableChain()
			chain.pending = func(ethcmn.Address, int) (uint64, error) { return 0, errors.New("rpc down") }
			return chain
		}},
		{"no new block", func() preflightChain { return &frozenChain{stableChain()} }},
		{"block number error", func() preflightChain {
			chain := stableChain()
			chain.blockErr = errors.New("rpc down")
			return chain
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := runPreflight(context.Background(), tc.chain(), preflightTestAccounts, nil, 50*time.Millisecond, time.Millisecond)
			if report.drained() || report.CheckA {
				t.Fatalf("expected (a) to fail, got %+v", report)
			}
		})
	}
}

// frozenChain never produces a new block.
type frozenChain struct{ *fakePreflightChain }

func (c *frozenChain) BlockNumber(context.Context) (uint64, error) { return 100, nil }

// unsetSignerKeyEnv clears the SIGNER_*_PRIV_KEY overrides for the test.
func unsetSignerKeyEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{signer.SpOperatorPrivKey, signer.SpSealPrivKey, signer.SpGcPrivKey} {
		if value, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { _ = os.Setenv(env, value) })
			_ = os.Unsetenv(env)
		}
	}
}

func testKey(t *testing.T) (string, ethcmn.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey)
}

// Preflight evidence must cover operator, seal and gc: an explicit
// --address replaces one scope's configured key, never the whole set.
func TestPreflightAccountsCoverEveryScope(t *testing.T) {
	unsetSignerKeyEnv(t)
	operatorKey, operatorAddr := testKey(t)
	sealKey, sealAddr := testKey(t)
	gcKey, gcAddr := testKey(t)
	configured := &gfspconfig.GfSpConfig{}
	configured.SpAccount.OperatorPrivateKey, configured.SpAccount.SealPrivateKey, configured.SpAccount.GcPrivateKey =
		operatorKey, sealKey, gcKey
	onlyOperatorKey := &gfspconfig.GfSpConfig{}
	onlyOperatorKey.SpAccount.OperatorPrivateKey = operatorKey
	explicit := ethcmn.HexToAddress("0x0000000000000000000000000000000000000001")
	all := []string{"operator=0x0000000000000000000000000000000000000001,seal=0x0000000000000000000000000000000000000002",
		"gc=0x0000000000000000000000000000000000000003"}

	for _, tc := range []struct {
		name  string
		cfg   *gfspconfig.GfSpConfig
		flags []string
		want  []ethcmn.Address // operator, seal, gc; nil wants an error
	}{
		{"from keys", configured, nil, []ethcmn.Address{operatorAddr, sealAddr, gcAddr}},
		{"all explicit", &gfspconfig.GfSpConfig{}, all,
			[]ethcmn.Address{explicit, ethcmn.HexToAddress("0x02"), ethcmn.HexToAddress("0x03")}},
		{"one explicit, rest from keys", configured, []string{"operator=0x0000000000000000000000000000000000000001"},
			[]ethcmn.Address{explicit, sealAddr, gcAddr}},
		{"omitted scope without a key", onlyOperatorKey, []string{"seal=0x0000000000000000000000000000000000000002"}, nil},
		{"partial explicit, no keys", &gfspconfig.GfSpConfig{}, []string{"operator=0x0000000000000000000000000000000000000001"}, nil},
		{"duplicate scope", configured, []string{"operator=0x0000000000000000000000000000000000000001",
			"operator=0x0000000000000000000000000000000000000002"}, nil},
		{"unknown scope", configured, []string{"approval=0x0000000000000000000000000000000000000001"}, nil},
		{"malformed", configured, []string{"operator"}, nil},
		{"nothing configured", &gfspconfig.GfSpConfig{}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts, err := preflightAccounts(tc.cfg, tc.flags)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("expected an error, got %+v", accounts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []preflightAccount{{Scope: "operator", Address: tc.want[0]}, {Scope: "seal", Address: tc.want[1]},
				{Scope: "gc", Address: tc.want[2]}}
			if fmt.Sprint(accounts) != fmt.Sprint(want) {
				t.Fatalf("got %+v, want %+v", accounts, want)
			}
		})
	}
}

// Even when handed an incomplete account list, preflight never reports a
// drain: a scope it did not sample may still have pending work.
func TestPreflightRefusesIncompleteScopes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		accounts []preflightAccount
	}{
		{"missing seal and gc", preflightTestAccounts[:1]},
		{"duplicate", append(append([]preflightAccount{}, preflightTestAccounts[:2]...), preflightTestAccounts[0])},
		{"unknown", append(append([]preflightAccount{}, preflightTestAccounts[:2]...),
			preflightAccount{Scope: "approval", Address: ethcmn.HexToAddress("0x09")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := runPreflight(context.Background(), stableChain(), tc.accounts, nil, time.Second, time.Millisecond)
			if report.drained() || report.CheckA {
				t.Fatalf("an incomplete account list must not pass, got %+v", report)
			}
		})
	}
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func runSignerEvmApp(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	app := &cli.App{
		Name:      "moca-sp",
		Flags:     []cli.Flag{utils.ConfigFileFlag},
		Commands:  []*cli.Command{SignerEvmCmd},
		Writer:    &out,
		ErrWriter: &errOut,
		ExitErrHandler: func(*cli.Context, error) {
			// keep the test process alive; the error is returned to the caller
		},
	}
	err := app.Run(append([]string{"moca-sp"}, args...))
	return out.String() + errOut.String(), err
}

// migrate and list end to end against MySQL.
func TestSignerEvmMigrateAndList(t *testing.T) {
	user := envOrDefault("SIGNER_EVM_TEST_DB_USER", envOrDefault("BLOCKSYNCER_TEST_DB_USER", "root"))
	password := envOrDefault("SIGNER_EVM_TEST_DB_PASSWORD", envOrDefault("BLOCKSYNCER_TEST_DB_PASSWORD", "root"))
	address := envOrDefault("SIGNER_EVM_TEST_DB_ADDRESS", envOrDefault("BLOCKSYNCER_TEST_DB_ADDRESS", "127.0.0.1:3306"))
	database := test.CreateDatabase(t, user, password, address, "signer_evm_cli")
	for _, env := range []string{"SP_DB_USER", "SP_DB_PASSWORD", "SP_DB_ADDRESS", "SP_DB_DATABASE"} {
		if value, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { _ = os.Setenv(env, value) })
			_ = os.Unsetenv(env)
		}
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	config := fmt.Sprintf("[Chain]\nChainID = 'moca_5151-1'\n\n[SpDB]\nUser = '%s'\nPasswd = '%s'\nAddress = '%s'\nDatabase = '%s'\n",
		user, password, address, database)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := runSignerEvmApp(t, "-c", configPath, "signer-evm", "list"); err == nil {
		t.Fatalf("list before migrate must fail:\n%s", out)
	}
	out, err := runSignerEvmApp(t, "-c", configPath, "signer-evm", "migrate")
	if err != nil || !strings.Contains(out, "signer EVM tables are ready") {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	out, err = runSignerEvmApp(t, "-c", configPath, "signer-evm", "list")
	if err != nil || !strings.Contains(out, "chain_id=moca_5151-1 evm_chain_id=5151") || !strings.Contains(out, "lease: none") {
		t.Fatalf("list on empty tables: %v\n%s", err, out)
	}

	// a corrupt evidence row is reported and turns the exit code to 2
	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s", user, password, address, database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO signer_evm_signed_tx (chain_id, evm_chain_id, scope, operation, from_address, nonce, tx_hash, raw, recorded_block, lease_epoch, created_at)
VALUES ('moca_5151-1', 5151, 'operator', ?, ?, 7, ?, 'not a transaction', 1, 1, 0)`,
		ethcmn.HexToHash("0x01").Hex(), ethcmn.HexToAddress("0x01").Hex(), ethcmn.HexToHash("0x02").Hex()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO signer_evm_state (chain_id, signed_tx_id, scope, from_address, nonce, tx_hash, operation, lease_epoch, created_at, updated_at)
VALUES ('moca_5151-1', LAST_INSERT_ID(), 'operator', ?, 7, ?, ?, 1, 0, 0)`,
		ethcmn.HexToAddress("0x01").Hex(), ethcmn.HexToHash("0x02").Hex(), ethcmn.HexToHash("0x01").Hex()); err != nil {
		t.Fatal(err)
	}
	out, err = runSignerEvmApp(t, "-c", configPath, "signer-evm", "list", "--json")
	var exitErr cli.ExitCoder
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != signerEvmExitNotSatisfied ||
		!strings.Contains(out, `"evidence_valid": false`) || !strings.Contains(out, "does not decode") {
		t.Fatalf("list must flag the corrupt row with exit code 2: %v\n%s", err, out)
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr cli.ExitCoder
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// release and dispose-outcome end to end against MySQL: their flags, exit
// codes, dry run and audit. resolve and resolve-outcome need a chain; their
// decisions are covered in modular/signer.
func TestSignerEvmOutcomeCommands(t *testing.T) {
	user := envOrDefault("SIGNER_EVM_TEST_DB_USER", envOrDefault("BLOCKSYNCER_TEST_DB_USER", "root"))
	password := envOrDefault("SIGNER_EVM_TEST_DB_PASSWORD", envOrDefault("BLOCKSYNCER_TEST_DB_PASSWORD", "root"))
	address := envOrDefault("SIGNER_EVM_TEST_DB_ADDRESS", envOrDefault("BLOCKSYNCER_TEST_DB_ADDRESS", "127.0.0.1:3306"))
	database := test.CreateDatabase(t, user, password, address, "signer_evm_cli")
	for _, env := range []string{"SP_DB_USER", "SP_DB_PASSWORD", "SP_DB_ADDRESS", "SP_DB_DATABASE"} {
		if value, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { _ = os.Setenv(env, value) })
			_ = os.Unsetenv(env)
		}
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	config := fmt.Sprintf("[Chain]\nChainID = 'moca_5151-1'\n\n[SpDB]\nUser = '%s'\nPasswd = '%s'\nAddress = '%s'\nDatabase = '%s'\n",
		user, password, address, database)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runSignerEvmApp(t, "-c", configPath, "signer-evm", "migrate"); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s)/%s", user, password, address, database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// one blocked_unknown and one succeeded outcome with valid evidence
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	insert := func(nonce uint64, operation, state string) int64 {
		to := ethcmn.HexToAddress("0x2000")
		tx, err := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(5151)), &types.LegacyTx{
			Nonce: nonce, GasPrice: big.NewInt(1), Gas: 21000, To: &to, Value: big.NewInt(0),
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := tx.MarshalBinary()
		res, err := db.Exec(`INSERT INTO signer_evm_signed_tx (chain_id, evm_chain_id, scope, operation, from_address, nonce, tx_hash, raw, recorded_block, lease_epoch, created_at)
VALUES ('moca_5151-1', 5151, 'operator', ?, ?, ?, ?, ?, 100, 1, 0)`, operation, from.Hex(), nonce, tx.Hash().Hex(), raw)
		if err != nil {
			t.Fatal(err)
		}
		signedTxID, _ := res.LastInsertId()
		res, err = db.Exec(`INSERT INTO signer_evm_state (chain_id, signed_tx_id, scope, from_address, nonce, tx_hash, operation, state, reason, source, created_at)
VALUES ('moca_5151-1', ?, 'operator', ?, ?, ?, ?, ?, 'receipt_missing', 'manual', 0)`, signedTxID, from.Hex(), nonce, tx.Hash().Hex(), operation, state)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	blocked := insert(7, ethcmn.HexToHash("0x01").Hex(), "blocked_unknown")
	succeeded := insert(8, ethcmn.HexToHash("0x02").Hex(), "succeeded")
	count := func(query string, args ...interface{}) int {
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	id := func(v int64) string { return fmt.Sprint(v) }
	base := []string{"-c", configPath, "signer-evm"}
	run := func(args ...string) (string, int) {
		out, err := runSignerEvmApp(t, append(append([]string{}, base...), args...)...)
		return out, exitCodeOf(err)
	}

	for _, args := range [][]string{
		{"release", "--outcome-id", id(blocked), "--operator", "ops", "--reason", "r", "--business-evidence", "e"},
		{"release", "--outcome-id", id(blocked), "--operator", "ops", "--reason", "r", "--decision-ref", "d"},
		{"release", "--outcome-id", id(blocked), "--reason", "r", "--decision-ref", "d", "--business-evidence", "e"},
		{"resolve", "--scope", "operator", "--tx-hash", "0x12", "--operator", "ops", "--reason", "r"},
		{"resolve", "--scope", "operator", "--tx-hash", ethcmn.HexToHash("0x12").Hex(), "--operator", "ops", "--reason", "r"},
	} {
		if out, code := run(args...); code != 1 {
			t.Fatalf("%v: want exit 1, got %d\n%s", args, code, out)
		}
	}
	release := []string{"release", "--outcome-id", id(blocked), "--operator", "ops", "--reason", "Board approved",
		"--decision-ref", "https://board.example/d/1", "--business-evidence", "deposit balance unchanged"}
	if out, code := run(append(release, "--dry-run")...); code != 0 || !strings.Contains(out, "action (would)") ||
		count("SELECT COUNT(*) FROM signer_evm_state") != 2 || count("SELECT COUNT(*) FROM signer_evm_audit") != 0 {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if out, code := run(release...); code != 0 || count("SELECT COUNT(*) FROM signer_evm_state WHERE id = ?", blocked) != 0 ||
		count("SELECT COUNT(*) FROM signer_evm_audit WHERE event = 'manual_release_unknown' AND decision_ref = 'https://board.example/d/1' AND actor LIKE 'ops (os user %'") != 1 {
		t.Fatalf("release: exit %d\n%s", code, out)
	}
	if out, code := run(release...); code != 3 || count("SELECT COUNT(*) FROM signer_evm_audit WHERE event = 'manual_rejected'") != 1 {
		t.Fatalf("a second release must exit 3 with manual_rejected: exit %d\n%s", code, out)
	}
	dispose := []string{"dispose-outcome", "--outcome-id", id(succeeded), "--operator", "ops", "--reason", "business confirmed", "--json"}
	if out, code := run(dispose...); code != 0 || !strings.Contains(out, `"audit_event": "manual_dispose_outcome"`) ||
		count("SELECT COUNT(*) FROM signer_evm_state") != 0 {
		t.Fatalf("dispose-outcome: exit %d\n%s", code, out)
	}
}
