package signer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDepositEvm_SendsEstimatedGas checks the gas a signer method actually sends:
// the node's estimate (the fake client has no margin), not the configured limit.
func TestDepositEvm_SendsEstimatedGas(t *testing.T) {
	const nonce = uint64(7)
	node := newFakeEvmNode(t, nonce)
	node.setGasEstimate(80_000, "")
	installFakeEvmSeams(t, node)
	signClient := newFakeEvmSignClient(t, node, nonce)

	_, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	require.NoError(t, err)
	require.Equal(t, uint64(80_000), node.sentTx(t, 0).Gas(), node.describe())
	require.Len(t, node.estimateCalls(), 1)
}

// TestDepositEvm_UnestimableCallIsNotSent checks that a call the node refuses to
// estimate, for example because it would revert, is never broadcast and leaves no
// pending row behind.
func TestDepositEvm_UnestimableCallIsNotSent(t *testing.T) {
	const nonce = uint64(7)
	node := newFakeEvmNode(t, nonce)
	node.setGasEstimate(0, "execution reverted: insufficient deposit")
	installFakeEvmSeams(t, node)
	signClient := newFakeEvmSignClient(t, node, nonce)

	_, err := signClient.DepositEvm(scenarioCtx(t), SignOperator, fakeDepositMsg(1000))
	require.Error(t, err)
	_, sends, _ := node.snapshot()
	require.Empty(t, sends, node.describe())
	pending, err := memStoreOf(t, signClient).GetPending(context.Background(), fakeEvmChainID, SignOperator)
	require.NoError(t, err)
	require.Nil(t, pending)
}
