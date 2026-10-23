package signer

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// signFakeTx signs a plain transfer with the fake key at nonce.
func signFakeTx(t *testing.T, nonce uint64, gasPrice int64) *ethtypes.Transaction {
	t.Helper()
	key, err := crypto.HexToECDSA(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	to := ethcmn.HexToAddress("0x00000000000000000000000000000000000000aa")
	tx, err := ethtypes.SignNewTx(key, ethtypes.LatestSignerForChainID(big.NewInt(5151)), &ethtypes.LegacyTx{
		Nonce: nonce, GasPrice: big.NewInt(gasPrice), Gas: 21000, To: &to, Value: big.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// seedPending records tx as the scope's pending transaction for operation.
func seedPending(t *testing.T, signClient *MocaChainSignClient, scope SignType, operation ethcmn.Hash,
	tx *ethtypes.Transaction,
) pendingRecord {
	t.Helper()
	original := blockNumberFn
	defer func() { blockNumberFn = original }()
	blockNumberFn = func(context.Context, *ethclient.Client) (uint64, error) { return 100, nil }
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	fence, _ := signClient.evm.lease.fence()
	pending, err := signClient.recordPendingEvmTx(context.Background(), signClient.evm, fence, scope, operation, tx, raw)
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	return pending
}

func newPendingUnitClient(t *testing.T) *MocaChainSignClient {
	t.Helper()
	node := newFakeEvmNode(t, 17)
	installFakeEvmSeams(t, node)
	return newFakeEvmSignClient(t, node, 17)
}

func TestResolvePendingEvmTxLookupErrorKeepsExactHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	signerClient := newPendingUnitClient(t)
	operation := ethcmn.HexToHash("0x01")
	pending := seedPending(t, signerClient, SignOperator, operation, signFakeTx(t, 17, 1))
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pending.TxHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pending.TxHash.Hex())
		}
		return nil, errors.New("receipt unavailable")
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if !handled {
		t.Fatal("unconfirmed transaction must block a new submission")
	}
	if txHash != pending.TxHash.String() {
		t.Fatalf("expected submitted hash %s, got %s", pending.TxHash.String(), txHash)
	}
	if err == nil || !strings.Contains(err.Error(), "submitted but unconfirmed") ||
		!strings.Contains(err.Error(), "nonce=17") || !strings.Contains(err.Error(), pending.TxHash.Hex()) {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if _, ok := memStoreOf(t, signerClient).pendingOf(SignOperator); !ok {
		t.Fatal("unconfirmed transaction was removed from pending state")
	}
}

func TestResolvePendingEvmTxNilReceiptKeepsExactHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	signerClient := newPendingUnitClient(t)
	operation := ethcmn.HexToHash("0x01")
	pending := seedPending(t, signerClient, SignOperator, operation, signFakeTx(t, 17, 1))
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pending.TxHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pending.TxHash.Hex())
		}
		return nil, nil
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if !handled {
		t.Fatal("nil receipt must block a new submission")
	}
	if txHash != pending.TxHash.String() {
		t.Fatalf("expected submitted hash %s, got %s", pending.TxHash.String(), txHash)
	}
	if err == nil || !strings.Contains(err.Error(), "submitted but unconfirmed") {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if _, ok := memStoreOf(t, signerClient).pendingOf(SignOperator); !ok {
		t.Fatal("transaction with nil receipt was removed from pending state")
	}
}

func TestResolvePendingEvmTxFailedReceiptClearsPendingAndPermitsRetry(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	signerClient := newPendingUnitClient(t)
	operation := ethcmn.HexToHash("0x01")
	pending := seedPending(t, signerClient, SignOperator, operation, signFakeTx(t, 17, 1))
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pending.TxHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pending.TxHash.Hex())
		}
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusFailed, BlockNumber: big.NewInt(10)}, nil
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if err != nil {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if handled {
		t.Fatalf("failed transaction was incorrectly handled as prior success with hash %s", txHash)
	}
	if txHash != "" {
		t.Fatalf("failed transaction returned prior hash %s", txHash)
	}
	store := memStoreOf(t, signerClient)
	if _, ok := store.pendingOf(SignOperator); ok {
		t.Fatal("failed transaction remained pending")
	}
	if len(store.outcomeRows()) != 0 || len(store.auditEvents(evmAuditResolvedFailed)) != 1 {
		t.Fatalf("failed receipt must only be audited: outcomes=%v", store.outcomeRows())
	}
}

func TestResolvePendingEvmTxMatchingConfirmedOperationReturnsOriginalHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	signerClient := newPendingUnitClient(t)
	operation := ethcmn.HexToHash("0x01")
	pending := seedPending(t, signerClient, SignOperator, operation, signFakeTx(t, 17, 1))
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, _ ethcmn.Hash) (*ethtypes.Receipt, error) {
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful, BlockNumber: big.NewInt(10)}, nil
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if err != nil {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if !handled {
		t.Fatal("matching confirmed operation must not be resubmitted")
	}
	if txHash != pending.TxHash.String() {
		t.Fatalf("expected original hash %s, got %s", pending.TxHash.String(), txHash)
	}
	store := memStoreOf(t, signerClient)
	if _, ok := store.pendingOf(SignOperator); ok {
		t.Fatal("confirmed transaction remained pending")
	}
	if len(store.outcomeRows()) != 0 || len(store.auditEvents(evmAuditOutcomeConsumed)) != 1 {
		t.Fatalf("direct consume must not keep an outcome: %v", store.outcomeRows())
	}
}

// F1: a success seen by a request for another operation is kept as a
// succeeded outcome for the original operation instead of being dropped.
func TestResolvePendingEvmTxDifferentConfirmedOperationKeepsOutcome(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	signerClient := newPendingUnitClient(t)
	first := ethcmn.HexToHash("0x01")
	pending := seedPending(t, signerClient, SignOperator, first, signFakeTx(t, 17, 1))
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, _ ethcmn.Hash) (*ethtypes.Receipt, error) {
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful, BlockNumber: big.NewInt(10)}, nil
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, ethcmn.HexToHash("0x02"))
	if err != nil || handled || txHash != "" {
		t.Fatalf("different operation must be allowed to sign: hash=%q handled=%t err=%v", txHash, handled, err)
	}
	store := memStoreOf(t, signerClient)
	outcomes := store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].State != evmOutcomeSucceeded || outcomes[0].Operation != first ||
		outcomes[0].ResultTxHash != pending.TxHash || outcomes[0].SignedTxID != pending.SignedTxID {
		t.Fatalf("expected a succeeded outcome for the original operation, got %+v", outcomes)
	}

	txHash, handled, err = signerClient.resolvePendingEvmTx(context.Background(), SignOperator, first)
	if err != nil || !handled || txHash != pending.TxHash.String() {
		t.Fatalf("original operation must get its hash: hash=%q handled=%t err=%v", txHash, handled, err)
	}
	if len(store.outcomeRows()) != 0 {
		t.Fatal("consumed outcome remained active")
	}
}

func TestEvmStateTransitionsTouchOnlyExactHash(t *testing.T) {
	signerClient := newPendingUnitClient(t)
	store := memStoreOf(t, signerClient)
	pending := seedPending(t, signerClient, SignSeal, ethcmn.HexToHash("0x01"), signFakeTx(t, 23, 1))
	fence, _ := signerClient.evm.lease.fence()

	other := pending
	other.TxHash = ethcmn.HexToHash("0x900")
	if _, err := store.ResolvePending(context.Background(), fence, other, pendingTerminal{Kind: terminalFailed}); !errors.Is(err, errEvmStateConflict) {
		t.Fatalf("a different hash must conflict, got %v", err)
	}
	if err := store.MarkAmbiguous(context.Background(), fence, other); !errors.Is(err, errEvmStateConflict) {
		t.Fatalf("a different hash must conflict, got %v", err)
	}
	if _, ok := store.pendingOf(SignSeal); !ok {
		t.Fatal("a different hash cleared pending state")
	}
	if _, err := store.ResolvePending(context.Background(), fence, pending, pendingTerminal{Kind: terminalFailed}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolvePending(context.Background(), fence, pending, pendingTerminal{Kind: terminalFailed}); !errors.Is(err, errEvmRowGone) {
		t.Fatalf("a second delete must report the row gone, got %v", err)
	}
}

func TestPendingEvmTxStateIsSafeAcrossSigningAccounts(t *testing.T) {
	signerClient := newPendingUnitClient(t)
	store := memStoreOf(t, signerClient)
	fence, _ := signerClient.evm.lease.fence()
	original := blockNumberFn
	defer func() { blockNumberFn = original }()
	blockNumberFn = func(context.Context, *ethclient.Client) (uint64, error) { return 100, nil }

	var wg sync.WaitGroup
	for i, scope := range []SignType{SignOperator, SignSeal, SignGc} {
		scope, base := scope, uint64(i*1000)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := uint64(0); n < 50; n++ {
				tx := signFakeTx(t, base+n, 1)
				raw, _ := tx.MarshalBinary()
				pending, err := signerClient.recordPendingEvmTx(context.Background(), signerClient.evm, fence, scope,
					ethcmn.BigToHash(big.NewInt(int64(base+n+1))), tx, raw)
				if err != nil {
					t.Error(err)
					return
				}
				signerClient.markPendingEvmTxAmbiguous(context.Background(), signerClient.evm, fence, pending)
				if _, err := store.ResolvePending(context.Background(), fence, pending, pendingTerminal{Kind: terminalFailed}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if rows := store.pendingRows(); len(rows) != 0 {
		t.Fatalf("pending rows left: %v", rows)
	}
}

func TestCompleteSPExitFingerprintIgnoresUnsubmittedStorageProvider(t *testing.T) {
	first, err := completeSPExitEvmFingerprint(&virtualgrouptypes.MsgCompleteStorageProviderExit{
		StorageProvider: "first",
		Operator:        "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := completeSPExitEvmFingerprint(&virtualgrouptypes.MsgCompleteStorageProviderExit{
		StorageProvider: "second",
		Operator:        "operator",
	})
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Fatalf("identical CompleteSPExit calldata produced different fingerprints: %s != %s", first, second)
	}
}

func evmCallPositions(body ast.Node) map[string]token.Pos {
	calls := map[string]token.Pos{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if _, seen := calls[name]; name != "" && !seen {
			calls[name] = call.Pos()
		}
		return true
	})
	return calls
}

func TestAllEvmSubmissionMethodsTrackPendingTransactions(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "signer_client.go", nil, 0)
	if err != nil {
		t.Fatalf("parse signer client: %v", err)
	}

	var (
		missing []string
		methods int
		helper  *ast.FuncDecl
	)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil {
			continue
		}
		if function.Name.Name == "submitEvmTx" {
			helper = function
		}
		if !strings.HasSuffix(function.Name.Name, "Evm") {
			continue
		}
		methods++

		calls := evmCallPositions(function.Body)
		fingerprintPos := calls["evmOperationFingerprint"]
		if function.Name.Name == "CompleteSPExitEvm" {
			fingerprintPos = calls["completeSPExitEvmFingerprint"]
		}
		resolvePos := calls["resolvePendingEvmTx"]
		submitPos := calls["submitEvmTx"]

		noSendPos, noSendOpts := token.NoPos, ""
		ast.Inspect(function.Body, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			selector, ok := assign.Lhs[0].(*ast.SelectorExpr)
			value, isIdent := assign.Rhs[0].(*ast.Ident)
			if ok && selector.Sel.Name == "NoSend" && isIdent && value.Name == "true" {
				noSendPos = assign.Pos()
				if opts, ok := selector.X.(*ast.Ident); ok {
					noSendOpts = opts.Name
				}
			}
			return true
		})
		// Sessions copy the TransactOpts by value, so NoSend must be set before
		// the session is created, not merely before the session method call.
		sessionCreatePos, sessionCallPos, sessionOpts := token.NoPos, token.NoPos, ""
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(ident.Name, "Create") &&
				strings.HasSuffix(ident.Name, "Session") && sessionCreatePos == token.NoPos {
				sessionCreatePos = call.Pos()
				if len(call.Args) > 1 {
					if star, ok := call.Args[1].(*ast.StarExpr); ok {
						if opts, ok := star.X.(*ast.Ident); ok {
							sessionOpts = opts.Name
						}
					}
				}
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "session" && sessionCallPos == token.NoPos {
					sessionCallPos = call.Pos()
				}
			}
			return true
		})

		if fingerprintPos == token.NoPos || resolvePos == token.NoPos || noSendPos == token.NoPos ||
			sessionCreatePos == token.NoPos || sessionCallPos == token.NoPos || submitPos == token.NoPos ||
			noSendOpts == "" || noSendOpts != sessionOpts ||
			fingerprintPos >= resolvePos || resolvePos >= noSendPos || noSendPos >= sessionCreatePos ||
			sessionCreatePos >= sessionCallPos || sessionCallPos >= submitPos ||
			calls["recordPendingEvmTx"] != token.NoPos || calls["SendTransaction"] != token.NoPos {
			missing = append(missing, function.Name.Name)
		}
	}

	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("EVM submission methods missing ordered fingerprint/recovery/NoSend/session/sign/submitEvmTx: %s", strings.Join(missing, ", "))
	}
	if methods != 19 {
		t.Fatalf("expected 19 EVM submission methods, found %d", methods)
	}

	if helper == nil {
		t.Fatal("submitEvmTx not found")
	}
	var ctxCheckPos token.Pos
	ast.Inspect(helper.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Err" {
			if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "ctx" && ctxCheckPos == token.NoPos {
				ctxCheckPos = call.Pos()
			}
		}
		return true
	})
	helperCalls := evmCallPositions(helper.Body)
	recordPos, sendPos := helperCalls["recordPendingEvmTx"], helperCalls["sendEvmTxFn"]
	if ctxCheckPos == token.NoPos || recordPos == token.NoPos || sendPos == token.NoPos ||
		ctxCheckPos >= recordPos || recordPos >= sendPos {
		t.Fatalf("submitEvmTx must check ctx, then record pending, then send: ctx=%v record=%v send=%v",
			ctxCheckPos, recordPos, sendPos)
	}
}
