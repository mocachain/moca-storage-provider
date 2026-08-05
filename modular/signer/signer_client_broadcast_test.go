package signer

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkErrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/mocachain/moca/v2/sdk/client"
	ctypes "github.com/mocachain/moca/v2/sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestCosmosSequenceRetryCallSites(t *testing.T) {
	expectedCaches := map[string]string{
		"SealObject":                  "sealAccNonce",
		"RejectUnSealObject":          "sealAccNonce",
		"CreateGlobalVirtualGroup":    "operatorAccNonce",
		"CompleteMigrateBucket":       "operatorAccNonce",
		"SwapOut":                     "operatorAccNonce",
		"CompleteSwapOut":             "operatorAccNonce",
		"SPExit":                      "operatorAccNonce",
		"CompleteSPExit":              "operatorAccNonce",
		"RejectMigrateBucket":         "operatorAccNonce",
		"Deposit":                     "operatorAccNonce",
		"DeleteGlobalVirtualGroup":    "operatorAccNonce",
		"DelegateCreateObject":        "operatorAccNonce",
		"DelegateUpdateObjectContent": "operatorAccNonce",
		"ReserveSwapIn":               "operatorAccNonce",
		"CompleteSwapIn":              "operatorAccNonce",
		"CancelSwapIn":                "operatorAccNonce",
		"SealObjectV2":                "sealAccNonce",
	}
	allowedLegacy := map[string]bool{
		"DiscontinueBucket": true,
		"UpdateSPPrice":     true,
	}

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	sourcePath := filepath.Join(filepath.Dir(currentFile), "signer_client.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), sourcePath, nil, 0)
	require.NoError(t, err)

	seen := make(map[string]bool)
	var legacyCallers []string
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "broadcastTx":
				if !allowedLegacy[function.Name.Name] {
					legacyCallers = append(legacyCallers, function.Name.Name)
				}
			case "broadcastTxWithSequenceRetry":
				expectedCache, expected := expectedCaches[function.Name.Name]
				require.Truef(t, expected, "unexpected retry helper call in %s", function.Name.Name)
				require.Len(t, call.Args, 5, "retry helper call in %s", function.Name.Name)
				cachePointer, ok := call.Args[4].(*ast.UnaryExpr)
				require.Truef(t, ok && cachePointer.Op == token.AND, "retry helper cache in %s must be a pointer", function.Name.Name)
				cache, ok := cachePointer.X.(*ast.SelectorExpr)
				require.Truef(t, ok, "retry helper cache in %s must select a client field", function.Name.Name)
				require.Equal(t, expectedCache, cache.Sel.Name, "wrong retry helper cache in %s", function.Name.Name)
				seen[function.Name.Name] = true
			}
			return true
		})
	}

	sort.Strings(legacyCallers)
	require.Empty(t, legacyCallers, "Cosmos callers still using legacy broadcastTx: %v", legacyCallers)
	for function := range expectedCaches {
		require.Truef(t, seen[function], "%s does not use broadcastTxWithSequenceRetry", function)
	}
}

func TestBroadcastTxWithSequenceRetryUsesCachedNonceOnFirstAttempt(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceCache := uint64(9)
	nonceQueries := 0
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		nonceQueries++
		return 0, nil
	}
	var attemptedNonces []uint64
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		attemptedNonces = append(attemptedNonces, txOpt.Nonce)
		return successfulBroadcastResponse("ABC123"), nil
	}

	hash, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
		context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
	)

	require.NoError(t, err)
	require.Equal(t, "ABC123", hash)
	require.Equal(t, uint64(9), usedNonce)
	require.Equal(t, []uint64{9}, attemptedNonces)
	require.Zero(t, nonceQueries)
}

func TestBroadcastTxUsesProvidedNonceWithoutQuery(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceQueries := 0
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		nonceQueries++
		return 17, nil
	}
	var broadcastNonce uint64
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastNonce = txOpt.Nonce
		return successfulBroadcastResponse("ABC123"), nil
	}

	hash, nonce, err := (&MocaChainSignClient{}).broadcastTx(
		context.Background(), nil, nil, &ctypes.TxOption{Nonce: 9},
	)

	require.NoError(t, err)
	require.Equal(t, "ABC123", hash)
	require.Equal(t, uint64(9), nonce)
	require.Equal(t, uint64(9), broadcastNonce)
	require.Zero(t, nonceQueries)
}

func TestBroadcastTxWithSequenceRetryRefreshesAfterMismatch(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceCache := uint64(7)
	nonceQueries := 0
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		nonceQueries++
		return 11, nil
	}
	var attemptedNonces []uint64
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		attemptedNonces = append(attemptedNonces, txOpt.Nonce)
		if len(attemptedNonces) == 1 {
			return wrongSequenceBroadcastResponse(), nil
		}
		return successfulBroadcastResponse("REFRESHED"), nil
	}

	hash, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
		context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
	)

	require.NoError(t, err)
	require.Equal(t, "REFRESHED", hash)
	require.Equal(t, uint64(11), usedNonce)
	require.Equal(t, uint64(11), nonceCache)
	require.Equal(t, []uint64{7, 11}, attemptedNonces)
	require.Equal(t, 1, nonceQueries)
}

func TestBroadcastTxWithSequenceRetryStopsOnNonSequenceError(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceCache := uint64(5)
	nonceQueries := 0
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		nonceQueries++
		return 0, nil
	}
	broadcastCalls := 0
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastCalls++
		return nil, fmt.Errorf("transport unavailable")
	}

	_, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
		context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
	)

	require.ErrorContains(t, err, "transport unavailable")
	require.Equal(t, uint64(5), usedNonce)
	require.Equal(t, 1, broadcastCalls)
	require.Zero(t, nonceQueries)
}

func TestBroadcastTxWithSequenceRetryStopsWhenRefreshFails(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceCache := uint64(3)
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		return 0, fmt.Errorf("nonce unavailable")
	}
	broadcastCalls := 0
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastCalls++
		return wrongSequenceBroadcastResponse(), nil
	}

	_, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
		context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
	)

	require.ErrorContains(t, err, "failed to get nonce on chain")
	require.ErrorContains(t, err, "nonce unavailable")
	require.Equal(t, uint64(3), usedNonce)
	require.Equal(t, 1, broadcastCalls)
}

func TestBroadcastTxWithSequenceRetryStopsAtRetryLimit(t *testing.T) {
	restoreBroadcastRetrySeams(t)
	nonceCache := uint64(1)
	nonceQueries := 0
	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		nonceQueries++
		return uint64(nonceQueries + 1), nil
	}
	broadcastCalls := 0
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastCalls++
		return wrongSequenceBroadcastResponse(), nil
	}

	_, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
		context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
	)

	require.Equal(t, sdkErrors.ErrWrongSequence, err)
	require.Equal(t, uint64(BroadcastTxRetry), usedNonce)
	require.Equal(t, BroadcastTxRetry, broadcastCalls)
	require.Equal(t, BroadcastTxRetry-1, nonceQueries)
}

func restoreBroadcastRetrySeams(t *testing.T) {
	t.Helper()
	originalGetNonce := getCosmosNonceFn
	originalBroadcast := broadcastCosmosTxFn
	t.Cleanup(func() {
		getCosmosNonceFn = originalGetNonce
		broadcastCosmosTxFn = originalBroadcast
	})
}

func successfulBroadcastResponse(hash string) *tx.BroadcastTxResponse {
	return &tx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: hash}}
}

func wrongSequenceBroadcastResponse() *tx.BroadcastTxResponse {
	return &tx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{Code: sdkErrors.ErrWrongSequence.ABCICode()}}
}
