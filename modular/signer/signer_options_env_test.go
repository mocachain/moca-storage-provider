package signer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

// signerConfigWithChain returns the smallest config that reaches the private key
// resolution in DefaultSignerOptions.
func signerConfigWithChain() *gfspconfig.GfSpConfig {
	cfg := &gfspconfig.GfSpConfig{}
	cfg.Chain.ChainAddress = []string{"localhost:9090"}
	return cfg
}

func TestDefaultSignerOptions_RejectsEmptyPrivateKeyEnv(t *testing.T) {
	for _, env := range []string{SpOperatorPrivKey, SpSealPrivKey, SpBlsPrivKey, SpApprovalPrivKey, SpGcPrivKey} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "")

			cfg := signerConfigWithChain()
			cfg.SpAccount.OperatorPrivateKey = testConfiguredKey
			cfg.SpAccount.SealPrivateKey = testConfiguredKey
			cfg.SpAccount.BlsPrivateKey = testConfiguredKey
			cfg.SpAccount.ApprovalPrivateKey = testConfiguredKey
			cfg.SpAccount.GcPrivateKey = testConfiguredKey

			err := DefaultSignerOptions(&SignModular{}, cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), env,
				"an empty key env var must fail startup instead of silently blanking the configured key")
		})
	}
}

func TestDefaultSignerOptions_RejectsFundingPrivateKey(t *testing.T) {
	cfg := signerConfigWithChain()
	cfg.SpAccount.FundingPrivateKey = testConfiguredKey

	err := DefaultSignerOptions(&SignModular{}, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FundingPrivateKey is not supported")
}

const testConfiguredKey = "0f6e0d9c8b7a695847362514f3e2d1c0b9a8978685746352413f2e1d0c9b8a79"
