package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

func TestConfigDumpCmd(t *testing.T) {
	workDir := t.TempDir()
	previousWorkDir, err := os.Getwd()
	assert.NoError(t, err)
	require.NoError(t, os.Chdir(workDir))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previousWorkDir)) })

	err = ConfigDumpCmd.Action(&cli.Context{})
	assert.Equal(t, nil, err)
	config, err := os.ReadFile(filepath.Join(workDir, DefaultConfigFile))
	assert.NoError(t, err)
	assert.NotContains(t, string(config), "FundingPrivateKey")
}
