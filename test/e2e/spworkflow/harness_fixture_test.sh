#!/usr/bin/env bash
# Offline fixtures for the test/e2e/spworkflow/e2e_test.sh ref, metadata and
# dependency-pin helpers.
# Usage: bash test/e2e/spworkflow/harness_fixture_test.sh [path-to-e2e_test.sh]
# Uses only local file:// git repos plus a stub go/make for the dependency pin
# order checks (the real Go 1.26 builds are not faked here); starts no
# services and needs no network. Exits nonzero when any check fails.
# (no set -u: the sourced harness reads unset optional env)
SCRIPT=$(realpath "${1:-$(dirname "$0")/e2e_test.sh}")
ROOT=$(mktemp -d "${TMPDIR:-/tmp}/fx.XXXXXX")
PASS=0
FAIL=0
ok() { echo "PASS: $*"; PASS=$((PASS + 1)); }
bad() { echo "FAIL: $*"; FAIL=$((FAIL + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

export GIT_AUTHOR_NAME=fx GIT_AUTHOR_EMAIL=fx@example.invalid GIT_COMMITTER_NAME=fx GIT_COMMITTER_EMAIL=fx@example.invalid
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

# remote with c1 (tag t1) and c2 (branch main)
git init -q -b main "${ROOT}/src"
(cd "${ROOT}/src" && echo one >f && git add f && git commit -qm c1 && git tag t1 && echo two >f && git commit -qam c2)
C1=$(git -C "${ROOT}/src" rev-parse t1)
C2=$(git -C "${ROOT}/src" rev-parse main)
git clone -q --bare "${ROOT}/src" "${ROOT}/remote.git"
URL="file://${ROOT}/remote.git"

run_sync() { # ws ref expected label ; prints rc, then "AFTER" if the next command ran
  local ws=$1
  (
    export GITHUB_WORKSPACE=${ws}
    # shellcheck disable=SC1090
    source "${SCRIPT}" >/dev/null
    set -e
    sync_repo_ref "${URL}" "${ws}/repo" "$2" "$3" "$4"
    echo AFTER
  )
}

# 1. pinned SHA path (fetch by SHA), default protocol
WS=${ROOT}/ws1; mkdir -p "${WS}"
out=$(run_sync "${WS}" "${C1}" "" fx 2>&1); rc=$?
check "sha-fetch: exit 0" "[ ${rc} -eq 0 ]"
check "sha-fetch: next command ran" "grep -q AFTER <<<\"\${out}\""
check "sha-fetch: HEAD is C1" "[ \"\$(git -C ${WS}/repo rev-parse HEAD)\" = ${C1} ]"
check "sha-fetch: ref_check=PASS" "grep -qx 'fx.ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt"
check "sha-fetch: resolved recorded" "grep -qx 'fx.resolved_sha=${C1}' ${WS}/.github/action-logs/e2e-metadata.txt"

# 1b. same with protocol v0 and allowReachableSHA1InWant (GitHub behaviour)
git -C "${ROOT}/remote.git" config uploadpack.allowReachableSHA1InWant true
WS=${ROOT}/ws1b; mkdir -p "${WS}"
out=$(export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=protocol.version GIT_CONFIG_VALUE_0=0; run_sync "${WS}" "${C1}" "" fx 2>&1); rc=$?
check "sha-fetch v0+allowReachable: exit 0 and HEAD C1" "[ ${rc} -eq 0 ] && [ \"\$(git -C ${WS}/repo rev-parse HEAD)\" = ${C1} ]"
git -C "${ROOT}/remote.git" config --unset uploadpack.allowReachableSHA1InWant

# 1c. server refuses SHA wants (v0, no allowReachable, tag removed) but the
#     commit is reachable from a branch: the full clone already has it, so the
#     SHA checkout still succeeds and is asserted
git -C "${ROOT}/remote.git" tag -d t1 >/dev/null
WS=${ROOT}/ws1c; mkdir -p "${WS}"
out=$(export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=protocol.version GIT_CONFIG_VALUE_0=0; run_sync "${WS}" "${C1}" "" fx 2>&1); rc=$?
check "sha in clone, server refuses wants: exit 0, ref_check=PASS" "[ ${rc} -eq 0 ] && grep -qx 'fx.ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt"
git -C "${ROOT}/remote.git" tag t1 "${C1}"

# 1d. commit only behind a hidden ref (not cloned, not advertised): refused -> fail, no fallback
(cd "${ROOT}/src" && git checkout -q -b hidden && echo h >hf && git add hf && git commit -qm hidden && git checkout -q main)
CH=$(git -C "${ROOT}/src" rev-parse hidden)
git -C "${ROOT}/src" push -q "${ROOT}/remote.git" hidden:refs/hidden/x
git -C "${ROOT}/remote.git" config uploadpack.hideRefs refs/hidden
WS=${ROOT}/ws1d; mkdir -p "${WS}"
out=$(export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=protocol.version GIT_CONFIG_VALUE_0=0; run_sync "${WS}" "${CH}" "" fx 2>&1); rc=$?
check "sha-fetch refused: nonzero exit" "[ ${rc} -ne 0 ]"
check "sha-fetch refused: next command did not run" "! grep -q AFTER <<<\"\${out}\""
check "sha-fetch refused: server error surfaced" "grep -q 'unadvertised object' <<<\"\${out}\""
check "sha-fetch refused: nothing recorded as PASS" "! grep -q 'fx.ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt 2>/dev/null"

# 2. tag + expected SHA (fallback path) match
WS=${ROOT}/ws2; mkdir -p "${WS}"
out=$(run_sync "${WS}" t1 "${C1}" fx 2>&1); rc=$?
check "tag+expected match: exit 0" "[ ${rc} -eq 0 ] && grep -q AFTER <<<\"\${out}\""
check "tag+expected match: ref_check=PASS" "grep -qx 'fx.ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt"

# 3. branch moved past expected SHA -> fail before next command
WS=${ROOT}/ws3; mkdir -p "${WS}"
out=$(run_sync "${WS}" main "${C1}" fx 2>&1); rc=$?
check "branch+expected mismatch: nonzero exit" "[ ${rc} -ne 0 ]"
check "branch+expected mismatch: next command did not run" "! grep -q AFTER <<<\"\${out}\""
check "branch+expected mismatch: message names both SHAs" "grep -q \"resolved to ${C2}, expected ${C1}\" <<<\"\${out}\""
check "branch+expected mismatch: ref_check=FAIL" "grep -qx 'fx.ref_check=FAIL' ${WS}/.github/action-logs/e2e-metadata.txt"

# 4. unknown SHA -> fetch fails, propagates
WS=${ROOT}/ws4; mkdir -p "${WS}"
out=$(run_sync "${WS}" 0123456789abcdef0123456789abcdef01234567 "" fx 2>&1); rc=$?
check "unknown sha: nonzero exit, next command did not run" "[ ${rc} -ne 0 ] && ! grep -q AFTER <<<\"\${out}\""

# 5. unpinned branch is recorded as UNPINNED, not PASS
WS=${ROOT}/ws5; mkdir -p "${WS}"
run_sync "${WS}" main "" fx >/dev/null 2>&1; rc=$?
check "unpinned: exit 0, ref_check=UNPINNED" "[ ${rc} -eq 0 ] && grep -qx 'fx.ref_check=UNPINNED' ${WS}/.github/action-logs/e2e-metadata.txt"

# 6. end-to-end through the real entrypoint and a workflow-style pipeline:
#    github URL rewritten to the fixture remote; mismatch must stop before make
WS=${ROOT}/ws6; mkdir -p "${WS}"
(cd "${ROOT}/src" && git checkout -q main && printf 'proto-gen:\n\t@true\nbuild:\n\t@echo STUB_BUILD_REACHED; exit 7\n' >Makefile && git add Makefile && git commit -qm mk)
git -C "${ROOT}/src" push -q "${ROOT}/remote.git" main
C3=$(git -C "${ROOT}/src" rev-parse main)
pipeline() { # ws moca_tag expected [sdk_tag] [cmd_tag]
  (
    cd "$1" || exit 99
    export GITHUB_WORKSPACE=$1 MOCA_TAG=$2 MOCA_EXPECTED_SHA=$3 MOCA_GO_SDK_TAG=${4:-${C3}} MOCA_CMD_TAG=${5:-${C3}}
    export GIT_CONFIG_COUNT=3 \
      GIT_CONFIG_KEY_0="url.${URL}.insteadOf" GIT_CONFIG_VALUE_0=https://github.com/mocachain/moca.git \
      GIT_CONFIG_KEY_1="url.${URL}.insteadOf" GIT_CONFIG_VALUE_1=https://github.com/mocachain/moca-go-sdk.git \
      GIT_CONFIG_KEY_2="url.${URL}.insteadOf" GIT_CONFIG_VALUE_2=https://github.com/mocachain/moca-cmd.git
    bash -e -c 'mkdir -p .github/action-logs; set -o pipefail; bash "$0" --startChain 2>&1 | tee .github/action-logs/start-chain.log' "${SCRIPT}"
  )
}
pipeline "${WS}" main "${C1}" >/dev/null 2>&1; rc=$?
check "pipeline mismatch: step exit nonzero" "[ ${rc} -ne 0 ]"
check "pipeline mismatch: build never reached" "! grep -q STUB_BUILD_REACHED ${WS}/.github/action-logs/start-chain.log"
check "pipeline mismatch: chain.ref_check=FAIL" "grep -qx 'chain.ref_check=FAIL' ${WS}/.github/action-logs/e2e-metadata.txt"
WS=${ROOT}/ws7; mkdir -p "${WS}"
pipeline "${WS}" "${C3}" "" >/dev/null 2>&1; rc=$?
check "pipeline pinned sha: passed ref check and reached build" "grep -qx 'chain.ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt && grep -q STUB_BUILD_REACHED ${WS}/.github/action-logs/start-chain.log"
check "pipeline pinned sha: build failure propagates through tee" "[ ${rc} -ne 0 ]"
check "pipeline pinned sha: sdk and cmd refs checked before chain" "[ \"\$(grep -n -E '^(sdk|cmd|chain).ref_check=PASS' ${WS}/.github/action-logs/e2e-metadata.txt | cut -d: -f2 | cut -d. -f1 | paste -sd,)\" = sdk,cmd,chain ]"
WS=${ROOT}/ws7b; mkdir -p "${WS}"
MOCA_GO_SDK_EXPECTED_SHA=${C1} pipeline "${WS}" "${C3}" "" main "" >/dev/null 2>&1; rc=$?
check "pipeline sdk ref moved: nonzero, chain never cloned or built" "[ ${rc} -ne 0 ] && [ ! -d ${WS}/moca ] && ! grep -q STUB_BUILD_REACHED ${WS}/.github/action-logs/start-chain.log"
WS=${ROOT}/ws7c; mkdir -p "${WS}"
pipeline "${WS}" "${C3}" "" "${C3}" 0123456789abcdef0123456789abcdef01234567 >/dev/null 2>&1; rc=$?
check "pipeline cmd ref unknown: nonzero, chain never cloned" "[ ${rc} -ne 0 ] && [ ! -d ${WS}/moca ]"

# 7. run metadata on a depth-1 checkout of a synthetic merge; missing env -> UNKNOWN
git init -q -b main "${ROOT}/sp"
(cd "${ROOT}/sp" && echo 'module fx' >go.mod && git add go.mod && git commit -qm base && git checkout -qb head && echo x >h && git add h && git commit -qm head && git checkout -q main && git merge -q --no-ff head -m merge)
M=$(git -C "${ROOT}/sp" rev-parse main); P1=$(git -C "${ROOT}/sp" rev-parse main^1); P2=$(git -C "${ROOT}/sp" rev-parse main^2)
git clone -q --depth 1 "file://${ROOT}/sp" "${ROOT}/spclone"
(
  cd "${ROOT}/spclone" || exit 1
  unset GITHUB_RUN_ID GITHUB_RUN_ATTEMPT GITHUB_EVENT_NAME GITHUB_SHA GITHUB_REPOSITORY ImageOS ImageVersion RUNNER_OS
  GITHUB_WORKSPACE=${ROOT}/spclone bash "${SCRIPT}" --recordMetadata >/dev/null
)
MD=${ROOT}/spclone/.github/action-logs/e2e-metadata.txt
check "metadata: shallow clone is depth 1" "[ -f ${ROOT}/spclone/.git/shallow ]"
check "metadata: checkout sha = merge" "grep -qx 'sp.checkout_sha=${M}' ${MD}"
check "metadata: two parents recorded in order" "grep -qx 'sp.checkout_parent.1=${P1}' ${MD} && grep -qx 'sp.checkout_parent.2=${P2}' ${MD} && grep -qx 'sp.checkout_parent_count=2' ${MD}"
check "metadata: missing env -> UNKNOWN" "grep -qx 'run.id=UNKNOWN' ${MD} && grep -qx 'runner.image_version=UNKNOWN' ${MD} && grep -qx 'run.pr_head_sha=UNKNOWN' ${MD}"
check "metadata: go.sum missing -> UNKNOWN" "grep -qx 'sp.go_sum_sha256=UNKNOWN' ${MD}"

# 8. tree diff recorded per call with matching hash; binary vcs stamp or UNKNOWN
WS=${ROOT}/ws8; mkdir -p "${WS}"
git clone -q "${URL}" "${WS}/r"
(
  export GITHUB_WORKSPACE=${WS} GOTOOLCHAIN=local GOFLAGS=-mod=mod GOPROXY=off
  # shellcheck disable=SC1090
  source "${SCRIPT}" >/dev/null
  set -e
  echo patched >"${WS}/r/f"
  record_tree_diff fxd "${WS}/r"
  echo again >>"${WS}/r/f"; touch "${WS}/r/untracked"
  record_tree_diff fxd "${WS}/r"
  mkdir -p "${ROOT}/bin1" && printf 'package main\nfunc main() {}\n' >"${ROOT}/bin1/main.go"
  (cd "${ROOT}/bin1" && go build -o b main.go)
  record_binary nostamp "${ROOT}/bin1/b"
  git init -q "${ROOT}/bin2"
  (cd "${ROOT}/bin2" && printf 'module fxbin\n\ngo 1.23\n' >go.mod && cp "${ROOT}/bin1/main.go" . && git add . && git commit -qm b && go build -o b .)
  record_binary stamped "${ROOT}/bin2/b"
  record_binary missing "${ROOT}/nope"
  echo notgo >"${ROOT}/notgo"
  record_binary notgo "${ROOT}/notgo"
) || bad "diff/binary block exited nonzero"
MD=${WS}/.github/action-logs/e2e-metadata.txt
D2=$(git -C "${WS}/r" diff --binary HEAD | sha256sum | cut -d ' ' -f 1)
check "tree diff: two entries" "grep -q '^fxd.prepare.1.diff_sha256=' ${MD} && grep -qx 'fxd.prepare.2.diff_sha256=${D2}' ${MD}"
check "tree diff: diff file has both headers" "[ \"\$(grep -c '^### fxd prepare' ${WS}/.github/action-logs/fxd-prepare.diff)\" = 2 ]"
check "tree diff: untracked counted" "grep -qx 'fxd.prepare.2.untracked_count=1' ${MD}"
check "binary: no vcs stamp -> UNKNOWN" "grep -qx 'nostamp.binary_vcs_revision=UNKNOWN' ${MD}"
check "binary: vcs stamp recorded" "grep -qx \"stamped.binary_vcs_revision=\$(git -C ${ROOT}/bin2 rev-parse HEAD)\" ${MD}"
check "binary: sha256 recorded" "grep -qx \"stamped.binary_sha256=\$(sha256sum ${ROOT}/bin2/b | cut -d ' ' -f 1)\" ${MD}"
check "binary: missing -> MISSING" "grep -qx 'missing.binary=MISSING' ${MD}"
check "binary: no buildinfo -> go_version UNKNOWN" "grep -qx 'notgo.binary_go_version=UNKNOWN' ${MD}"

# 8b. evidence write failure warns but never aborts a set -e test step
out=$(
  export GITHUB_WORKSPACE=${ROOT}/ws8 E2E_EVIDENCE_DIR=/proc/nonexistent/evidence
  # shellcheck disable=SC1090
  source "${SCRIPT}" >/dev/null
  set -e
  record_evidence k v
  record_tree_diff fxd "${ROOT}/ws8/r"
  echo AFTER
) ; rc=$?
check "evidence unwritable: step continues" "[ ${rc} -eq 0 ] && grep -q AFTER <<<\"\${out}\""

# 9. no secrets in evidence even when keys are in the environment
SECRET=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
(
  export PRIVATE_KEY=${SECRET} ACCOUNT_ADDR=0x0000000000000000000000000000000000000001
  cd "${ROOT}/spclone" && GITHUB_WORKSPACE=${ROOT}/spclone bash "${SCRIPT}" --recordMetadata >/dev/null
)
check "secrets: generated key not in any evidence file" "! grep -rqF ${SECRET} ${ROOT}/spclone/.github/action-logs ${ROOT}/ws*/.github/action-logs"
check "secrets: dev key not in any evidence file" "! grep -rqF 2228e392584d902843272c37fd62b8c73c10c81a5ecb901773c9ebe366e937bb ${ROOT}/spclone/.github/action-logs ${ROOT}/ws8/.github/action-logs ${ROOT}/ws1/.github/action-logs"

# 10. Go 1.26 dependency pin: exact versions, edit -> tidy -> check order, a
#     graph that selects anything else fails before make build. go and make
#     are stubs that log their argv; they prove ordering and exit propagation
#     only (the real builds are checked separately with Go 1.26.10).
STUB=${ROOT}/stub
mkdir -p "${STUB}"
cat >"${STUB}/go" <<'GO'
#!/usr/bin/env bash
echo "go $*" >>"${FX_LOG}"
case "$*" in
"list -m -f {{.Version}} github.com/bytedance/sonic")
  if [ -n "${FX_CMD_SONIC}" ] && [ "$(basename "${PWD}")" = moca-cmd ]; then echo "${FX_CMD_SONIC}"; else echo "${FX_SONIC:-v1.15.1}"; fi ;;
"list -m -f {{.Version}} github.com/bytedance/sonic/loader") echo "${FX_LOADER:-v0.5.1}" ;;
"list -m all") printf 'fx\ngithub.com/bytedance/sonic %s\ngithub.com/stays/same v1.0.0\n%b' "${FX_SONIC:-v1.15.1}" "${FX_AFTER_EXTRA}" ;;
"list -mod=mod -modfile="*" -m all")
  # the pre-pin graph: sonic v1.14.0 and a module only it pulled in
  case "$*" in *"/go.mod -m all") ;; *) exit 3 ;; esac
  [ "${3#-modfile=}" != "${PWD}/go.mod" ] || exit 4
  printf 'fx\ngithub.com/bytedance/sonic v1.14.0\ngithub.com/cloudwego/iasm v0.2.0\ngithub.com/stays/same v1.0.0\n' ;;
"env GOVERSION") echo go1.26.10 ;;
"mod edit -json") echo '{"Replace":[{"Old":{"Path":"github.com/mocachain/moca-go-sdk"},"New":{"Path":"LOCAL"}}]}' ;;
esac
exit 0
GO
cat >"${STUB}/make" <<'MK'
#!/usr/bin/env bash
echo "make $*" >>"${FX_LOG}"
MK
chmod +x "${STUB}/go" "${STUB}/make"

# go-sdk remote whose sources already carry the harness patches (so the patch
# step is a no-op and only the pin is under test)
SDKSRC=${ROOT}/sdksrc
git init -q -b main "${SDKSRC}"
python3 - "${SCRIPT}" "${SDKSRC}" <<'PY'
import ast, sys
from pathlib import Path
text = Path(sys.argv[1]).read_text()
start = text.index("function prepare_moca_go_sdk() {")
line = next(l for l in text[start:].splitlines() if l.startswith('new = """'))
patched = ast.literal_eval(line[len("new = "):])
root = Path(sys.argv[2])
(root / "client").mkdir()
(root / "client/api_client.go").write_text("package client\n" + patched)
(root / "e2e/basesuite").mkdir(parents=True)
(root / "e2e/basesuite/suite.go").write_text('package basesuite\nvar o = Option{\n\t\tHost:           "gnfd.test-sp.com",\n}\n')
(root / "go.mod").write_text("module github.com/mocachain/moca-go-sdk\n\ngo 1.23\n")
PY
(cd "${SDKSRC}" && git add . && git commit -qm sdk)
SDKC=$(git -C "${SDKSRC}" rev-parse HEAD)
git clone -q --bare "${SDKSRC}" "${ROOT}/sdk.git"
deps_run() { # ws fn [env...]: run a harness function with stub go/make, print rc
  local ws=$1 fn=$2
  shift 2
  mkdir -p "${ws}"
  (
    # "$@" carries extra VAR=value pairs for the stubs
    # shellcheck disable=SC2163
    export GITHUB_WORKSPACE=${ws} FX_LOG=${ws}/calls.log PATH=${STUB}:${PATH} "$@"
    export MOCA_GO_SDK_TAG=${SDKC} MOCA_CMD_TAG=${C3}
    export GIT_CONFIG_COUNT=2 \
      GIT_CONFIG_KEY_0="url.file://${ROOT}/sdk.git.insteadOf" GIT_CONFIG_VALUE_0=https://github.com/mocachain/moca-go-sdk.git \
      GIT_CONFIG_KEY_1="url.${URL}.insteadOf" GIT_CONFIG_VALUE_1=https://github.com/mocachain/moca-cmd.git
    cd "${ws}" || exit 99
    # shellcheck disable=SC1090
    source "${SCRIPT}" >/dev/null
    "${fn}"
  ) >"${ws}/out.log" 2>&1
}
calls_order() { # ws: positions of the key calls, comma separated
  grep -n -E -x -e 'go list -mod=mod -modfile=.* -m all' -e 'go mod edit -replace .*' -e 'go mod edit -require=github.com/bytedance/sonic@v1.15.1 -require=github.com/bytedance/sonic/loader@v0.5.1' \
    -e 'go mod tidy' -e 'go list -m -f \{\{.Version\}\} github.com/bytedance/sonic' -e 'make build' "$1/calls.log" |
    sed -E 's/^[0-9]+:go list -mod=mod.*/base/; s/^[0-9]+:go mod edit -replace.*/replace/; s/^[0-9]+:go mod edit -require.*/pin/; s/^[0-9]+:go mod tidy/tidy/; s/^[0-9]+:go list.*/check/; s/^[0-9]+:make build/make/' | paste -sd ',' -
}

WS=${ROOT}/ws10
deps_run "${WS}" prepare_moca_go_sdk; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "sdk pin: prepare exit 0" "[ ${rc} -eq 0 ]"
check "sdk pin: pre-pin graph from a temp modfile, exact pin, tidy, selected check" "[ \"\$(calls_order ${WS})\" = base,pin,tidy,check ]"
check "sdk pin: never @latest, -u or go get" "! grep -q -E '@latest|-u( |$)|^go get' ${WS}/calls.log"
check "sdk pin: selected versions and Go version recorded, check PASS" "grep -qx 'sdk.sonic_selected=v1.15.1' ${MD} && grep -qx 'sdk.sonic_loader_selected=v0.5.1' ${MD} && grep -qx 'sdk.go_version=go1.26.10' ${MD} && grep -qx 'sdk.go126_deps_check=PASS' ${MD}"
check "sdk pin: check recorded before the prepare diff" "[ \"\$(grep -n -E '^sdk\.(go126_deps_check|prepare\.1\.diff_sha256)=' ${MD} | cut -d: -f2 | cut -d= -f1 | paste -sd,)\" = sdk.go126_deps_check,sdk.prepare.1.diff_sha256 ]"

WS=${ROOT}/ws10b
deps_run "${WS}" prepare_moca_go_sdk FX_SONIC=v1.15.2; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "sdk pin: graph selects another sonic -> nonzero, FAIL recorded, no prepare diff" "[ ${rc} -ne 0 ] && grep -qx 'sdk.go126_deps_check=FAIL' ${MD} && ! grep -q '^sdk.prepare' ${MD} && grep -q 'selected sonic v1.15.2' ${WS}/out.log"

WS=${ROOT}/ws10g
deps_run "${WS}" prepare_moca_go_sdk FX_AFTER_EXTRA='github.com/unrelated/bump v9.9.9\n'; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "sdk drift: a module outside the allowed set appears -> nonzero, FAIL, drift listed" "[ ${rc} -ne 0 ] && grep -qx 'sdk.go126_deps_check=FAIL' ${MD} && grep -qx 'sdk.go126_unexpected_drift=1' ${MD} && grep -q 'github.com/unrelated/bump - -> v9.9.9' ${WS}/out.log && ! grep -q '^sdk.prepare' ${MD}"

WS=${ROOT}/ws10h
deps_run "${WS}" prepare_moca_go_sdk FX_AFTER_EXTRA='github.com/cloudwego/base64x v0.1.7\n'; rc=$?
check "sdk drift: an allowed module at another version -> nonzero" "[ ${rc} -ne 0 ] && grep -q 'github.com/cloudwego/base64x - -> v0.1.7' ${WS}/out.log"

WS=${ROOT}/ws10i
deps_run "${WS}" prepare_moca_go_sdk FX_AFTER_EXTRA='github.com/cloudwego/base64x v0.1.6\ngithub.com/bytedance/gopkg v0.1.3\n'; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "sdk drift: the expected sonic, gopkg and base64x moves and iasm drop pass, both graph hashes recorded" "[ ${rc} -eq 0 ] && grep -qx 'sdk.go126_deps_check=PASS' ${MD} && grep -q '^sdk.module_graph_before_sha256=' ${MD} && grep -q '^sdk.module_graph_sha256=' ${MD}"

WS=${ROOT}/ws10c
deps_run "${WS}" prepare_moca_go_sdk FX_LOADER=v0.5.2; rc=$?
check "sdk pin: graph selects another loader -> nonzero" "[ ${rc} -ne 0 ] && grep -qx 'sdk.go126_deps_check=FAIL' ${WS}/.github/action-logs/e2e-metadata.txt"

build_cmd_prefix() { prepare_moca_go_sdk && prepare_moca_cmd; }
WS=${ROOT}/ws10d
deps_run "${WS}" build_cmd_prefix; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "cmd pin: exit 0, both prepares ran" "[ ${rc} -eq 0 ] && grep -qx 'sdk.go126_deps_check=PASS' ${MD} && grep -qx 'cmd.go126_deps_check=PASS' ${MD}"
check "cmd pin: sdk base/pin/tidy/check, then cmd replace, base, pin, tidy, check, make build" "[ \"\$(calls_order ${WS})\" = base,pin,tidy,check,replace,base,pin,tidy,check,make ]"
check "cmd pin: cmd check recorded before its prepare diff and binary" "[ \"\$(grep -n -E '^cmd\.(go126_deps_check|prepare\.1\.diff_sha256|moca-cmd\.binary)=' ${MD} | cut -d: -f2 | cut -d= -f1 | paste -sd,)\" = cmd.go126_deps_check,cmd.prepare.1.diff_sha256,cmd.moca-cmd.binary ]"

WS=${ROOT}/ws10e
deps_run "${WS}" build_cmd_prefix FX_CMD_SONIC=v1.16.0; rc=$?
MD=${WS}/.github/action-logs/e2e-metadata.txt
check "cmd pin: sdk passes but cmd graph selects another sonic -> nonzero, make build never reached" "[ ${rc} -ne 0 ] && grep -qx 'sdk.go126_deps_check=PASS' ${MD} && grep -qx 'cmd.go126_deps_check=FAIL' ${MD} && ! grep -qx 'make build' ${WS}/calls.log"

# through the real --buildCmd entrypoint and a workflow-style tee pipeline:
# a failing check must fail the step with pipefail before make build
WS=${ROOT}/ws10f
mkdir -p "${WS}"
(
  export GITHUB_WORKSPACE=${WS} FX_LOG=${WS}/calls.log PATH=${STUB}:${PATH} FX_SONIC=v1.14.0
  export MOCA_GO_SDK_TAG=${SDKC} MOCA_CMD_TAG=${C3}
  export GIT_CONFIG_COUNT=2 \
    GIT_CONFIG_KEY_0="url.file://${ROOT}/sdk.git.insteadOf" GIT_CONFIG_VALUE_0=https://github.com/mocachain/moca-go-sdk.git \
    GIT_CONFIG_KEY_1="url.${URL}.insteadOf" GIT_CONFIG_VALUE_1=https://github.com/mocachain/moca-cmd.git
  cd "${WS}" && bash --noprofile --norc -eo pipefail -c 'mkdir -p .github/action-logs; set -o pipefail; bash "$0" --buildCmd 2>&1 | tee .github/action-logs/sp-e2e-build-cmd.log' "${SCRIPT}"
) >/dev/null 2>&1; rc=$?
check "buildCmd entrypoint: unpinned graph fails the tee'd step, make never reached" "[ ${rc} -ne 0 ] && ! grep -qx 'make build' ${WS}/calls.log && grep -q 'selected sonic v1.14.0' ${WS}/.github/action-logs/sp-e2e-build-cmd.log"

echo "TOTAL PASS=${PASS} FAIL=${FAIL}"
echo "fixture root: ${ROOT}"
[ "${FAIL}" -eq 0 ]
