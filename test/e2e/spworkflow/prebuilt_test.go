package spworkflow_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrebuiltTakesOnlyAnExecutableFromTheBinDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mocad"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "moca-sp"), nil, 0o644))
	for _, tc := range []struct {
		binDir, name string
		want         int
	}{
		{dir, "mocad", 0},
		{dir, "moca-sp", 1},
		{dir, "moca-cmd", 1},
		{"", "mocad", 1},
	} {
		out, code := runBash(t, strictScript(t, "prebuilt")+"E2E_BIN_DIR="+shq(tc.binDir)+"\nprebuilt "+tc.name)
		require.Equal(t, tc.want, code, "E2E_BIN_DIR=%q prebuilt %s: %s", tc.binDir, tc.name, out)
	}
}

// gitRun runs git in dir and returns its trimmed output
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

func TestBuildMocadChecksOutTheCommitMocadWasBuiltFrom(t *testing.T) {
	root := t.TempDir()
	gitConfig := filepath.Join(root, "gitconfig")
	require.NoError(t, os.WriteFile(gitConfig, []byte("[user]\n\tname = e2e\n\temail = e2e@example.com\n"), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	upstream := filepath.Join(root, "upstream")
	require.NoError(t, os.MkdirAll(upstream, 0o755))
	gitRun(t, upstream, "init", "-q")
	commits := make([]string, 0, 2)
	for _, version := range []string{"v1", "v2"} {
		require.NoError(t, os.WriteFile(filepath.Join(upstream, "localup.sh"), []byte(version+"\n"), 0o644))
		gitRun(t, upstream, "add", "localup.sh")
		gitRun(t, upstream, "commit", "-q", "-m", version)
		commits = append(commits, gitRun(t, upstream, "rev-parse", "HEAD"))
	}
	built := commits[0]

	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	mocad := "#!/bin/sh\necho '{\"version\":\"v1\",\"commit\":\"" + built + "\"}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "mocad"), []byte(mocad), 0o755))

	workspace := filepath.Join(root, "ws")
	out, code := runBash(t, strictScript(t, "prebuilt", "build_mocad")+
		`sync_repo_ref() { git clone -q "`+upstream+`" "$2"; }`+"\n"+
		"workspace="+shq(workspace)+"\nE2E_BIN_DIR="+shq(binDir)+"\nMOCA_TAG=main\nMOCA_IMAGE=\nbuild_mocad")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "mocad "+built+", moca checkout at "+built)

	moca := filepath.Join(workspace, "moca")
	require.Equal(t, built, gitRun(t, moca, "rev-parse", "HEAD"))
	localup, err := os.ReadFile(filepath.Join(moca, "localup.sh"))
	require.NoError(t, err)
	require.Equal(t, "v1\n", string(localup))
	copied, err := os.ReadFile(filepath.Join(moca, "build", "mocad"))
	require.NoError(t, err)
	require.Equal(t, mocad, string(copied))
}
