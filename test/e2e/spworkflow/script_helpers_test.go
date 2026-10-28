package spworkflow_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scriptFuncs returns the source of the named functions of e2e_test.sh plus the retry_cmd failure pattern.
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

// strictScript is a set -euo pipefail script defining the named functions of e2e_test.sh
func strictScript(t *testing.T, names ...string) string {
	t.Helper()
	return "set -euo pipefail\n" + scriptFuncs(t, names...) + "\n"
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeMocadQ answers the queries in answers, keyed by their arguments, and fails any other like a broken rpc
func fakeMocadQ(answers map[string]string) string {
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("mocad_q() {\n  case \"$*\" in\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s) printf '%%s' %s ;;\n", shq(k), shq(answers[k]))
	}
	b.WriteString("  *) echo \"rpc error: query $* failed\" >&2; return 1 ;;\n  esac\n}\n")
	return b.String()
}

// runPredicate runs a script predicate as the last command of a strict script, against a fake chain
func runPredicate(t *testing.T, predicate string, answers map[string]string, arg string) (string, int) {
	t.Helper()
	return runBash(t, strictScript(t, predicate)+fakeMocadQ(answers)+predicate+" "+shq(arg))
}

// runWaited gives a predicate one poll through wait_until, like the exit test does
func runWaited(t *testing.T, predicate string, answers map[string]string, arg string) (string, int) {
	t.Helper()
	return runBash(t, strictScript(t, "wait_until", predicate)+fakeMocadQ(answers)+
		`wait_until 0 1 "check" `+predicate+" "+shq(arg))
}

const (
	familiesQuery = "virtualgroup global-virtual-group-families 1000"
	spListQuery   = "sp storage-providers"
	exitingSP     = "0xAbCd000000000000000000000000000000000001"
)

func gvgsQuery(family int) string {
	return fmt.Sprintf("virtualgroup global-virtual-group-by-family-id %d", family)
}

// familiesJSON lists one family per primary sp id, numbered from 1
func familiesJSON(primarySPIDs ...int) string {
	families := make([]string, len(primarySPIDs))
	for i, sp := range primarySPIDs {
		families[i] = fmt.Sprintf(`{"id":%d,"primary_sp_id":%d,"global_virtual_group_ids":[],"virtual_payment_address":"0x01"}`, i+1, sp)
	}
	return `{"gvg_families":[` + strings.Join(families, ",") + `],"pagination":{"next_key":null,"total":"0"}}`
}

// gvgsJSON lists one group per comma separated secondary sp id list
func gvgsJSON(secondarySets ...string) string {
	gvgs := make([]string, len(secondarySets))
	for i, ids := range secondarySets {
		gvgs[i] = fmt.Sprintf(`{"id":%d,"family_id":1,"primary_sp_id":1,"secondary_sp_ids":[%s],"stored_size":"266"}`, i+1, ids)
	}
	return `{"global_virtual_groups":[` + strings.Join(gvgs, ",") + `]}`
}

func TestSPGVGsDrainedFailsWhenAChainQueryFails(t *testing.T) {
	out, code := runPredicate(t, "sp_gvgs_drained", nil, "7")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "global virtual group families query failed: rpc error")
	require.NotContains(t, out, "primary_families=")

	out, code = runPredicate(t, "sp_gvgs_drained", map[string]string{
		familiesQuery: familiesJSON(1, 2),
		gvgsQuery(1):  gvgsJSON(),
	}, "7")
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "global virtual groups query of family 2 failed: rpc error")
	require.NotContains(t, out, "primary_families=")
}

// wait_until runs the predicate in an if condition, where set -e does not apply
func TestSPGVGsDrainedIsNotSatisfiedByFailingQueriesWhenPolled(t *testing.T) {
	out, code := runWaited(t, "sp_gvgs_drained", nil, "7")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "check: not satisfied within 0s, last state: global virtual group families query failed")
	require.NotContains(t, out, "primary_families=")
}

func TestSPGVGsDrainedRejectsAnswersWithoutTheExpectedShape(t *testing.T) {
	for _, tc := range []struct{ name, families string }{
		{"empty output", ""},
		{"not json", "Error: rpc error: code = Unknown desc = boom"},
		{"truncated json", `{"gvg_families":[{"id":1`},
		{"no family list", `{"pagination":{"next_key":null,"total":"0"}}`},
		{"null document", `null`},
		{"list document", `[]`},
		{"null family list", `{"gvg_families":null}`},
		{"family list is an object", `{"gvg_families":{"id":1}}`},
		{"family without primary sp", `{"gvg_families":[{"id":1}]}`},
		{"family without id", `{"gvg_families":[{"primary_sp_id":1}]}`},
		{"family primary sp is not a number", `{"gvg_families":[{"id":1,"primary_sp_id":"x"}]}`},
	} {
		t.Run("families "+tc.name, func(t *testing.T) {
			out, code := runPredicate(t, "sp_gvgs_drained", map[string]string{familiesQuery: tc.families}, "7")
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "global virtual group families are unreadable")
			require.NotContains(t, out, "primary_families=")
		})
	}
	for _, tc := range []struct{ name, gvgs string }{
		{"empty output", ""},
		{"not json", "Error: rpc error: code = Unknown desc = boom"},
		{"no group list", `{}`},
		{"null group list", `{"global_virtual_groups":null}`},
		{"group list is an object", `{"global_virtual_groups":{"id":1}}`},
		{"group without secondaries", `{"global_virtual_groups":[{"id":1}]}`},
		{"secondaries are not a list", `{"global_virtual_groups":[{"secondary_sp_ids":"7"}]}`},
		{"secondary is not a number", `{"global_virtual_groups":[{"secondary_sp_ids":["x"]}]}`},
	} {
		t.Run("groups "+tc.name, func(t *testing.T) {
			out, code := runPredicate(t, "sp_gvgs_drained", map[string]string{familiesQuery: familiesJSON(1), gvgsQuery(1): tc.gvgs}, "7")
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "global virtual groups of family 1 are unreadable")
			require.NotContains(t, out, "primary_families=")
		})
	}
}

func TestSPGVGsDrainedSucceedsOnlyForAWellFormedEmptyMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers map[string]string
	}{
		{"no families", map[string]string{familiesQuery: `{"gvg_families":[],"pagination":{"next_key":null,"total":"0"}}`}},
		{"only other sps", map[string]string{
			familiesQuery: familiesJSON(1, 2, 1),
			gvgsQuery(1):  gvgsJSON("3,4", "5,6"),
			gvgsQuery(2):  gvgsJSON(),
			gvgsQuery(3):  gvgsJSON("3,5"),
		}},
		{"ids as strings", map[string]string{
			familiesQuery: `{"gvg_families":[{"id":"1","primary_sp_id":"2"}]}`,
			gvgsQuery(1):  `{"global_virtual_groups":[{"secondary_sp_ids":["3","4"]}]}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runPredicate(t, "sp_gvgs_drained", tc.answers, "7")
			require.Equal(t, 0, code, out)
			require.Contains(t, out, "primary_families=0 secondary_gvgs=0")
		})
	}
}

func TestSPGVGsDrainedFailsWhileTheSPHoldsAMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers map[string]string
		state   string
	}{
		{"primary of an empty family", map[string]string{
			familiesQuery: familiesJSON(1, 7),
			gvgsQuery(1):  gvgsJSON("3,4"),
			gvgsQuery(2):  gvgsJSON(),
		}, "primary_families=1 secondary_gvgs=0"},
		{"secondary of two groups", map[string]string{
			familiesQuery: familiesJSON(1, 2),
			gvgsQuery(1):  gvgsJSON("3,4"),
			gvgsQuery(2):  gvgsJSON("5,7", "7,8"),
		}, "primary_families=0 secondary_gvgs=2"},
		{"primary and secondary", map[string]string{
			familiesQuery: familiesJSON(7, 2),
			gvgsQuery(1):  gvgsJSON(),
			gvgsQuery(2):  gvgsJSON("7,8"),
		}, "primary_families=1 secondary_gvgs=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runPredicate(t, "sp_gvgs_drained", tc.answers, "7")
			require.Equal(t, 1, code, out)
			require.Contains(t, out, tc.state)

			out, code = runWaited(t, "sp_gvgs_drained", tc.answers, "7")
			require.Equal(t, 1, code, out)
			require.Contains(t, out, "check: not satisfied within 0s, last state: "+tc.state)
		})
	}
}

type chainSP struct {
	id       int
	operator string
	status   string
}

func spListJSON(sps ...chainSP) string {
	items := make([]string, len(sps))
	for i, sp := range sps {
		items[i] = fmt.Sprintf(`{"id":%d,"operator_address":%q,"status":%q,"endpoint":"https://sp.example"}`, sp.id, sp.operator, sp.status)
	}
	return `{"sps":[` + strings.Join(items, ",") + `],"pagination":{"next_key":null,"total":"0"}}`
}

func TestSPLeftChainFailsWhenTheListQueryFails(t *testing.T) {
	out, code := runPredicate(t, "sp_left_chain", nil, exitingSP)
	require.Equal(t, 2, code, out)
	require.Contains(t, out, "sp list query failed: rpc error")
	require.NotContains(t, out, "not in the SP list any more")

	out, code = runWaited(t, "sp_left_chain", nil, exitingSP)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "check: not satisfied within 0s, last state: sp list query failed")
	require.NotContains(t, out, "not in the SP list any more")
}

func TestSPLeftChainTellsAnUnreadableListFromAListWithoutTheSP(t *testing.T) {
	for _, tc := range []struct{ name, list string }{
		{"empty output", ""},
		{"not json", "Error: rpc error: code = Unknown desc = boom"},
		{"truncated json", `{"sps":[{"id":1`},
		{"no sp list", `{"pagination":{"next_key":null,"total":"0"}}`},
		{"null document", `null`},
		{"null sp list", `{"sps":null}`},
		{"sp list is an object", `{"sps":{"id":1}}`},
		{"sps are not objects", `{"sps":[1,2]}`},
		{"sp without operator address", `{"sps":[{"id":1,"status":"STATUS_IN_SERVICE"}]}`},
		{"null operator address", `{"sps":[{"id":1,"operator_address":null}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runPredicate(t, "sp_left_chain", map[string]string{spListQuery: tc.list}, exitingSP)
			require.Equal(t, 2, code, out)
			require.Contains(t, out, "sp list is unreadable")
			require.NotContains(t, out, "not in the SP list any more")
		})
	}
}

func TestSPLeftChainSucceedsForAValidListWithoutTheSP(t *testing.T) {
	out, code := runPredicate(t, "sp_left_chain", map[string]string{spListQuery: spListJSON(
		chainSP{2, "0x2222222222222222222222222222222222222222", "STATUS_IN_SERVICE"},
		chainSP{3, "0x3333333333333333333333333333333333333333", "STATUS_IN_SERVICE"},
	)}, exitingSP)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "not in the SP list any more (2 SPs remain)")

	out, code = runPredicate(t, "sp_left_chain", map[string]string{spListQuery: spListJSON()}, exitingSP)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "not in the SP list any more (0 SPs remain)")
}

func TestSPLeftChainFailsWhileTheSPIsListed(t *testing.T) {
	answers := map[string]string{spListQuery: spListJSON(
		chainSP{1, strings.ToLower(exitingSP), "STATUS_IN_SERVICE"},
		chainSP{2, "0x2222222222222222222222222222222222222222", "STATUS_GRACEFUL_EXITING"},
	)}
	out, code := runPredicate(t, "sp_left_chain", answers, exitingSP)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "still listed (1:STATUS_IN_SERVICE 2:STATUS_GRACEFUL_EXITING)")

	out, code = runWaited(t, "sp_left_chain", answers, exitingSP)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "check: not satisfied within 0s, last state: still listed")
}

func hash32(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// headObjectJSON is a head-object answer: the primary's integrity hash, then one per secondary, as base64
func headObjectJSON(id string, checksums ...[]byte) string {
	encoded := make([]string, len(checksums))
	for i, c := range checksums {
		encoded[i] = strconv.Quote(base64.StdEncoding.EncodeToString(c))
	}
	return fmt.Sprintf(`{"object_info":{"id":%q,"checksums":[%s]},"global_virtual_group":{"id":4}}`, id, strings.Join(encoded, ","))
}

// challengePieceOutput is what challenge.piece prints for a piece that agrees with its own metadata
func challengePieceOutput(integrityHash string) string {
	piece := hex.EncodeToString(hash32("piece"))
	return "integrity meta info: \n\nintegrity hash[" + integrityHash + "]\n\n" +
		"piece[0] checksum[" + piece + "]\n" +
		"\nchallenge piece info\n: redundancy_idx[0], segment_idx[0], piece_checksum[" + piece + "]\n\n" +
		"succeed to check integrity hash!!!\n"
}

// the script reads the hash from the line challenge.piece prints
func TestChallengePieceOutputIsWhatTheScriptParses(t *testing.T) {
	src, err := os.ReadFile("../../../cmd/command/query.go")
	require.NoError(t, err)
	require.Contains(t, string(src), `fmt.Printf("integrity hash[%s]\n\n", hex.EncodeToString(integrityHash))`)
	require.Contains(t, string(src), `fmt.Printf("succeed to check integrity hash!!!\n")`)
}

func TestIntegrityHashMatchesChainChecksum(t *testing.T) {
	primary, sec0, sec1 := hash32("primary pieces"), hash32("secondary 0 pieces"), hash32("secondary 1 pieces")
	chain := headObjectJSON("42", primary, sec0, sec1)
	hx := hex.EncodeToString

	for _, tc := range []struct {
		name       string
		out        string
		head       string
		redundancy int
		failure    string // empty: the hash must match
	}{
		{"primary is checksum 0", challengePieceOutput(hx(primary)), chain, -1, ""},
		{"secondary 0 is checksum 1", challengePieceOutput(hx(sec0)), chain, 0, ""},
		{"secondary 1 is checksum 2", challengePieceOutput(hx(sec1)), chain, 1, ""},
		{"upper case hex", challengePieceOutput(strings.ToUpper(hx(sec0))), chain, 0, ""},
		{"hash of another redundancy index", challengePieceOutput(hx(primary)), chain, 0, "is not the on-chain checksum " + hx(sec0)},
		{"self consistent but not on chain", challengePieceOutput(hx(hash32("forged"))), chain, 1, "is not the on-chain checksum " + hx(sec1)},
		{"prefix of the chain checksum", challengePieceOutput(hx(sec0)[:32]), chain, 0, "is not the on-chain checksum"},
		{"no hash printed", "failed to get challenge info, error: no such piece\n", chain, 0, "printed no integrity hash"},
		{"empty hash printed", challengePieceOutput(""), chain, 0, "printed no integrity hash"},
		{"failure line is not the hash line", "integrity hash[" + hx(sec0) + "] mismatch checksum list result[ab]\n", chain, 0, "printed no integrity hash"},
		{"index beyond the checksums", challengePieceOutput(hx(sec1)), chain, 3, "no readable checksum for redundancy index 3"},
		{"negative index does not wrap", challengePieceOutput(hx(sec1)), chain, -2, "no readable checksum for redundancy index -2"},
		{"checksum is not base64", challengePieceOutput(hx(primary)), `{"object_info":{"checksums":["***"]}}`, -1, "no readable checksum"},
		{"checksum is empty", challengePieceOutput(hx(primary)), `{"object_info":{"checksums":[""]}}`, -1, "no readable checksum"},
		{"no checksums", challengePieceOutput(hx(primary)), `{"object_info":{"id":"42"}}`, -1, "no readable checksum"},
		{"no head object", challengePieceOutput(hx(primary)), "", -1, "no readable checksum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runBash(t, strictScript(t, "hex_of_b64", "integrity_hash_matches_chain")+
				"integrity_hash_matches_chain "+shq(tc.out)+" "+shq(tc.head)+" "+strconv.Itoa(tc.redundancy))
			if tc.failure == "" {
				require.Equal(t, 0, code, out)
				require.Contains(t, out, fmt.Sprintf("is the on-chain checksum of redundancy index %d", tc.redundancy))
				return
			}
			require.Equal(t, 1, code, out)
			require.Contains(t, out, tc.failure)
			require.NotContains(t, out, "is the on-chain checksum of")
		})
	}
}

// fakeSPDir makes ${spdir}, whose moca-sp3 records its arguments and prints the file out.txt
const fakeSPDir = `
spdir="$(mktemp -d)/sp3"
mkdir -p "${spdir}"
trap 'rm -rf "$(dirname "${spdir}")"' EXIT
cat >"${spdir}/moca-sp3" <<'FAKE'
#!/usr/bin/env bash
echo "$@" >"$(dirname "$0")/args.txt"
cat "$(dirname "$0")/out.txt"
FAKE
chmod +x "${spdir}/moca-sp3"
`

// runChallengePieceOk challenges spexit-sec/secondary_object.txt at a redundancy index on a fake SP that prints spOutput
func runChallengePieceOk(t *testing.T, answers map[string]string, spOutput string, redundancy int) (string, int) {
	t.Helper()
	return runBash(t, strictScript(t, "hex_of_b64", "integrity_hash_matches_chain", "challenge_piece_ok")+
		fakeMocadQ(answers)+fakeSPDir+
		"printf '%s' "+shq(spOutput)+` >"${spdir}/out.txt"`+"\n"+
		fmt.Sprintf("rc=0\nchallenge_piece_ok \"${spdir}\" spexit-sec secondary_object.txt %d 0 || rc=$?\n", redundancy)+
		`echo "sp args: $(cat "${spdir}/args.txt" 2>/dev/null || echo not run)"`+"\n"+
		`exit "${rc}"`)
}

const headObjectQuery = "storage head-object spexit-sec secondary_object.txt"

func TestChallengePieceOkAcceptsThePieceOfTheChainChecksum(t *testing.T) {
	primary, sec0, sec1 := hash32("primary pieces"), hash32("secondary 0 pieces"), hash32("secondary 1 pieces")
	out, code := runChallengePieceOk(t, map[string]string{headObjectQuery: headObjectJSON("42", primary, sec0, sec1)},
		challengePieceOutput(hex.EncodeToString(sec1)), 1)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "spexit-sec/secondary_object.txt redundancy 1 segment 0 on sp3: integrity hash "+hex.EncodeToString(sec1)+" is the on-chain checksum of redundancy index 1")
	require.Contains(t, out, "sp args: challenge.piece -c ./config.toml --object.id 42 --redundancy.index=1 --segment.index 0")
}

// challenge.piece only compares the SP's data with the SP's own metadata
func TestChallengePieceOkRejectsASelfConsistentPieceThatIsNotOnChain(t *testing.T) {
	primary, sec0, sec1 := hash32("primary pieces"), hash32("secondary 0 pieces"), hash32("secondary 1 pieces")
	out, code := runChallengePieceOk(t, map[string]string{headObjectQuery: headObjectJSON("42", primary, sec0, sec1)},
		challengePieceOutput(hex.EncodeToString(hash32("forged"))), 1)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "challenge spexit-sec/secondary_object.txt redundancy 1 segment 0 on sp3 failed: integrity hash "+hex.EncodeToString(hash32("forged"))+" is not the on-chain checksum "+hex.EncodeToString(sec1))
	require.NotContains(t, out, "is the on-chain checksum of")
}

func TestChallengePieceOkFailsWhenThePieceCheckFails(t *testing.T) {
	chain := headObjectJSON("42", hash32("primary pieces"), hash32("secondary 0 pieces"))
	out, code := runChallengePieceOk(t, map[string]string{headObjectQuery: chain},
		"failed to get challenge info, error: no such piece\n", 0)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "on sp3 failed:")
	require.Contains(t, out, "failed to get challenge info, error: no such piece")
	require.NotContains(t, out, "on-chain checksum")
}

func TestChallengePieceOkNeedsReadableChainInfoBeforeAskingTheSP(t *testing.T) {
	out, code := runChallengePieceOk(t, nil, challengePieceOutput(hex.EncodeToString(hash32("primary pieces"))), -1)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "head-object query failed: rpc error")
	require.Contains(t, out, "sp args: not run")

	out, code = runChallengePieceOk(t, map[string]string{headObjectQuery: `{"object_info":{"checksums":[]}}`},
		challengePieceOutput(hex.EncodeToString(hash32("primary pieces"))), -1)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "head-object has no object id")
	require.Contains(t, out, "sp args: not run")
}
