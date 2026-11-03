package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorageProviderRejectsLegacyFundingPrivateKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[SpAccount]\nFundingPrivateKey = 'legacy-key'\n"), 0o600))

	err := app.Run([]string{"moca-sp", "--config", configPath})

	require.ErrorContains(t, err, "FundingPrivateKey is not supported")
}

func TestStorageProviderRejectsLegacyFundingEnvironment(t *testing.T) {
	if server := os.Getenv("MOCA_TEST_FUNDING_STARTUP_SERVER"); server != "" {
		os.Args = []string{"moca-sp", "--log.std"}
		if server != "default" {
			os.Args = append(os.Args, "--server", server)
		}
		main()
		return
	}
	for _, server := range []string{"default", "manager", "blocksyncer"} {
		t.Run("server="+server, func(t *testing.T) {
			for _, value := range []string{"", "legacy-environment-sentinel"} {
				t.Setenv("SIGNER_FUNDING_PRIV_KEY", value)
				// No config or credentials: the funding error must precede TLS,
				// database and chain client initialization.
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageProviderRejectsLegacyFundingEnvironment$")
				child.Env = append(os.Environ(), "MOCA_TEST_FUNDING_STARTUP_SERVER="+server)
				output, err := child.CombinedOutput()
				require.NoError(t, ctx.Err())
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 1, exit.ExitCode())
				require.Contains(t, string(output), "SIGNER_FUNDING_PRIV_KEY is not supported")
				require.NotContains(t, string(output), "legacy-environment-sentinel")
			}
		})
	}
}
