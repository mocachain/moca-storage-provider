package spworkflow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// checkGoTestResults runs check_go_test_results of e2e_test.sh on a `go test -v` log.
func checkGoTestResults(t *testing.T, goTestLog string) (string, int) {
	t.Helper()
	script, err := os.ReadFile("e2e_test.sh")
	require.NoError(t, err)
	lines := strings.Split(string(script), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "function check_go_test_results() {") {
			start = i
			break
		}
	}
	require.NotEqual(t, -1, start, "check_go_test_results is not in e2e_test.sh")
	end := start
	for lines[end] != "}" {
		end++
	}
	logFile := filepath.Join(t.TempDir(), "go-test.log")
	require.NoError(t, os.WriteFile(logFile, []byte(goTestLog), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := "set -euo pipefail\n" + strings.Join(lines[start:end+1], "\n") + "\ncheck_go_test_results \"$1\""
	cmd := exec.CommandContext(ctx, "bash", "-c", body, "bash", logFile)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "bash did not run: %v\n%s", err, out.String())
	return out.String(), exitErr.ExitCode()
}

func TestGoSDKResultsNeedPassingTestsAndNoSkips(t *testing.T) {
	cases := []struct {
		name     string
		log      string
		wantCode int
		wantOut  string
	}{
		{
			name: "all passing",
			log: `--- PASS: TestSuite (70.95s)
    --- PASS: TestSuite/Test_A (34.76s)
    --- PASS: TestSuite/Test_B (21.95s)
PASS
ok  	github.com/mocachain/moca-go-sdk/e2e	72.118s
`,
			wantCode: 0,
			wantOut:  "3 passed, 0 skipped, 0 failed",
		},
		{
			name: "every case skipped",
			log: `--- PASS: TestSuite (0.95s)
    --- SKIP: TestSuite/Test_A (0.00s)
    --- SKIP: TestSuite/Test_B (0.00s)
PASS
ok  	github.com/mocachain/moca-go-sdk/e2e	1.118s
`,
			wantCode: 1,
			wantOut:  "2 test(s) were skipped",
		},
		{
			name: "one case skipped",
			log: `--- PASS: TestSuite (10s)
    --- PASS: TestSuite/Test_A (3s)
    --- SKIP: TestSuite/Test_B (0.00s)
PASS
`,
			wantCode: 1,
			wantOut:  "1 test(s) were skipped",
		},
		{
			name: "filter matched no test",
			log: `testing: warning: no tests to run
PASS
ok  	github.com/mocachain/moca-go-sdk/e2e	0.012s [no tests to run]
`,
			wantCode: 1,
			wantOut:  "no test ran",
		},
		{
			name: "failed case",
			log: `--- FAIL: TestSuite (10s)
    --- FAIL: TestSuite/Test_A (3s)
    --- PASS: TestSuite/Test_B (3s)
FAIL
`,
			wantCode: 1,
			wantOut:  "2 failed",
		},
		{name: "empty log", log: "", wantCode: 1, wantOut: "no test ran"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := checkGoTestResults(t, tc.log)
			require.Equal(t, tc.wantCode, code, out)
			require.Contains(t, out, tc.wantOut)
		})
	}
}
