package signer

import (
	"context"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/mocachain/moca/v2/sdk/client"
	ctypes "github.com/mocachain/moca/v2/sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestBroadcastTxOverwritesNonceFromChain(t *testing.T) {
	originalGetNonce := getCosmosNonceFn
	originalBroadcast := broadcastCosmosTxFn
	t.Cleanup(func() {
		getCosmosNonceFn = originalGetNonce
		broadcastCosmosTxFn = originalBroadcast
	})

	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		return 17, nil
	}
	var broadcastNonce uint64
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastNonce = txOpt.Nonce
		return &tx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: "ABC123"}}, nil
	}

	txOpt := &ctypes.TxOption{Nonce: 9}
	hash, nonce, err := (&MocaChainSignClient{}).broadcastTx(context.Background(), nil, nil, txOpt)

	require.NoError(t, err)
	require.Equal(t, "ABC123", hash)
	require.Equal(t, uint64(17), nonce)
	require.Equal(t, uint64(17), txOpt.Nonce)
	require.Equal(t, uint64(17), broadcastNonce)
}

func TestBroadcastTxDoesNotBroadcastWhenNonceQueryFails(t *testing.T) {
	originalGetNonce := getCosmosNonceFn
	originalBroadcast := broadcastCosmosTxFn
	t.Cleanup(func() {
		getCosmosNonceFn = originalGetNonce
		broadcastCosmosTxFn = originalBroadcast
	})

	getCosmosNonceFn = func(_ *client.MocaClient, _ context.Context) (uint64, error) {
		return 0, fmt.Errorf("nonce unavailable")
	}
	broadcastCalls := 0
	broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
		broadcastCalls++
		return &tx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{}}, nil
	}

	txOpt := &ctypes.TxOption{Nonce: 9}
	hash, _, err := (&MocaChainSignClient{}).broadcastTx(context.Background(), nil, nil, txOpt)

	require.ErrorContains(t, err, "failed to get nonce on chain")
	require.Empty(t, hash)
	require.Equal(t, uint64(9), txOpt.Nonce)
	require.Zero(t, broadcastCalls)
}
