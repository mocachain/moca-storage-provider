package signer

import (
	"os"
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
	clearSignerKeyEnvironment(t)
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

// These tests mutate process environment and must remain serial.
func clearSignerKeyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"SIGNER_FUNDING_PRIV_KEY", SpOperatorPrivKey, SpSealPrivKey, SpBlsPrivKey, SpApprovalPrivKey, SpGcPrivKey} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

func TestDefaultSignerOptions_RejectsLegacyFundingEnvironment(t *testing.T) {
	for _, value := range []string{"", "legacy-environment-sentinel"} {
		t.Setenv("SIGNER_FUNDING_PRIV_KEY", value)
		err := DefaultSignerOptions(&SignModular{}, signerConfigWithChain())
		require.ErrorContains(t, err, "SIGNER_FUNDING_PRIV_KEY is not supported")
		require.NotContains(t, err.Error(), "legacy-environment-sentinel")
	}
}

func TestDefaultSignerOptions_SupportedKeyEnvironment(t *testing.T) {
	for _, fromEnv := range []bool{false, true} {
		name := "configured"
		if fromEnv {
			name = "environment"
		}
		t.Run(name, func(t *testing.T) {
			clearSignerKeyEnvironment(t)
			cfg := signerConfigWithChain()
			bindings := []struct {
				env string
				key *string
			}{
				{SpOperatorPrivKey, &cfg.SpAccount.OperatorPrivateKey},
				{SpApprovalPrivKey, &cfg.SpAccount.ApprovalPrivateKey},
				{SpSealPrivKey, &cfg.SpAccount.SealPrivateKey},
				{SpBlsPrivKey, &cfg.SpAccount.BlsPrivateKey},
				{SpGcPrivKey, &cfg.SpAccount.GcPrivateKey},
			}
			for _, binding := range bindings {
				*binding.key = "configured-" + binding.env
				if fromEnv {
					t.Setenv(binding.env, "environment-"+binding.env)
				}
			}
			// Missing RPC stops before client creation, after all key bindings.
			err := DefaultSignerOptions(&SignModular{}, cfg)
			require.EqualError(t, err, "rpc address not configured")
			for _, binding := range bindings {
				prefix := "configured-"
				if fromEnv {
					prefix = "environment-"
				}
				require.Equal(t, prefix+binding.env, *binding.key)
			}
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
