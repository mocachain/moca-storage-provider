package gnfd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mocachain/moca-storage-provider/core/consensus"
)

// Values read from testnet moca_222888-1 (node 1.4.1).
const (
	// EVM tx at height 33324320 and the Cosmos tx that carries it.
	testEvmHash    = "0xafd500ddbb76aedc5044c17f02820bf776657a9d82af201c8eb2ae37d0657b06"
	testCosmosHash = "75E6089345F587F9FAA3F566D82F7E51EB866D9DCDAD9C22B6B44FE6D130E9E7"
	// Reverted EVM tx at height 33174950: Cosmos code 0, ethereumTxFailed set.
	testFailedEvmHash    = "0xcea850480a385b028e22d05e5d19398c83b51f33ce042c05a58bcc33ebbdac71"
	testFailedCosmosHash = "A4948601FB4EA880EEEBE4FAA2737ED47953AD503A234243E811BD4AD1B3011F"
)

// fakeTxClient answers like a node: GetTx only knows Cosmos hashes and fails to
// hex-decode a "0x" prefix; GetTxsEvent matches the ethereum_tx.ethereumTxHash
// value exactly (case-sensitive).
type fakeTxClient struct {
	byCosmosHash map[string]*sdk.TxResponse
	byEvmHash    map[string]*sdk.TxResponse
	getTxErr     error
	eventsErr    error

	getTxCalls  []string
	eventsCalls []*tx.GetTxsEventRequest
}

func (f *fakeTxClient) GetTx(_ context.Context, in *tx.GetTxRequest, _ ...grpc.CallOption) (*tx.GetTxResponse, error) {
	f.getTxCalls = append(f.getTxCalls, in.Hash)
	if f.getTxErr != nil {
		return nil, f.getTxErr
	}
	if strings.HasPrefix(in.Hash, "0x") {
		return nil, status.Error(codes.InvalidArgument, "encoding/hex: invalid byte: U+0078 'x'")
	}
	if resp, ok := f.byCosmosHash[strings.ToUpper(in.Hash)]; ok {
		return &tx.GetTxResponse{TxResponse: resp}, nil
	}
	return nil, status.Errorf(codes.NotFound, "tx not found: %s", in.Hash)
}

func (f *fakeTxClient) GetTxsEvent(_ context.Context, in *tx.GetTxsEventRequest, _ ...grpc.CallOption) (*tx.GetTxsEventResponse, error) {
	f.eventsCalls = append(f.eventsCalls, in)
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	for hash, resp := range f.byEvmHash {
		if in.Query == fmt.Sprintf("ethereum_tx.ethereumTxHash='%s'", hash) {
			return &tx.GetTxsEventResponse{TxResponses: []*sdk.TxResponse{resp}, Total: 1}, nil
		}
	}
	return &tx.GetTxsEventResponse{}, nil
}

func evmTxResponse(cosmosHash, evmHash, failure string) *sdk.TxResponse {
	attrs := []abci.EventAttribute{
		{Key: "ethereumTxHash", Value: evmHash},
		{Key: "txIndex", Value: "0"},
	}
	if failure != "" {
		attrs = append(attrs, abci.EventAttribute{Key: "ethereumTxFailed", Value: failure})
	}
	return &sdk.TxResponse{
		TxHash: cosmosHash,
		Code:   0,
		Events: []abci.Event{
			{Type: "message", Attributes: []abci.EventAttribute{{Key: "action", Value: "/cosmos.evm.vm.v1.MsgEthereumTx"}}},
			{Type: "ethereum_tx", Attributes: attrs},
		},
	}
}

type waitCounter struct{ n int }

func (w *waitCounter) wait(context.Context) error {
	w.n++
	return nil
}

func TestConfirmTxFindsEvmHashByEvent(t *testing.T) {
	want := evmTxResponse(testCosmosHash, testEvmHash, "")
	client := &fakeTxClient{byEvmHash: map[string]*sdk.TxResponse{testEvmHash: want}}
	w := &waitCounter{}

	resp, err := confirmTx(context.Background(), client, w.wait, testEvmHash)

	require.NoError(t, err)
	assert.Same(t, want, resp)
	assert.Empty(t, client.getTxCalls, "an EVM hash must not be looked up with GetTx")
	require.Len(t, client.eventsCalls, 1)
	assert.Equal(t, "ethereum_tx.ethereumTxHash='"+testEvmHash+"'", client.eventsCalls[0].Query)
	assert.Zero(t, w.n)
}

func TestConfirmTxLowercasesEvmHash(t *testing.T) {
	want := evmTxResponse(testCosmosHash, testEvmHash, "")
	client := &fakeTxClient{byEvmHash: map[string]*sdk.TxResponse{testEvmHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, "0X"+strings.ToUpper(testEvmHash[2:]))

	require.NoError(t, err)
	assert.Same(t, want, resp)
	require.Len(t, client.eventsCalls, 1)
	assert.Equal(t, "ethereum_tx.ethereumTxHash='"+testEvmHash+"'", client.eventsCalls[0].Query)
}

func TestConfirmTxReportsRevertedEvmTx(t *testing.T) {
	want := evmTxResponse(testFailedCosmosHash, testFailedEvmHash, "Object already exists")
	client := &fakeTxClient{byEvmHash: map[string]*sdk.TxResponse{testFailedEvmHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, testFailedEvmHash)

	require.ErrorIs(t, err, consensus.ErrTxExecutionFailed)
	assert.Contains(t, err.Error(), "Object already exists")
	assert.Contains(t, err.Error(), testFailedEvmHash)
	assert.Same(t, want, resp)
}

func TestConfirmTxIgnoresFailureOfAnotherEvmTx(t *testing.T) {
	want := evmTxResponse(testCosmosHash, testEvmHash, "")
	other := evmTxResponse(testFailedCosmosHash, testFailedEvmHash, "Object already exists")
	want.Events = append(want.Events, other.Events[1])
	client := &fakeTxClient{byEvmHash: map[string]*sdk.TxResponse{testEvmHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, testEvmHash)

	require.NoError(t, err)
	assert.Same(t, want, resp)
}

func TestConfirmTxReportsRevertedEvmTxByCosmosHash(t *testing.T) {
	want := evmTxResponse(testFailedCosmosHash, testFailedEvmHash, "Object already exists")
	client := &fakeTxClient{byCosmosHash: map[string]*sdk.TxResponse{testFailedCosmosHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, testFailedCosmosHash)

	require.ErrorIs(t, err, consensus.ErrTxExecutionFailed)
	assert.Contains(t, err.Error(), "Object already exists")
	assert.Same(t, want, resp)
}

func TestConfirmTxReportsFailedCosmosTx(t *testing.T) {
	want := &sdk.TxResponse{TxHash: testCosmosHash, Code: 5, Codespace: "sdk", RawLog: "insufficient funds"}
	client := &fakeTxClient{byCosmosHash: map[string]*sdk.TxResponse{testCosmosHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, testCosmosHash)

	require.ErrorIs(t, err, consensus.ErrTxExecutionFailed)
	assert.Contains(t, err.Error(), "code=5")
	assert.Same(t, want, resp)
}

func TestConfirmTxFindsCosmosHash(t *testing.T) {
	want := &sdk.TxResponse{TxHash: testCosmosHash}
	client := &fakeTxClient{byCosmosHash: map[string]*sdk.TxResponse{testCosmosHash: want}}

	resp, err := confirmTx(context.Background(), client, (&waitCounter{}).wait, testCosmosHash)

	require.NoError(t, err)
	assert.Same(t, want, resp)
	assert.Equal(t, []string{testCosmosHash}, client.getTxCalls)
	assert.Empty(t, client.eventsCalls)
}

func TestConfirmTxWaitsForBlocksWhileNotFound(t *testing.T) {
	for name, hash := range map[string]string{"evm": testEvmHash, "cosmos": testCosmosHash} {
		t.Run(name, func(t *testing.T) {
			client := &fakeTxClient{}
			w := &waitCounter{}

			resp, err := confirmTx(context.Background(), client, w.wait, hash)

			require.Error(t, err)
			assert.NotErrorIs(t, err, consensus.ErrTxExecutionFailed)
			assert.Nil(t, resp)
			assert.Equal(t, ConfirmBlockNumber, w.n)
			assert.Equal(t, ConfirmBlockNumber, len(client.getTxCalls)+len(client.eventsCalls))
		})
	}
}

func TestConfirmTxReturnsRPCErrorAtOnce(t *testing.T) {
	rpcErr := status.Error(codes.Unavailable, "connection refused")
	for name, hash := range map[string]string{"evm": testEvmHash, "cosmos": testCosmosHash} {
		t.Run(name, func(t *testing.T) {
			client := &fakeTxClient{getTxErr: rpcErr, eventsErr: rpcErr}
			w := &waitCounter{}

			_, err := confirmTx(context.Background(), client, w.wait, hash)

			require.ErrorIs(t, err, rpcErr)
			assert.Zero(t, w.n)
			assert.Equal(t, 1, len(client.getTxCalls)+len(client.eventsCalls))
		})
	}
}

func TestConfirmTxRejectsMalformedHash(t *testing.T) {
	for name, hash := range map[string]string{
		"empty":         "",
		"leading space": " " + testEvmHash,
		"trailing nl":   testCosmosHash + "\n",
		"63 hex":        testCosmosHash[1:],
		"evm 63 hex":    testEvmHash[:65],
		"double prefix": "0x" + testEvmHash,
		"non hex":       "0x" + strings.Repeat("g", 64),
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeTxClient{}
			w := &waitCounter{}

			_, err := confirmTx(context.Background(), client, w.wait, hash)

			require.Error(t, err)
			assert.Empty(t, client.getTxCalls)
			assert.Empty(t, client.eventsCalls)
			assert.Zero(t, w.n)
		})
	}
}
