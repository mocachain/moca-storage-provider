package signer

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestDefaultSignerOptions_RejectsGasMarginAboveLimit(t *testing.T) {
	clearSignerKeyEnvironment(t)
	cfg := signerConfigWithChain()
	cfg.Chain.EvmGasMarginPercent = gfspconfig.MaxEvmGasMarginPercent + 1

	err := DefaultSignerOptions(&SignModular{}, cfg)
	require.ErrorContains(t, err, "EvmGasMarginPercent 101 is above 100")
}

func TestDefaultSignerOptions_DefaultsGasMarginAndLimits(t *testing.T) {
	clearSignerKeyEnvironment(t)
	cfg := signerConfigWithChain()

	// The call fails later on the missing keys; the defaults are set before that.
	_ = DefaultSignerOptions(&SignModular{}, cfg)
	require.Equal(t, gfspconfig.DefaultEvmGasMarginPercent, cfg.Chain.EvmGasMarginPercent)
	// Upper bounds sit above the gas measured on a v2 network (seal: up to 473k).
	require.Equal(t, uint64(DefaultSealGasLimit), cfg.Chain.SealGasLimit)
	require.Greater(t, cfg.Chain.SealGasLimit, uint64(473_079))
}
