package signer

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkErrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/mocachain/moca-storage-provider/util"
	"github.com/mocachain/moca/v2/sdk/client"
	"github.com/mocachain/moca/v2/sdk/keys"
	ctypes "github.com/mocachain/moca/v2/sdk/types"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestDiscontinueBucketRetriesWithRefreshedSequence(t *testing.T) {
	chainClient := newSequenceRetryTestClient(t)
	signClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignGc: chainClient},
		gcAccNonce:  7,
	}

	restoreSequenceRetrySeams(t)
	var attemptedNonces []uint64
	broadcastTxForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient,
		_ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption,
	) (string, error) {
		attemptedNonces = append(attemptedNonces, txOpt.Nonce)
		if len(attemptedNonces) == 1 {
			return "", sdkErrors.ErrWrongSequence
		}
		return "discontinue-hash", nil
	}
	getNonceOnChainForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient) (uint64, error) {
		return 11, nil
	}

	txHash, err := signClient.DiscontinueBucket(context.Background(), SignGc, &storagetypes.MsgDiscontinueBucket{
		BucketName: "bucket",
		Reason:     "retired",
	})

	require.NoError(t, err)
	assert.Equal(t, "discontinue-hash", txHash)
	assert.Equal(t, []uint64{7, 11}, attemptedNonces)
	assert.Equal(t, uint64(12), signClient.gcAccNonce)
}

func TestUpdateSPPriceRetriesWithRefreshedSequence(t *testing.T) {
	chainClient := newSequenceRetryTestClient(t)
	signClient := &MocaChainSignClient{
		mocaClients:      map[SignType]*client.MocaClient{SignOperator: chainClient},
		operatorAccNonce: 5,
		gasInfo: map[GasInfoType]GasInfo{
			UpdateSPPrice: {},
		},
	}

	restoreSequenceRetrySeams(t)
	var attemptedNonces []uint64
	broadcastTxForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient,
		_ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption,
	) (string, error) {
		attemptedNonces = append(attemptedNonces, txOpt.Nonce)
		if len(attemptedNonces) == 1 {
			return "", sdkErrors.ErrWrongSequence
		}
		return "price-hash", nil
	}
	getNonceOnChainForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient) (uint64, error) {
		return 9, nil
	}

	txHash, err := signClient.UpdateSPPrice(context.Background(), SignOperator, &sptypes.MsgUpdateSpStoragePrice{
		ReadPrice:     math.LegacyOneDec(),
		StorePrice:    math.LegacyOneDec(),
		FreeReadQuota: 10,
	})

	require.NoError(t, err)
	assert.Equal(t, "price-hash", txHash)
	assert.Equal(t, []uint64{5, 9}, attemptedNonces)
	assert.Equal(t, uint64(10), signClient.operatorAccNonce)
}

func TestUpdateSPPriceReportsNonceRefreshFailure(t *testing.T) {
	chainClient := newSequenceRetryTestClient(t)
	signClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignOperator: chainClient},
		gasInfo: map[GasInfoType]GasInfo{
			UpdateSPPrice: {},
		},
	}

	restoreSequenceRetrySeams(t)
	broadcastTxForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient,
		_ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption,
	) (string, error) {
		return "", sdkErrors.ErrWrongSequence
	}
	nonceErr := errors.New("nonce query failed")
	getNonceOnChainForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient) (uint64, error) {
		return 0, nonceErr
	}

	_, err := signClient.UpdateSPPrice(context.Background(), SignOperator, &sptypes.MsgUpdateSpStoragePrice{
		ReadPrice:  math.LegacyOneDec(),
		StorePrice: math.LegacyOneDec(),
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to get operator account nonce")
	assert.NotContains(t, err.Error(), "approval account nonce")
	assert.ErrorContains(t, err, nonceErr.Error())
	assert.NotContains(t, err.Error(), sdkErrors.ErrWrongSequence.Error())
}

func TestDiscontinueBucketDoesNotRetryNonSequenceError(t *testing.T) {
	chainClient := newSequenceRetryTestClient(t)
	signClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignGc: chainClient},
	}

	restoreSequenceRetrySeams(t)
	broadcastCalls := 0
	broadcastTxForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient,
		_ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption,
	) (string, error) {
		broadcastCalls++
		return "", errors.New("transport unavailable")
	}

	_, err := signClient.DiscontinueBucket(context.Background(), SignGc, &storagetypes.MsgDiscontinueBucket{})

	require.ErrorIs(t, err, ErrDiscontinueBucketOnChain)
	assert.Equal(t, 1, broadcastCalls)
}

func TestUpdateSPPriceDoesNotRetryNonSequenceError(t *testing.T) {
	chainClient := newSequenceRetryTestClient(t)
	signClient := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{SignOperator: chainClient},
		gasInfo: map[GasInfoType]GasInfo{
			UpdateSPPrice: {},
		},
	}

	restoreSequenceRetrySeams(t)
	broadcastCalls := 0
	broadcastTxForRetryFn = func(_ *MocaChainSignClient, _ context.Context, _ *client.MocaClient,
		_ []sdk.Msg, _ *ctypes.TxOption, _ ...grpc.CallOption,
	) (string, error) {
		broadcastCalls++
		return "", errors.New("transport unavailable")
	}

	_, err := signClient.UpdateSPPrice(context.Background(), SignOperator, &sptypes.MsgUpdateSpStoragePrice{})

	require.ErrorIs(t, err, ErrUpdateSPPriceOnChain)
	assert.Equal(t, 1, broadcastCalls)
}

func newSequenceRetryTestClient(t *testing.T) *client.MocaClient {
	t.Helper()
	km, err := keys.NewPrivateKeyManager(util.RandHexKey())
	require.NoError(t, err)
	chainClient, err := client.NewMocaClient("http://127.0.0.1:26657", "http://127.0.0.1:8545", "moca_5151-1",
		client.WithKeyManager(km))
	require.NoError(t, err)
	return chainClient
}

func restoreSequenceRetrySeams(t *testing.T) {
	t.Helper()
	originalBroadcast := broadcastTxForRetryFn
	originalGetNonce := getNonceOnChainForRetryFn
	t.Cleanup(func() {
		broadcastTxForRetryFn = originalBroadcast
		getNonceOnChainForRetryFn = originalGetNonce
	})
}
