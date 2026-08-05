package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageProviderRejectsLegacyFundingPrivateKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[SpAccount]\nFundingPrivateKey = 'legacy-key'\n"), 0o600))

	err := app.Run([]string{"moca-sp", "--config", configPath})

	require.ErrorContains(t, err, "FundingPrivateKey is not supported")
}
