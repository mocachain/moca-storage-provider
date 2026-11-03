package gfspconfig

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGfSpConfig_RejectsLegacyFundingEnvironment(t *testing.T) {
	for _, value := range []string{"", "legacy-environment-sentinel"} {
		name := "nonempty"
		if value == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("SIGNER_FUNDING_PRIV_KEY", value)
			err := (&GfSpConfig{}).Validate()
			require.ErrorContains(t, err, "SIGNER_FUNDING_PRIV_KEY is not supported")
			require.Contains(t, err.Error(), "remove it from the environment")
			require.NotContains(t, err.Error(), "legacy-environment-sentinel")
		})
	}
}

func TestGfSpConfig_LegacyFundingPrecedence(t *testing.T) {
	for _, env := range []string{"unset", "empty", "nonempty"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("SIGNER_FUNDING_PRIV_KEY", "legacy-environment-sentinel")
			if env == "unset" {
				require.NoError(t, os.Unsetenv("SIGNER_FUNDING_PRIV_KEY"))
			} else if env == "empty" {
				t.Setenv("SIGNER_FUNDING_PRIV_KEY", "")
			}
			cfg := &GfSpConfig{SpAccount: SpAccountConfig{FundingPrivateKey: "legacy-toml-sentinel"}}
			err := cfg.Validate()
			require.EqualError(t, err, "FundingPrivateKey is not supported; the signer uses OperatorPrivateKey for funding operations")
			require.NotContains(t, err.Error(), "legacy-toml-sentinel")
			require.NotContains(t, err.Error(), "legacy-environment-sentinel")
		})
	}

	t.Run("unset continues existing validation", func(t *testing.T) {
		t.Setenv("SIGNER_FUNDING_PRIV_KEY", "")
		require.NoError(t, os.Unsetenv("SIGNER_FUNDING_PRIV_KEY"))
		require.NoError(t, (&GfSpConfig{}).Validate())
		require.EqualError(t, (&GfSpConfig{Gateway: GatewayConfig{PeerApprovalAuthMode: "invalid"}}).Validate(),
			"Gateway.PeerApprovalAuthMode must be disabled, permissive, or required")
	})
}
