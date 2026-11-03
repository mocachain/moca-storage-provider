package signer

import (
	"context"
	"math/big"
	"testing"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// signWithEstimate builds the transactor against node and signs a call to a
// precompile address, the way the session bindings do.
func signWithEstimate(t *testing.T, node *fakeEvmNode, maxGasLimit, marginPercent uint64) (*ethtypes.Transaction, error) {
	t.Helper()
	client, err := ethclient.Dial(node.server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	txOpts, err := CreateTxOpts(context.Background(), client, testPrivateKey(t), big.NewInt(5151),
		maxGasLimit, marginPercent, 9, big.NewInt(10_000_000_000))
	require.NoError(t, err)
	to := ethcmn.HexToAddress("0x0000000000000000000000000000000000002001")
	return txOpts.Signer(txOpts.From, ethtypes.NewTx(&ethtypes.LegacyTx{
		Nonce: 9, GasPrice: txOpts.GasPrice, Gas: txOpts.GasLimit, To: &to, Data: []byte{0xde, 0xad, 0xbe, 0xef},
	}))
}

func TestCreateTxOpts_SignsWithEstimatePlusMargin(t *testing.T) {
	node := newFakeEvmNode(t, 9)
	node.setGasEstimate(100_000, "")

	signedTx, err := signWithEstimate(t, node, 1_000_000, 25)
	require.NoError(t, err)
	require.Equal(t, uint64(125_000), signedTx.Gas())

	calls := node.estimateCalls()
	require.Len(t, calls, 1)
	require.Equal(t, ethcmn.HexToAddress("0x0000000000000000000000000000000000002001"), *calls[0].To)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, []byte(append(calls[0].Data, calls[0].Input...)))
	require.Equal(t, crypto.PubkeyToAddress(testPrivateKey(t).PublicKey), calls[0].From)
	require.Nil(t, calls[0].GasPrice, "the estimate must not be capped by the sender's balance at a gas price")
}

func TestCreateTxOpts_MarginStopsAtConfiguredLimit(t *testing.T) {
	node := newFakeEvmNode(t, 9)
	node.setGasEstimate(100_000, "")

	signedTx, err := signWithEstimate(t, node, 110_000, 25)
	require.NoError(t, err)
	require.Equal(t, uint64(110_000), signedTx.Gas())
}

func TestCreateTxOpts_RefusesEstimateAboveConfiguredLimit(t *testing.T) {
	node := newFakeEvmNode(t, 9)
	node.setGasEstimate(200_000, "")

	signedTx, err := signWithEstimate(t, node, 150_000, 25)
	require.ErrorContains(t, err, "estimated gas 200000 exceeds the configured gas limit 150000")
	require.Nil(t, signedTx)
}

func TestCreateTxOpts_RefusesCallTheNodeCannotEstimate(t *testing.T) {
	node := newFakeEvmNode(t, 9)
	node.setGasEstimate(0, "execution reverted: object is not in created status")

	signedTx, err := signWithEstimate(t, node, 1_000_000, 25)
	require.ErrorContains(t, err, "failed to estimate gas")
	require.ErrorContains(t, err, "object is not in created status")
	require.Nil(t, signedTx)
}
