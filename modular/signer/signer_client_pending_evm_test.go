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
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/sdk/client"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

func TestResolvePendingEvmTxLookupErrorKeepsExactHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	pendingHash := ethcmn.HexToHash("0x899")
	operation := ethcmn.HexToHash("0x01")
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pendingHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pendingHash.Hex())
		}
		return nil, errors.New("receipt unavailable")
	}

	signerClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignOperator: nil},
		pendingEvmTxs: map[SignType]pendingEvmTx{
			SignOperator: {operation: operation, nonce: 17, hash: pendingHash},
		},
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if !handled {
		t.Fatal("unconfirmed transaction must block a new submission")
	}
	if txHash != pendingHash.String() {
		t.Fatalf("expected submitted hash %s, got %s", pendingHash.String(), txHash)
	}
	if err == nil || !strings.Contains(err.Error(), "submitted but unconfirmed") ||
		!strings.Contains(err.Error(), "nonce=17") || !strings.Contains(err.Error(), pendingHash.Hex()) {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if _, ok := signerClient.pendingEvmTxs[SignOperator]; !ok {
		t.Fatal("unconfirmed transaction was removed from pending state")
	}
}

func TestResolvePendingEvmTxNilReceiptKeepsExactHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	pendingHash := ethcmn.HexToHash("0x899")
	operation := ethcmn.HexToHash("0x01")
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pendingHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pendingHash.Hex())
		}
		return nil, nil
	}

	signerClient := &MocaChainSignClient{
		pendingEvmTxs: map[SignType]pendingEvmTx{
			SignOperator: {operation: operation, nonce: 17, hash: pendingHash},
		},
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if !handled {
		t.Fatal("nil receipt must block a new submission")
	}
	if txHash != pendingHash.String() {
		t.Fatalf("expected submitted hash %s, got %s", pendingHash.String(), txHash)
	}
	if err == nil || !strings.Contains(err.Error(), "submitted but unconfirmed") {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if _, ok := signerClient.pendingEvmTxs[SignOperator]; !ok {
		t.Fatal("transaction with nil receipt was removed from pending state")
	}
}

func TestResolvePendingEvmTxFailedReceiptClearsPendingAndPermitsRetry(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	pendingHash := ethcmn.HexToHash("0x899")
	operation := ethcmn.HexToHash("0x01")
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if hash != pendingHash {
			t.Fatalf("looked up %s instead of exact pending hash %s", hash.Hex(), pendingHash.Hex())
		}
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusFailed, BlockNumber: big.NewInt(10)}, nil
	}

	signerClient := &MocaChainSignClient{
		pendingEvmTxs: map[SignType]pendingEvmTx{
			SignOperator: {operation: operation, nonce: 17, hash: pendingHash},
		},
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
	if _, ok := signerClient.pendingEvmTxs[SignOperator]; ok {
		t.Fatal("failed transaction remained pending")
	}
}

func TestResolvePendingEvmTxMatchingConfirmedOperationReturnsOriginalHash(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	pendingHash := ethcmn.HexToHash("0x899")
	operation := ethcmn.HexToHash("0x01")
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, _ ethcmn.Hash) (*ethtypes.Receipt, error) {
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful, BlockNumber: big.NewInt(10)}, nil
	}

	signerClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignOperator: nil},
		pendingEvmTxs: map[SignType]pendingEvmTx{
			SignOperator: {operation: operation, nonce: 17, hash: pendingHash},
		},
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, operation)
	if err != nil {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if !handled {
		t.Fatal("matching confirmed operation must not be resubmitted")
	}
	if txHash != pendingHash.String() {
		t.Fatalf("expected original hash %s, got %s", pendingHash.String(), txHash)
	}
	if _, ok := signerClient.pendingEvmTxs[SignOperator]; ok {
		t.Fatal("confirmed transaction remained pending")
	}
}

func TestResolvePendingEvmTxDifferentConfirmedOperationPermitsSubmission(t *testing.T) {
	originalReceipt := transactionReceiptFn
	defer func() { transactionReceiptFn = originalReceipt }()

	pendingHash := ethcmn.HexToHash("0x899")
	transactionReceiptFn = func(_ context.Context, _ *ethclient.Client, _ ethcmn.Hash) (*ethtypes.Receipt, error) {
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful, BlockNumber: big.NewInt(10)}, nil
	}

	signerClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignOperator: nil},
		pendingEvmTxs: map[SignType]pendingEvmTx{
			SignOperator: {operation: ethcmn.HexToHash("0x01"), nonce: 17, hash: pendingHash},
		},
	}

	txHash, handled, err := signerClient.resolvePendingEvmTx(context.Background(), SignOperator, ethcmn.HexToHash("0x02"))
	if err != nil {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if handled {
		t.Fatalf("different operation was incorrectly handled as prior success with hash %s", txHash)
	}
	if txHash != "" {
		t.Fatalf("different operation received prior hash %s", txHash)
	}
	if _, ok := signerClient.pendingEvmTxs[SignOperator]; ok {
		t.Fatal("confirmed prior transaction remained pending")
	}
}

func TestRecordPendingEvmTxClearsOnlyExactHash(t *testing.T) {
	signerClient := &MocaChainSignClient{}
	operation := ethcmn.HexToHash("0x01")
	pendingHash := ethcmn.HexToHash("0x899")

	signerClient.recordPendingEvmTx(SignSeal, operation, 23, pendingHash)
	pending, ok := signerClient.pendingEvmTxs[SignSeal]
	if !ok || pending.operation != operation || pending.nonce != 23 || pending.hash != pendingHash {
		t.Fatalf("unexpected pending transaction: %+v", pending)
	}

	signerClient.clearPendingEvmTx(SignSeal, ethcmn.HexToHash("0x900"))
	if _, ok := signerClient.pendingEvmTxs[SignSeal]; !ok {
		t.Fatal("a different hash cleared pending state")
	}

	signerClient.clearPendingEvmTx(SignSeal, pendingHash)
	if _, ok := signerClient.pendingEvmTxs[SignSeal]; ok {
		t.Fatal("exact confirmed hash remained pending")
	}
}

func TestPendingEvmTxStateIsSafeAcrossSigningAccounts(t *testing.T) {
	signerClient := &MocaChainSignClient{}

	var wg sync.WaitGroup
	for _, scope := range []SignType{SignOperator, SignSeal, SignGc} {
		scope := scope
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				hash := ethcmn.BigToHash(big.NewInt(int64(i + 1)))
				signerClient.recordPendingEvmTx(scope, hash, uint64(i), hash)
				signerClient.clearPendingEvmTx(scope, hash)
			}
		}()
	}
	wg.Wait()
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

func TestAllEvmSubmissionMethodsTrackPendingTransactions(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "signer_client.go", nil, 0)
	if err != nil {
		t.Fatalf("parse signer client: %v", err)
	}

	var missing []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || !strings.HasSuffix(function.Name.Name, "Evm") {
			continue
		}

		calls := map[string]token.Pos{}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch function := call.Fun.(type) {
			case *ast.Ident:
				calls[function.Name] = call.Pos()
			case *ast.SelectorExpr:
				selector := function
				calls[selector.Sel.Name] = call.Pos()
			}
			return true
		})

		fingerprintPos := calls["evmOperationFingerprint"]
		if function.Name.Name == "CompleteSPExitEvm" {
			fingerprintPos = calls["completeSPExitEvmFingerprint"]
		}
		resolvePos := calls["resolvePendingEvmTx"]
		recordPos := calls["recordPendingEvmTx"]
		clearPos := calls["clearPendingEvmTx"]
		if fingerprintPos == token.NoPos || resolvePos == token.NoPos || recordPos == token.NoPos || clearPos == token.NoPos ||
			fingerprintPos >= resolvePos || resolvePos >= recordPos || recordPos >= clearPos {
			missing = append(missing, function.Name.Name)
		}
	}

	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("EVM submission methods missing ordered fingerprint/recovery/record/clear calls: %s", strings.Join(missing, ", "))
	}
}
