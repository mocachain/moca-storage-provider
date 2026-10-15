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

func pendingFor(t *testing.T, signClient *MocaChainSignClient) (pendingEvmTx, bool) {
	t.Helper()
	signClient.pendingEvmTxMu.Lock()
	defer signClient.pendingEvmTxMu.Unlock()
	pending, ok := signClient.pendingEvmTxs[SignOperator]
	return pending, ok
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

// Scenario 7, known limit: pending state is in memory only. A fresh client
// (restart) re-submits at the refreshed nonce. This pins current behaviour;
// it is not a guarantee.
func TestEvmSubmitKnownLimitPendingLostOnRestart(t *testing.T) {
	node, signClient := newScenario(t)
	node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
	node.hideReceipts = true
	ctx := scenarioCtx(t)

	if _, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000)); err == nil {
		t.Fatal("expected unconfirmed error")
	}
	node.setHideReceipts(false)

	_, _, chainNonce := node.snapshot()
	restarted := newFakeEvmSignClient(t, node, chainNonce)
	if _, err := restarted.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	accepted, _, _ := node.snapshot()
	if len(accepted) != 2 || accepted[1].Nonce() != scenarioNonce+1 {
		t.Fatalf("known limit changed: expected a second deposit at nonce %d after restart; %s", scenarioNonce+1, node.describe())
	}
}

// Scenario 8 (all 19 methods use the helper) is enforced by
// TestAllEvmSubmissionMethodsTrackPendingTransactions; this drives both
// methods without a retry loop through the same ambiguous path.
func TestEvmSubmitNoLoopMethodKeepsAmbiguousPending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope SignType
		call  func(context.Context, *MocaChainSignClient) (string, error)
	}{
		{"DiscontinueBucketEvm", SignGc, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.DiscontinueBucketEvm(ctx, SignGc, &storagetypes.MsgDiscontinueBucket{BucketName: "bucket", Reason: "test"})
		}},
		{"UpdateSPPriceEvm", SignOperator, func(ctx context.Context, c *MocaChainSignClient) (string, error) {
			return c.UpdateSPPriceEvm(ctx, SignOperator, &sptypes.MsgUpdateSpStoragePrice{
				ReadPrice: sdkmath.LegacyNewDec(1), FreeReadQuota: 1, StorePrice: sdkmath.LegacyNewDec(1),
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, signClient := newScenario(t)
			node.script = scriptFirst(fakeEvmAction{kind: fakeEvmAdmitThenDrop})
			node.hideReceipts = true

			txHash, err := tc.call(scenarioCtx(t), signClient)
			_, sends, _ := node.snapshot()
			if err == nil || len(sends) == 0 || txHash != sends[0].hash.String() {
				t.Fatalf("expected original hash with an error, got hash=%q err=%v; %s", txHash, err, node.describe())
			}
			signClient.pendingEvmTxMu.Lock()
			pending, ok := signClient.pendingEvmTxs[tc.scope]
			signClient.pendingEvmTxMu.Unlock()
			if !ok || !pending.ambiguous || pending.hash != sends[0].hash || string(pending.raw) != string(sends[0].raw) {
				t.Fatalf("expected ambiguous pending entry for the sent bytes, got hash=%s ambiguous=%t ok=%t",
					pending.hash.Hex(), pending.ambiguous, ok)
			}
			for _, send := range sends {
				if string(send.raw) != string(sends[0].raw) {
					t.Fatalf("no-loop method re-signed; %s", node.describe())
				}
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
