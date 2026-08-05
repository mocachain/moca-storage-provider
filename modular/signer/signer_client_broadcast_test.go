package signer

import (
	"context"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkErrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/mocachain/moca/v2/sdk/client"
	ctypes "github.com/mocachain/moca/v2/sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

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
