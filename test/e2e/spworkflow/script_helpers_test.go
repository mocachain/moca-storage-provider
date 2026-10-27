package spworkflow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scriptFuncs returns the source of the named top-level functions of e2e_test.sh, with the
// failure pattern retry_cmd matches, so the helpers can be exercised without a running stack.
func scriptFuncs(t *testing.T, names ...string) string {
	t.Helper()
	script, err := os.ReadFile("e2e_test.sh")
	require.NoError(t, err)
	lines := strings.Split(string(script), "\n")

	var src []string
	for _, line := range lines {
		if strings.HasPrefix(line, "CMD_FAILURE_PATTERN=") {
			src = append(src, line)
		}
	}
	for _, name := range names {
		start := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "function "+name+"() {") {
				start = i
				break
			}
		}
		require.NotEqual(t, -1, start, "function %s is not in e2e_test.sh", name)
		end := start
		for lines[end] != "}" {
			end++
		}
		src = append(src, lines[start:end+1]...)
	}
	return strings.Join(src, "\n")
}

func runBash(t *testing.T, body string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", body)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "bash did not run: %v\n%s", err, out.String())
	return out.String(), exitErr.ExitCode()
}

func TestWorkflowScriptParses(t *testing.T) {
	out, code := runBash(t, "bash -n e2e_test.sh")
	require.Equal(t, 0, code, out)
}

// moca-cmd prints a failed command on stdout and exits 0: the guard must read the output
func TestRetryCmdTreatsAPrintedCommandErrorAsFailure(t *testing.T) {
	for _, msg := range []string{
		"run command error: bucket not exist",
		"The operator account have no balance, please transfer token to your account",
		"no such bucket",
		"no such object",
		"no such group",
		"no such virtual group family: 3",
		"the user does not exist in the group",
	} {
		out, code := runBash(t, scriptFuncs(t, "retry_cmd")+`
dump_sp_logs() { echo dumped; }
printed_error() { printf '%s\n' '`+msg+`'; }
retry_cmd 3 0 "guarded call" printed_error`)
		require.Equal(t, 1, code, out)
		require.Equal(t, 3, strings.Count(out, "guarded call failed on attempt"), out)
		require.Contains(t, out, "guarded call failed after 3 attempts")
		require.Contains(t, out, "dumped")
		require.NotContains(t, out, "guarded call succeeded")
	}
}

func TestRetryCmdFailsOnNonZeroExit(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "retry_cmd")+`
dump_sp_logs() { :; }
broken() { echo plain output; return 7; }
retry_cmd 2 0 "broken call" broken`)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "broken call failed after 2 attempts")
}

func TestRetryCmdRetriesUntilTheOutputIsClean(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "retry_cmd")+`
dump_sp_logs() { :; }
count_file=$(mktemp)
echo 0 >"${count_file}"
flaky() {
  local n=$(($(cat "${count_file}") + 1))
  echo "${n}" >"${count_file}"
  if [ "${n}" -lt 3 ]; then echo "run command error: not yet"; else echo 'bucket_name:"x"'; fi
}
retry_cmd 5 0 "flaky call" flaky
echo "attempts=$(cat "${count_file}") output=${RETRY_CMD_OUTPUT}"`)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `attempts=3 output=bucket_name:"x"`)
}

func TestRetryCmdFailureStopsAStrictScript(t *testing.T) {
	out, code := runBash(t, "set -euo pipefail\n"+scriptFuncs(t, "retry_cmd")+`
dump_sp_logs() { :; }
printed_error() { echo "run command error: boom"; }
retry_cmd 1 0 "guarded call" printed_error
echo not reached`)
	require.Equal(t, 1, code, out)
	require.NotContains(t, out, "not reached")
}

func TestWaitUntilPollsAndIsBounded(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "wait_until")+`
count_file=$(mktemp)
echo 0 >"${count_file}"
third_time() {
  local n=$(($(cat "${count_file}") + 1))
  echo "${n}" >"${count_file}"
  echo "poll ${n}"
  [ "${n}" -ge 3 ]
}
wait_until 10 0 "ready" third_time`)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "ready: poll 3")

	out, code = runBash(t, scriptFuncs(t, "wait_until")+`
stuck() { echo "state=stuck"; return 1; }
wait_until 1 1 "stuck call" stuck`)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "stuck call: not satisfied within 1s, last state: state=stuck")
	require.Equal(t, 1, strings.Count(out, "waiting, state=stuck"), out)
}

// fakeMocaCmd writes a ./moca-cmd that prints a missing bucket for the first calls and then the bucket
const fakeMocaCmd = `
cd "$(mktemp -d)"
cat >moca-cmd <<'FAKE'
#!/usr/bin/env bash
n=$(cat calls 2>/dev/null || echo 0)
n=$((n + 1))
echo "${n}" >calls
if [ "${n}" -le "${MISSING_CALLS}" ]; then
  echo "no such bucket"
else
  printf 'latest bucket info:\nbucket_status: BUCKET_STATUS_CREATED\nowner:"%s"\nbucket_name:"spbucket"\n' "${BUCKET_OWNER}"
fi
FAKE
chmod +x moca-cmd
TEST_ACCOUNT_ADDRESS=0xAbCd000000000000000000000000000000000001
`

func TestAssertBucketHeadPollsUntilTheBucketShowsUp(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "wait_until", "bucket_head_shows_owner", "assert_bucket_head")+fakeMocaCmd+`
MISSING_CALLS=2 BUCKET_OWNER=0xabcd000000000000000000000000000000000001 assert_bucket_head spbucket 20 0
echo "calls=$(cat calls)"`)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "bucket spbucket head: bucket_name=spbucket owner=0xAbCd000000000000000000000000000000000001")
	require.Contains(t, out, "calls=3")
}

func TestAssertBucketHeadFailsWhenTheBucketNeverShowsUp(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "wait_until", "bucket_head_shows_owner", "assert_bucket_head")+fakeMocaCmd+`
MISSING_CALLS=1000 BUCKET_OWNER=0x1 assert_bucket_head spbucket 2 1`)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "bucket spbucket head: not satisfied within 2s, last state: no such bucket")
}

func TestAssertBucketHeadFailsForAnotherOwner(t *testing.T) {
	out, code := runBash(t, scriptFuncs(t, "wait_until", "bucket_head_shows_owner", "assert_bucket_head")+fakeMocaCmd+`
MISSING_CALLS=0 BUCKET_OWNER=0x2222222222222222222222222222222222222222 assert_bucket_head spbucket 2 1`)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "not satisfied within 2s")
}
