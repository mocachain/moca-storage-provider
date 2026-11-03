package spworkflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalupKeepsSeparateRecoveryLedgersAcrossRestarts(t *testing.T) {
	script, err := os.ReadFile("../../../deployment/localup/localup.sh")
	require.NoError(t, err)
	start := strings.Index(string(script), "function start_sp() {")
	require.NotEqual(t, -1, start)
	function := string(script)[start:]
	end := strings.Index(function, "\n}")
	require.NotEqual(t, -1, end)
	function = function[:end+2]

	workspace := t.TempDir()
	t.Setenv("TEST_LOCALUP_WORKSPACE", workspace)
	for _, name := range []string{"sp0", "sp1"} {
		dir := filepath.Join(workspace, "local_env", name)
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), nil, 0600))
		binary := "#!/usr/bin/env bash\nprintf '%s\\n%s\\n' \"${RECOVERY_STAGING_LEDGER_DIR-}\" \"${RECOVERY_STAGING_CAPACITY_BYTES-}\" >env.txt\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "moca-"+name), []byte(binary), 0700))
	}
	body := function + `
workspace=$TEST_LOCALUP_WORKSPACE
SP_DEPLOY_DIR=local_env
SP_NUM=2
SP_START_PORT=9033
sp_bin_name=moca-sp
wait_for_grpc_port() {
  for ((i=0; i<100; i++)); do
    if [ -f env.txt ]; then return 0; fi
    sleep 0.01
  done
  return 1
}
start_sp
`
	out, code := runBash(t, body)
	require.Equal(t, 0, code, out)
	for _, name := range []string{"sp0", "sp1"} {
		dir := filepath.Join(workspace, "local_env", name)
		env, err := os.ReadFile(filepath.Join(dir, "env.txt"))
		require.NoError(t, err)
		ledger := filepath.Join(dir, "recovery-staging")
		require.Equal(t, ledger+"\n1073741824\n", string(env))
		info, err := os.Stat(ledger)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
		require.NoError(t, os.WriteFile(filepath.Join(ledger, "existing-claim"), []byte("keep"), 0600))
		require.NoError(t, os.Remove(filepath.Join(dir, "env.txt")))
	}
	out, code = runBash(t, body)
	require.Equal(t, 0, code, out)
	for _, name := range []string{"sp0", "sp1"} {
		claim, err := os.ReadFile(filepath.Join(workspace, "local_env", name, "recovery-staging", "existing-claim"))
		require.NoError(t, err)
		require.Equal(t, "keep", string(claim))
	}
}
