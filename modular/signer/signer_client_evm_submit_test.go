package signer

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/sdk/client"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// MOCA-1284: the first deposit is committed but its response is lost, the
// next eth_gasPrice differs, so a re-signed transaction at the same nonce
// misses the tx cache and is rejected with "invalid nonce". The deposit must
// not then be re-signed at the next nonce.
func TestDepositEvmAmbiguousSubmitDoesNotResignAtNextNonce(t *testing.T) {
	const nonce = uint64(7)
	node := newFakeEvmNode(t, nonce)
	node.gasPrices = []*big.Int{big.NewInt(1_000_000_000), big.NewInt(1_100_000_000)}
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			return fakeEvmAction{kind: fakeEvmAdmitThenDrop}
		}
		return fakeEvmAction{}
	}
	installFakeEvmSeams(t, node)
	signClient := newFakeEvmSignClient(t, node, nonce)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	t.Logf("DepositEvm returned hash=%q err=%v", txHash, err)

	accepted, sends, _ := node.snapshot()
	for _, tx := range accepted {
		if tx.Nonce() != nonce {
			t.Fatalf("fake accepted a deposit at nonce %d after the nonce-%d deposit was committed; %s",
				tx.Nonce(), nonce, node.describe())
		}
	}
	for _, send := range sends {
		if send.nonce != nonce {
			t.Fatalf("a transaction at nonce %d was sent; %s", send.nonce, node.describe())
		}
	}
}

const scenarioNonce = uint64(7)

func scenarioCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newScenario(t *testing.T) (*fakeEvmNode, *MocaChainSignClient) {
	t.Helper()
	node := newFakeEvmNode(t, scenarioNonce)
	installFakeEvmSeams(t, node)
	return node, newFakeEvmSignClient(t, node, scenarioNonce)
}

func scriptFirst(action fakeEvmAction) func(int, *ethtypes.Transaction) fakeEvmAction {
	return func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			return action
		}
		return fakeEvmAction{kind: fakeEvmDefault}
	}
}

func scriptAll(action fakeEvmAction) func(int, *ethtypes.Transaction) fakeEvmAction {
	return func(int, *ethtypes.Transaction) fakeEvmAction { return action }
}

// pendingEvmTx is the view of a scope's pending row the A-era tests check:
// the stored row plus its evidence bytes.
type pendingEvmTx struct {
	operation ethcmn.Hash
	nonce     uint64
	hash      ethcmn.Hash
	raw       []byte
	ambiguous bool
}

func pendingFor(t *testing.T, signClient *MocaChainSignClient) (pendingEvmTx, bool) {
	t.Helper()
	return pendingForScope(t, signClient, SignOperator)
}

func pendingForScope(t *testing.T, signClient *MocaChainSignClient, scope SignType) (pendingEvmTx, bool) {
	t.Helper()
	store := memStoreOf(t, signClient)
	row, ok := store.pendingOf(scope)
	if !ok {
		return pendingEvmTx{}, false
	}
	evidence, ok := store.signedRow(row.SignedTxID)
	if !ok {
		t.Fatalf("pending row %d references missing evidence %d", row.ID, row.SignedTxID)
	}
	return pendingEvmTx{operation: row.Operation, nonce: row.Nonce, hash: row.TxHash, raw: evidence.Raw, ambiguous: row.Ambiguous}, true
}

// assertSignedOnce checks that every send carried the same bytes at the
// scenario nonce, i.e. the operation was signed once and never re-signed.
func assertSignedOnce(t *testing.T, node *fakeEvmNode) fakeEvmSend {
	t.Helper()
	accepted, sends, _ := node.snapshot()
	if len(sends) == 0 {
		t.Fatalf("nothing was sent; %s", node.describe())
	}
	for _, send := range sends {
		if string(send.raw) != string(sends[0].raw) || send.nonce != scenarioNonce {
			t.Fatalf("operation was re-signed; %s", node.describe())
		}
	}
	for _, tx := range accepted {
		if tx.Nonce() != scenarioNonce {
			t.Fatalf("fake accepted a transaction at nonce %d; %s", tx.Nonce(), node.describe())
		}
	}
	return sends[0]
}

func assertAmbiguousPending(t *testing.T, signClient *MocaChainSignClient, first fakeEvmSend) {
	t.Helper()
	pending, ok := pendingFor(t, signClient)
	if !ok {
		t.Fatal("pending entry was cleared without a receipt")
	}
	if !pending.ambiguous || pending.hash != first.hash || pending.nonce != scenarioNonce || string(pending.raw) != string(first.raw) {
		t.Fatalf("unexpected pending entry: hash=%s nonce=%d ambiguous=%t raw-matches=%t",
			pending.hash.Hex(), pending.nonce, pending.ambiguous, string(pending.raw) == string(first.raw))
	}
}

// Scenario 1: admitted, response lost, receipt not yet visible.
func TestEvmSubmitLostResponseKeepsAmbiguousPending(t *testing.T) {
	node, signClient := newScenario(t)
	node.gasPrices = []*big.Int{big.NewInt(1_000_000_000), big.NewInt(1_100_000_000)}
	node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
	node.hideReceipts = true

	txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an unconfirmed error, got hash=%q err=%v", txHash, err)
	}
	assertAmbiguousPending(t, signClient, first)
	if signClient.operatorAccNonce != scenarioNonce+1 {
		t.Fatalf("nonce cache = %d, want %d", signClient.operatorAccNonce, scenarioNonce+1)
	}
}

// Scenarios 2a and 2a': the original is committed; the re-send of the same
// bytes gets "invalid nonce" (cache miss) or a tx-cache error (cache hit).
func TestEvmSubmitCommittedOriginalResolvesByReceipt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		disableCache bool
		wantResend   string
	}{
		{"2a cache miss invalid nonce", true, "invalid nonce"},
		{"2a' cache hit", false, "tx already exists in cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.gasPrices = []*big.Int{big.NewInt(1_000_000_000), big.NewInt(1_100_000_000)}
			node.disableCache = tc.disableCache
			node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})

			txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
			if err != nil {
				t.Fatalf("unexpected error: %v; %s", err, node.describe())
			}
			first := assertSignedOnce(t, node)
			if txHash != first.hash.String() {
				t.Fatalf("returned %s, want original %s", txHash, first.hash.String())
			}
			_, sends, _ := node.snapshot()
			if len(sends) != 2 || !strings.Contains(sends[1].result, tc.wantResend) {
				t.Fatalf("expected one re-send answered with %q; %s", tc.wantResend, node.describe())
			}
			if _, ok := pendingFor(t, signClient); ok {
				t.Fatal("pending entry not cleared after receipt")
			}
			if signClient.operatorAccNonce != scenarioNonce+1 {
				t.Fatalf("nonce cache = %d, want %d", signClient.operatorAccNonce, scenarioNonce+1)
			}
		})
	}
}

// Scenario 2a-double-prime: a tx-cache error on the very first send is not a refusal.
func TestEvmSubmitFirstCacheErrorIsAmbiguous(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = scriptAll(fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: fakeEvmCacheError})
	node.hideReceipts = true

	txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an unconfirmed error, got hash=%q err=%v", txHash, err)
	}
	assertAmbiguousPending(t, signClient, first)
}

// Scenario 2b: nil from the node is not success; only a receipt is.
func TestEvmSubmitNilResponseWaitsForReceipt(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = scriptAll(fakeEvmAction{kind: fakeEvmAnswerHashOnly})

	txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("nil response without receipt reported success: hash=%q err=%v", txHash, err)
	}
	pending, ok := pendingFor(t, signClient)
	if !ok || pending.hash != first.hash || pending.ambiguous {
		t.Fatalf("expected non-ambiguous pending entry for %s, got %+v ok=%t", first.hash.Hex(), pending, ok)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 1 {
		t.Fatalf("accepted request must not be re-sent; %s", node.describe())
	}
}

// Scenario 2c: nonce consumed by an unknown transaction, original never mined.
func TestEvmSubmitNonceConsumedElsewhereKeepsScopeBlocked(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			node.commitExternal()
			return fakeEvmAction{kind: fakeEvmDropOnly}
		}
		return fakeEvmAction{}
	}

	ctx := scenarioCtx(t)
	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an error, got hash=%q err=%v", txHash, err)
	}
	assertAmbiguousPending(t, signClient, first)

	_, before, _ := node.snapshot()
	txHash, err = signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	if err == nil || txHash != first.hash.String() || !strings.Contains(err.Error(), "submitted but unconfirmed") {
		t.Fatalf("next call must stay blocked on %s, got hash=%q err=%v", first.hash.Hex(), txHash, err)
	}
	assertSignedOnce(t, node)
	if _, after, _ := node.snapshot(); len(after) != len(before)+1 {
		t.Fatalf("blocked call must re-broadcast the stored bytes once; %s", node.describe())
	}
}

// The deposit was admitted at nonce N, so the account nonce is N+1, but no
// receipt can be read yet. "Nonce consumed" must not release the scope here:
// clearing pending lets the next call re-sign the same deposit at N+1, and
// MsgDeposit has no on-chain dedup, so it would be paid twice. Combining this
// branch with a nonce-consumed shortcut (as in PR #188) must keep this passing.
func TestEvmSubmitConsumedNonceWithoutReceiptKeepsScopeBlocked(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nilReceipt bool
	}{
		{"receipt not found", false},
		{"nil receipt without error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
			node.hideReceipts = true
			if tc.nilReceipt {
				transactionReceiptFn = func(context.Context, *ethclient.Client, ethcmn.Hash) (*ethtypes.Receipt, error) {
					return nil, nil
				}
			}
			ctx := scenarioCtx(t)

			txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
			first := assertSignedOnce(t, node)
			if err == nil || txHash != first.hash.String() {
				t.Fatalf("expected original hash with an unconfirmed error, got hash=%q err=%v", txHash, err)
			}
			accepted, before, _ := node.snapshot()
			if len(accepted) != 1 {
				t.Fatalf("first deposit must be admitted exactly once; %s", node.describe())
			}
			assertAmbiguousPending(t, signClient, first)
			for name, nonceFn := range map[string]func() (uint64, error){
				"evm":    func() (uint64, error) { return getNonceOnChainFn(ctx, signClient, nil) },
				"cosmos": func() (uint64, error) { return getCosmosNonceFn(nil, ctx) },
			} {
				if nonce, err := nonceFn(); err != nil || nonce != scenarioNonce+1 {
					t.Fatalf("%s nonce = %d err=%v, want %d (consumed)", name, nonce, err, scenarioNonce+1)
				}
			}

			txHash, err = signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
			if err == nil || txHash != first.hash.String() || !strings.Contains(err.Error(), "submitted but unconfirmed") {
				t.Fatalf("repeat deposit must stay blocked on %s, got hash=%q err=%v; %s",
					first.hash.Hex(), txHash, err, node.describe())
			}
			assertSignedOnce(t, node)
			accepted, after, _ := node.snapshot()
			if len(accepted) != 1 {
				t.Fatalf("deposit was admitted twice; %s", node.describe())
			}
			if len(after) != len(before)+1 {
				t.Fatalf("blocked call must re-broadcast the stored bytes once; %s", node.describe())
			}
			assertAmbiguousPending(t, signClient, first)
		})
	}
}

// Scenario 3: receipt wait timed out; the next call is blocked and
// re-broadcasts, and is released once the receipt shows up.
func TestEvmSubmitReceiptTimeoutBlocksUntilReceipt(t *testing.T) {
	node, signClient := newScenario(t)
	node.hideReceipts = true
	ctx := scenarioCtx(t)

	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an unconfirmed error, got hash=%q err=%v", txHash, err)
	}

	otherOp := fakeDepositMsg(2000)
	if txHash, err = signClient.DepositEvm(ctx, SignOperator, otherOp); err == nil || txHash != first.hash.String() {
		t.Fatalf("scope must stay blocked while unconfirmed, got hash=%q err=%v", txHash, err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 2 || string(sends[1].raw) != string(first.raw) {
		t.Fatalf("blocked call must re-broadcast the stored bytes; %s", node.describe())
	}

	node.setHideReceipts(false)
	if txHash, err = signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000)); err != nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash once mined, got hash=%q err=%v", txHash, err)
	}
	if _, err = signClient.DepositEvm(ctx, SignOperator, otherOp); err != nil {
		t.Fatalf("a new operation after confirmation failed: %v; %s", err, node.describe())
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 2 || accepted[1].Nonce() != scenarioNonce+1 {
		t.Fatalf("expected the second operation at nonce %d; %s", scenarioNonce+1, node.describe())
	}
}

// Scenario 4: a verified refusal on the first send clears pending and is not
// re-signed.
func TestEvmSubmitVerifiedRefusalIsNotResigned(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = scriptFirst(fakeEvmAction{kind: fakeEvmRPCError, code: -32000,
		message: "failed to broadcast transaction: insufficient funds"})
	ctx := scenarioCtx(t)

	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	if err == nil || txHash != "" {
		t.Fatalf("expected refusal without hash, got hash=%q err=%v", txHash, err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 1 {
		t.Fatalf("refused transaction was re-signed or re-sent; %s", node.describe())
	}
	if _, ok := pendingFor(t, signClient); ok {
		t.Fatal("refused transaction left pending state")
	}
	if signClient.operatorAccNonce != scenarioNonce {
		t.Fatalf("nonce cache = %d, want unchanged %d", signClient.operatorAccNonce, scenarioNonce)
	}

	if _, err = signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000)); err != nil {
		t.Fatalf("new call after refusal failed: %v; %s", err, node.describe())
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 1 || accepted[0].Nonce() != scenarioNonce {
		t.Fatalf("expected one accepted tx at nonce %d; %s", scenarioNonce, node.describe())
	}
}

// Scenario 4b: a nonce refusal on the first send refreshes and re-signs once.
func TestEvmSubmitNonceRefusalRefreshesAndResigns(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cachedNonce uint64
	}{
		{"nonce low", scenarioNonce - 1},
		{"nonce high", scenarioNonce + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := newFakeEvmNode(t, scenarioNonce)
			installFakeEvmSeams(t, node)
			signClient := newFakeEvmSignClient(t, node, tc.cachedNonce)
			// Lease recovery refreshes the cache from the chain; make it
			// stale again to drive the refusal.
			signClient.operatorAccNonce = tc.cachedNonce

			txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
			if err != nil {
				t.Fatalf("unexpected error: %v; %s", err, node.describe())
			}
			accepted, sends, _ := node.snapshot()
			if len(sends) != 2 || sends[0].nonce != tc.cachedNonce || len(accepted) != 1 ||
				accepted[0].Nonce() != scenarioNonce || accepted[0].Hash().String() != txHash {
				t.Fatalf("expected one refused send then one accepted at %d; %s", scenarioNonce, node.describe())
			}
			if _, ok := pendingFor(t, signClient); ok {
				t.Fatal("pending entry left after confirmation")
			}
		})
	}
}

// Scenarios 4c and 4c': unknown or transport-level failures are ambiguous even
// when their text contains an allowlisted refusal reason.
func TestEvmSubmitUnprovenFailuresAreAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action fakeEvmAction
	}{
		{"-32002 timeout", fakeEvmAction{kind: fakeEvmRPCError, code: -32002, message: "request timed out"}},
		{"-32603 internal", fakeEvmAction{kind: fakeEvmRPCError, code: -32603, message: "internal error"}},
		{"unknown broadcast failure", fakeEvmAction{kind: fakeEvmRPCError, code: -32000,
			message: "failed to broadcast transaction: post failed: Post \"http://localhost:26657\": EOF"}},
		{"http 503", fakeEvmAction{kind: fakeEvmHTTPStatus, status: 503, body: "upstream unavailable"}},
		{"http 503 with nonce body", fakeEvmAction{kind: fakeEvmHTTPStatus, status: 503,
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"invalid nonce; got 7, expected 8"}}`}},
		{"-32603 with nonce text", fakeEvmAction{kind: fakeEvmRPCError, code: -32603,
			message: "failed to broadcast transaction: invalid nonce; got 7, expected 8"}},
		{"-32002 with funds text", fakeEvmAction{kind: fakeEvmRPCError, code: -32002,
			message: "request timed out: insufficient funds"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.script = scriptAll(tc.action)
			node.hideReceipts = true

			txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
			first := assertSignedOnce(t, node)
			if err == nil || txHash != first.hash.String() {
				t.Fatalf("expected original hash with an error, got hash=%q err=%v", txHash, err)
			}
			assertAmbiguousPending(t, signClient, first)
		})
	}
}

// Scenario 4d: after an EOF, a refusal of the re-send must not clear pending.
func TestEvmSubmitRefusalAfterAmbiguousSendKeepsPending(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			return fakeEvmAction{kind: fakeEvmDropOnly}
		}
		return fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: "failed to broadcast transaction: insufficient funds"}
	}

	txHash, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an error, got hash=%q err=%v", txHash, err)
	}
	assertAmbiguousPending(t, signClient, first)
}

// Scenario 5a: a context cancelled before sending sends and records nothing.
func TestEvmSubmitCancelledBeforeSendRecordsNothing(t *testing.T) {
	node, signClient := newScenario(t)
	ctx := scenarioCtx(t)
	txOpts, err := CreateTxOpts(ctx, signClient.evmClient, signClient.evmPrivateKeys[SignOperator], big.NewInt(5151),
		300000, scenarioNonce, signClient.maxEvmGasPrice)
	if err != nil {
		t.Fatal(err)
	}
	signedTx, err := txOpts.Signer(txOpts.From, ethtypes.NewTransaction(scenarioNonce, txOpts.From, big.NewInt(0), 300000, txOpts.GasPrice, nil))
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	txHash, err := signClient.submitEvmTx(cancelled, SignOperator, ethcmn.HexToHash("0x01"), scenarioNonce, signedTx)
	if !errors.Is(err, context.Canceled) || txHash != "" {
		t.Fatalf("expected cancellation without hash, got hash=%q err=%v", txHash, err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 0 {
		t.Fatalf("server received a request; %s", node.describe())
	}
	if _, ok := pendingFor(t, signClient); ok {
		t.Fatal("cancelled submit recorded pending state")
	}
}

// Scenario 5b: cancelled while the server is handling the admitted send. A
// later call with a fresh context re-sends the same bytes and is released once
// the receipt exists.
func TestEvmSubmitCancelledDuringSendRecoversWithSameBytes(t *testing.T) {
	node, signClient := newScenario(t)
	node.hideReceipts = true
	ctx, cancel := context.WithCancel(scenarioCtx(t))
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			cancel()
			return fakeEvmAction{kind: fakeEvmAdmitThenBlock}
		}
		return fakeEvmAction{kind: fakeEvmDefault}
	}

	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an error, got hash=%q err=%v", txHash, err)
	}
	if _, sends, _ := node.snapshot(); len(sends) != 1 {
		t.Fatalf("expected exactly one send before cancellation; %s", node.describe())
	}
	assertAmbiguousPending(t, signClient, first)

	fresh := scenarioCtx(t)
	if txHash, err = signClient.DepositEvm(fresh, SignOperator, fakeDepositMsg(1000)); err == nil || txHash != first.hash.String() {
		t.Fatalf("expected blocked call to re-send and report unconfirmed, got hash=%q err=%v", txHash, err)
	}
	node.setHideReceipts(false)
	if txHash, err = signClient.DepositEvm(fresh, SignOperator, fakeDepositMsg(1000)); err != nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash after receipt, got hash=%q err=%v; %s", txHash, err, node.describe())
	}
	assertSignedOnce(t, node)
	accepted, _, _ := node.snapshot()
	if len(accepted) != 1 || accepted[0].Hash() != first.hash {
		t.Fatalf("expected exactly the original transaction on chain; %s", node.describe())
	}
}

// Scenario 5c: the context is cancelled after the pending entry is recorded
// but before the first request leaves the client, so the server sees nothing.
// The entry must still let a later call deliver the same bytes instead of
// blocking the scope for good.
func TestEvmSubmitCancelledBeforeDeliveryRecoversWithSameBytes(t *testing.T) {
	node, signClient := newScenario(t)
	ctx, cancel := context.WithCancel(scenarioCtx(t))
	originalSend := sendEvmTxFn
	t.Cleanup(func() { sendEvmTxFn = originalSend })
	var seamCalls int
	sendEvmTxFn = func(sendCtx context.Context, evmClient *ethclient.Client, tx *ethtypes.Transaction) error {
		seamCalls++
		if seamCalls == 1 {
			cancel()
		}
		return originalSend(sendCtx, evmClient, tx)
	}

	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	if _, sends, _ := node.snapshot(); len(sends) != 0 {
		t.Fatalf("first call reached the server; %s", node.describe())
	}
	pending, ok := pendingFor(t, signClient)
	if !ok || !pending.ambiguous || pending.nonce != scenarioNonce || len(pending.raw) == 0 {
		t.Fatalf("expected ambiguous pending entry with raw bytes, got %+v ok=%t", pending, ok)
	}
	pendingTx := new(ethtypes.Transaction)
	if err := pendingTx.UnmarshalBinary(pending.raw); err != nil || pendingTx.Hash() != pending.hash || pendingTx.Nonce() != scenarioNonce {
		t.Fatalf("pending raw does not match its hash/nonce: err=%v", err)
	}
	if err == nil || txHash != pending.hash.String() {
		t.Fatalf("expected pending hash with an error, got hash=%q err=%v", txHash, err)
	}

	fresh := scenarioCtx(t)
	var released bool
	for call := 0; call < 2 && !released; call++ {
		txHash, err = signClient.DepositEvm(fresh, SignOperator, fakeDepositMsg(1000))
		if txHash != pending.hash.String() {
			t.Fatalf("later call returned a different hash %q; %s", txHash, node.describe())
		}
		released = err == nil
	}
	if !released {
		t.Fatalf("scope still blocked after the receipt became available: %v; %s", err, node.describe())
	}
	_, sends, _ := node.snapshot()
	if len(sends) == 0 {
		t.Fatalf("later calls never delivered the pending bytes; %s", node.describe())
	}
	for _, send := range sends {
		if string(send.raw) != string(pending.raw) {
			t.Fatalf("later call sent different bytes; %s", node.describe())
		}
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 1 || accepted[0].Hash() != pending.hash {
		t.Fatalf("expected exactly the original transaction on chain; %s", node.describe())
	}
	if _, ok := pendingFor(t, signClient); ok {
		t.Fatal("pending entry kept after the receipt was found")
	}
}

// Scenario 6: a repeated call after the original was mined returns its hash
// without sending anything.
func TestEvmSubmitRepeatAfterMinedReturnsOriginalHash(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
	node.hideReceipts = true
	ctx := scenarioCtx(t)

	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	first := assertSignedOnce(t, node)
	if err == nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash with an error, got hash=%q err=%v", txHash, err)
	}
	node.setHideReceipts(false)
	_, before, _ := node.snapshot()
	if txHash, err = signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000)); err != nil || txHash != first.hash.String() {
		t.Fatalf("expected original hash, got hash=%q err=%v", txHash, err)
	}
	if _, after, _ := node.snapshot(); len(after) != len(before) {
		t.Fatalf("repeat call sent a transaction; %s", node.describe())
	}
}

// Scenario 7, the known limit of A (pending lost on restart), is closed by the
// persisted state: see TestEvmStateUnconfirmedSurvivesRestart.

// retryLoopMethod drives one of the two methods that gained a retry loop in
// MOCA-1286, with the nonce cache of its signing scope.
type retryLoopMethod struct {
	name  string
	scope SignType
	call  func(context.Context, *MocaChainSignClient) (string, error)
	nonce func(*MocaChainSignClient) *uint64
}

var retryLoopMethods = []retryLoopMethod{
	{"DiscontinueBucketEvm", SignGc, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
		return c.DiscontinueBucketEvm(ctx, SignGc, &storagetypes.MsgDiscontinueBucket{BucketName: "bucket", Reason: "test"})
	}, func(c *MocaChainSignClient) *uint64 { return &c.gcAccNonce }},
	{"UpdateSPPriceEvm", SignOperator, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
		return c.UpdateSPPriceEvm(ctx, SignOperator, &sptypes.MsgUpdateSpStoragePrice{
			ReadPrice: sdkmath.LegacyNewDec(1), FreeReadQuota: 1, StorePrice: sdkmath.LegacyNewDec(1),
		})
	}, func(c *MocaChainSignClient) *uint64 { return &c.operatorAccNonce }},
}

// newStaleNonceScenario builds a client whose nonce cache for m's scope is set
// to cached while the chain stays at scenarioNonce.
func newStaleNonceScenario(t *testing.T, m retryLoopMethod, cached uint64) (*fakeEvmNode, *MocaChainSignClient) {
	t.Helper()
	node, signClient := newScenario(t)
	// Lease recovery refreshes the cache from the chain; make it stale again
	// to drive the refusal.
	*m.nonce(signClient) = cached
	return node, signClient
}

// assertNoEvmState checks that no pending row and no outcome is left.
func assertNoEvmState(t *testing.T, signClient *MocaChainSignClient, scope SignType) {
	t.Helper()
	if _, ok := pendingForScope(t, signClient, scope); ok {
		t.Fatal("pending entry left behind")
	}
	if outcomes := memStoreOf(t, signClient).outcomeRows(); len(outcomes) != 0 {
		t.Fatalf("unexpected outcomes: %+v", outcomes)
	}
}

// Scenario 8 (all 19 methods use the helper) is enforced by
// TestAllEvmSubmissionMethodsTrackPendingTransactions. MOCA-1286: the two
// methods that had no retry loop re-sign after a recorded nonce refusal, like
// the other 17.
func TestEvmSubmitRetryMethodNonceRefusalResigns(t *testing.T) {
	for _, m := range retryLoopMethods {
		for _, tc := range []struct {
			name   string
			cached uint64
		}{
			{"nonce low", scenarioNonce - 1},
			{"nonce high", scenarioNonce + 1},
		} {
			t.Run(m.name+"/"+tc.name, func(t *testing.T) {
				node, signClient := newStaleNonceScenario(t, m, tc.cached)

				txHash, err := m.call(scenarioCtx(t), signClient)
				if err != nil {
					t.Fatalf("unexpected error: %v; %s", err, node.describe())
				}
				accepted, sends, _ := node.snapshot()
				if len(sends) != 2 || sends[0].nonce != tc.cached || sends[1].nonce != scenarioNonce || len(accepted) != 1 ||
					accepted[0].Nonce() != scenarioNonce || accepted[0].Hash().String() != txHash {
					t.Fatalf("expected one refused send then one accepted at %d; %s", scenarioNonce, node.describe())
				}
				if got := *m.nonce(signClient); got != scenarioNonce+1 {
					t.Fatalf("nonce cache = %d, want %d", got, scenarioNonce+1)
				}
				assertNoEvmState(t, signClient, m.scope)
			})
		}
	}
}

func TestEvmSubmitRetryMethodFirstSendSucceeds(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newScenario(t)

			txHash, err := m.call(scenarioCtx(t), signClient)
			accepted, sends, _ := node.snapshot()
			if err != nil || len(sends) != 1 || len(accepted) != 1 || accepted[0].Hash().String() != txHash {
				t.Fatalf("expected one accepted send, got hash=%q err=%v; %s", txHash, err, node.describe())
			}
			if got := *m.nonce(signClient); got != scenarioNonce+1 {
				t.Fatalf("nonce cache = %d, want %d", got, scenarioNonce+1)
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

// Once a hash may be on chain the methods return it and never re-sign, even
// when the error text mentions a nonce.
func TestEvmSubmitRetryMethodKeepsAmbiguousPending(t *testing.T) {
	for _, m := range retryLoopMethods {
		for _, tc := range []struct {
			name   string
			action fakeEvmAction
		}{
			{"admit then drop", fakeEvmAction{kind: fakeEvmAdmitThenDrop}},
			{"-32002 timeout", fakeEvmAction{kind: fakeEvmRPCError, code: -32002, message: "request timed out"}},
			{"-32603 with nonce text", fakeEvmAction{kind: fakeEvmRPCError, code: -32603,
				message: "failed to broadcast transaction: invalid nonce; got 7, expected 8"}},
			{"http 503 with nonce body", fakeEvmAction{kind: fakeEvmHTTPStatus, status: 503,
				body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"invalid nonce; got 7, expected 8"}}`}},
		} {
			t.Run(m.name+"/"+tc.name, func(t *testing.T) {
				node, signClient := newScenario(t)
				node.script = scriptAll(tc.action)
				node.hideReceipts = true

				txHash, err := m.call(scenarioCtx(t), signClient)
				first := assertSignedOnce(t, node)
				if err == nil || txHash != first.hash.String() {
					t.Fatalf("expected original hash with an error, got hash=%q err=%v; %s", txHash, err, node.describe())
				}
				pending, ok := pendingForScope(t, signClient, m.scope)
				if !ok || !pending.ambiguous || pending.hash != first.hash || string(pending.raw) != string(first.raw) {
					t.Fatalf("expected ambiguous pending entry for the sent bytes, got hash=%s ambiguous=%t ok=%t",
						pending.hash.Hex(), pending.ambiguous, ok)
				}
			})
		}
	}
}

// An admitted send whose receipt is not found returns its hash; the operation
// stays pending and is not re-signed.
func TestEvmSubmitRetryMethodReceiptMissingIsNotResigned(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.hideReceipts = true

			txHash, err := m.call(scenarioCtx(t), signClient)
			first := assertSignedOnce(t, node)
			if _, sends, _ := node.snapshot(); len(sends) != 1 || err == nil || txHash != first.hash.String() {
				t.Fatalf("expected one send returning its hash with an error, got hash=%q err=%v; %s", txHash, err, node.describe())
			}
			if pending, ok := pendingForScope(t, signClient, m.scope); !ok || pending.hash != first.hash {
				t.Fatal("pending entry was cleared without a receipt")
			}
			if got := *m.nonce(signClient); got != scenarioNonce+1 {
				t.Fatalf("nonce cache = %d, want %d", got, scenarioNonce+1)
			}
		})
	}
}

func TestEvmSubmitRetryMethodNonceRefusalsExhaustRetries(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.script = scriptAll(fakeEvmAction{kind: fakeEvmRPCError, code: -32000,
				message: "failed to broadcast transaction: invalid nonce; got 7, expected 8: tx nonce is lower than account nonce"})

			txHash, err := m.call(scenarioCtx(t), signClient)
			if err == nil || txHash != "" {
				t.Fatalf("expected an error without hash, got hash=%q err=%v", txHash, err)
			}
			if !strings.Contains(err.Error(), "invalid nonce") {
				t.Fatalf("error does not carry the last refusal: %v", err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != BroadcastTxRetry {
				t.Fatalf("expected %d sends, got %d; %s", BroadcastTxRetry, len(sends), node.describe())
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

func TestEvmSubmitRetryMethodNonceRefreshFailureIsReturned(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newStaleNonceScenario(t, m, scenarioNonce-1)
			refreshErr := errors.New("nonce query unavailable")
			getNonceOnChainFn = func(context.Context, *MocaChainSignClient, *client.MocaClient) (uint64, error) {
				return 0, refreshErr
			}

			txHash, err := m.call(scenarioCtx(t), signClient)
			if err == nil || txHash != "" || !strings.Contains(err.Error(), refreshErr.Error()) {
				t.Fatalf("expected the refresh failure to be returned, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 1 {
				t.Fatalf("expected one send; %s", node.describe())
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

func TestEvmSubmitRetryMethodCancellationStopsRetry(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name+"/during refresh", func(t *testing.T) {
			node, signClient := newStaleNonceScenario(t, m, scenarioNonce-1)
			ctx, cancel := context.WithCancel(scenarioCtx(t))
			getNonceOnChainFn = func(ctx context.Context, _ *MocaChainSignClient, _ *client.MocaClient) (uint64, error) {
				cancel()
				return 0, ctx.Err()
			}

			txHash, err := m.call(ctx, signClient)
			if err == nil || txHash != "" || !strings.Contains(err.Error(), context.Canceled.Error()) {
				t.Fatalf("expected cancellation, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 1 {
				t.Fatalf("expected one send; %s", node.describe())
			}
		})
		t.Run(m.name+"/before send", func(t *testing.T) {
			node, signClient := newScenario(t)
			ctx, cancel := context.WithCancel(scenarioCtx(t))
			cancel()

			if txHash, err := m.call(ctx, signClient); err == nil || txHash != "" {
				t.Fatalf("expected cancellation, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 0 {
				t.Fatalf("a transaction was sent; %s", node.describe())
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

func TestEvmSubmitRetryMethodVerifiedRefusalIsNotResigned(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.script = scriptFirst(fakeEvmAction{kind: fakeEvmRPCError, code: -32000,
				message: "failed to broadcast transaction: insufficient funds"})

			txHash, err := m.call(scenarioCtx(t), signClient)
			if err == nil || txHash != "" || !strings.Contains(err.Error(), "insufficient funds") {
				t.Fatalf("expected the refusal without hash, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 1 {
				t.Fatalf("refused transaction was re-signed or re-sent; %s", node.describe())
			}
			if got := *m.nonce(signClient); got != scenarioNonce {
				t.Fatalf("nonce cache = %d, want unchanged %d", got, scenarioNonce)
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

func TestEvmSubmitRetryMethodLeaseLostStopsRetry(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newStaleNonceScenario(t, m, scenarioNonce-1)
			getNonceOnChainFn = func(context.Context, *MocaChainSignClient, *client.MocaClient) (uint64, error) {
				fence, _ := signClient.evm.lease.fence()
				signClient.evm.lease.lose(context.Background(), fence, "test lease handoff")
				_, _, nonce := node.snapshot()
				return nonce, nil
			}

			txHash, err := m.call(scenarioCtx(t), signClient)
			if err == nil || txHash != "" || !strings.Contains(err.Error(), errEvmLeaseNotHeld.Error()) {
				t.Fatalf("expected the lease error, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 1 {
				t.Fatalf("sent after the lease was lost; %s", node.describe())
			}
			assertNoEvmState(t, signClient, m.scope)
		})
	}
}

// A nonce refusal that cannot be recorded leaves the refused bytes pending;
// the method must not refresh the nonce and re-sign.
func TestEvmSubmitRetryMethodUnrecordedRefusalIsNotResigned(t *testing.T) {
	for _, m := range retryLoopMethods {
		t.Run(m.name, func(t *testing.T) {
			node, signClient := newStaleNonceScenario(t, m, scenarioNonce-1)
			memStoreOf(t, signClient).inject("resolve", memFaultFail)
			refreshed := false
			getNonceOnChainFn = func(context.Context, *MocaChainSignClient, *client.MocaClient) (uint64, error) {
				refreshed = true
				return scenarioNonce, nil
			}

			txHash, err := m.call(scenarioCtx(t), signClient)
			if err == nil || txHash != "" || !strings.Contains(err.Error(), "could not be recorded") {
				t.Fatalf("expected the record failure, got hash=%q err=%v", txHash, err)
			}
			_, sends, _ := node.snapshot()
			if len(sends) != 1 || refreshed {
				t.Fatalf("re-signed after an unrecorded refusal (refreshed=%t); %s", refreshed, node.describe())
			}
			if pending, ok := pendingForScope(t, signClient, m.scope); !ok || pending.hash != sends[0].hash {
				t.Fatal("refused bytes are no longer pending")
			}
		})
	}
}

// A local signing failure (here abigen's gas estimation hitting a node that
// does not serve eth_getCode) must surface as the returned reason instead of
// being lost behind the retry loop.
func TestEvmLocalSignErrorIsReturned(t *testing.T) {
	deposit := sdk.NewCoin("amoca", sdkmath.NewInt(1))
	for _, tc := range []struct {
		name    string
		gasType GasInfoType
		call    func(context.Context, *MocaChainSignClient) (string, error)
	}{
		{"SealObjectEvm", Seal, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.SealObjectEvm(ctx, SignSeal, &storagetypes.MsgSealObject{BucketName: "bucket", ObjectName: "object"})
		}},
		{"RejectUnSealObjectEvm", RejectSeal, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.RejectUnSealObjectEvm(ctx, SignSeal, &storagetypes.MsgRejectSealObject{BucketName: "bucket", ObjectName: "object"})
		}},
		{"CreateGlobalVirtualGroupEvm", CreateGlobalVirtualGroup, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.CreateGlobalVirtualGroupEvm(ctx, SignOperator, &virtualgrouptypes.MsgCreateGlobalVirtualGroup{FamilyId: 1, Deposit: deposit})
		}},
		{"CompleteMigrateBucketEvm", CompleteMigrateBucket, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.CompleteMigrateBucketEvm(ctx, SignOperator, &storagetypes.MsgCompleteMigrateBucket{BucketName: "bucket"})
		}},
		{"SwapOutEvm", SwapOut, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.SwapOutEvm(ctx, SignOperator, &virtualgrouptypes.MsgSwapOut{GlobalVirtualGroupFamilyId: 1})
		}},
		{"CompleteSwapOutEvm", CompleteSwapOut, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.CompleteSwapOutEvm(ctx, SignOperator, &virtualgrouptypes.MsgCompleteSwapOut{GlobalVirtualGroupFamilyId: 1})
		}},
		{"DiscontinueBucketEvm", DiscontinueBucket, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.DiscontinueBucketEvm(ctx, SignGc, &storagetypes.MsgDiscontinueBucket{BucketName: "bucket", Reason: "test"})
		}},
		{"UpdateSPPriceEvm", UpdateSPPrice, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.UpdateSPPriceEvm(ctx, SignOperator, &sptypes.MsgUpdateSpStoragePrice{
				ReadPrice: sdkmath.LegacyNewDec(1), FreeReadQuota: 1, StorePrice: sdkmath.LegacyNewDec(1),
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			signClient.gasInfo[tc.gasType] = GasInfo{GasLimit: 0}

			txHash, err := tc.call(scenarioCtx(t), signClient)
			if txHash != "" || err == nil || !strings.Contains(err.Error(), "eth_getCode") {
				t.Fatalf("expected the local signing error to be returned, got hash=%q err=%v", txHash, err)
			}
			if _, sends, _ := node.snapshot(); len(sends) != 0 {
				t.Fatalf("a transaction was sent; %s", node.describe())
			}
		})
	}
}
