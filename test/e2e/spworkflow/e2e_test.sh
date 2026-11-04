#!/usr/bin/env bash

export CGO_CFLAGS="-O -D__BLST_PORTABLE__"
export CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

workspace=${GITHUB_WORKSPACE}

# some constants
# release/1.4.x continues the 1.2.x ecosystem line, so the e2e stack pins the
# chain to its paired release branch and cmd/go-sdk to the last 1.2.x tags
# instead of main. Refs stay override-friendly for ad-hoc runs.
MOCA_TAG="${MOCA_TAG:-release/v1.4.x}"
MOCA_CMD_TAG="${MOCA_CMD_TAG:-v1.2.0-rc1}"
MOCA_GO_SDK_TAG="${MOCA_GO_SDK_TAG:-v1.2.0-rc1}"
MYSQL_USER="root"
MYSQL_PASSWORD="root"
MYSQL_ADDRESS="127.0.0.1:3306"
TEST_ACCOUNT_ADDRESS=${ACCOUNT_ADDR}
TEST_ACCOUNT_PRIVATE_KEY=${PRIVATE_KEY}
DEV_ACCOUNT_PRIVATE_KEY="2228e392584d902843272c37fd62b8c73c10c81a5ecb901773c9ebe366e937bb"
echo "TEST_ACCOUNT_ADDRESS is ""$TEST_ACCOUNT_ADDRESS"
# NOTE: do not print the test key: step output goes to the public step log and
# is tee'd into .github/action-logs/*.log, of which --redactEvidence uploads only
# redacted copies. key_log_fixture_test.py covers this script's own output.

BUCKET_NAME="spbucket"
SP_REQUEST_HOST="${SP_REQUEST_HOST:-gnfd.test-sp.com}"
E2E_SP_NUM=8

function dump_sp_logs() {
  if [ ! -d "${workspace}/deployment/localup/local_env" ]; then
    return
  fi

  for sp_dir in "${workspace}"/deployment/localup/local_env/sp*; do
    if [ ! -d "${sp_dir}" ] || [ ! -f "${sp_dir}/log.txt" ]; then
      continue
    fi

    echo "===== $(basename "${sp_dir}") log tail ====="
    tail -n 200 "${sp_dir}/log.txt"
  done
}

function start_sp_stack() {
  local max_attempts=3
  local attempt

  for attempt in $(seq 1 ${max_attempts}); do
    echo "start storage providers attempt ${attempt}/${max_attempts}"
    if bash ./deployment/localup/localup.sh start; then
      return 0
    fi

    echo "storage providers failed to start on attempt ${attempt}"
    dump_sp_logs
    bash ./deployment/localup/localup.sh stop || true
    sleep 5
  done

  echo "storage providers failed to start after ${max_attempts} attempts"
  return 1
}

function update_sp_quota() {
  local sp_name=$1
  local sp_bin=$2
  local sp_config=$3
  local max_attempts=12
  local attempt

  for attempt in $(seq 1 ${max_attempts}); do
    if "${sp_bin}" update.quota --quota 5000000000 -c "${sp_config}"; then
      echo "updated quota for ${sp_name}"
      return 0
    fi

    echo "quota update for ${sp_name} failed on attempt ${attempt}/${max_attempts}"
    sleep 10
  done

  echo "quota update for ${sp_name} failed after ${max_attempts} attempts"
  test -f "${sp_config%/*}/log.txt" && tail -n 200 "${sp_config%/*}/log.txt"
  return 1
}

function retry_cmd() {
  local max_attempts=$1
  local sleep_seconds=$2
  local description=$3
  shift 3
  local attempt

  for attempt in $(seq 1 "${max_attempts}"); do
    if "$@"; then
      echo "${description} succeeded"
      return 0
    fi

    echo "${description} failed on attempt ${attempt}/${max_attempts}"
    if [ "${attempt}" -lt "${max_attempts}" ]; then
      sleep "${sleep_seconds}"
    fi
  done

  echo "${description} failed after ${max_attempts} attempts"
  dump_sp_logs
  return 1
}

function select_exit_sp_dir() {
  local sp_dir

  for sp_dir in "${workspace}"/deployment/localup/local_env/sp*; do
    if [ ! -d "${sp_dir}" ]; then
      continue
    fi

    if [ "$(basename "${sp_dir}")" = "sp0" ]; then
      continue
    fi

    echo "${sp_dir}"
    return 0
  done

  echo "no non-primary storage provider directory found under ${workspace}/deployment/localup/local_env" >&2
  ls -la "${workspace}"/deployment/localup/local_env || true
  return 1
}

#########################################
# e2e evidence (non-sensitive only)     #
#########################################
# Records what was actually tested: refs, resolved SHAs, local patch diffs,
# go.mod/go.sum digests and binary hashes. Only write values that are safe to
# publish as a CI artifact: never keys, mnemonics, keystores, passwords,
# config files or environment dumps.
E2E_EVIDENCE_DIR="${E2E_EVIDENCE_DIR:-${workspace}/.github/action-logs}"
E2E_METADATA_FILE="${E2E_EVIDENCE_DIR}/e2e-metadata.txt"

# evidence is best effort: a failed write warns and never aborts the test
function record_evidence() {
  if ! { mkdir -p "${E2E_EVIDENCE_DIR}" && printf '%s=%s\n' "$1" "$2" >>"${E2E_METADATA_FILE}"; } 2>/dev/null; then
    echo "warning: could not record e2e evidence $1" >&2
  fi
  return 0
}

function is_full_sha() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]]
}

function file_sha256() {
  if [ -f "$1" ]; then
    sha256sum "$1" | cut -d ' ' -f 1
  else
    echo "UNKNOWN"
  fi
}

function record_go_mod_digest() {
  local label=$1
  local mod_dir=$2

  record_evidence "${label}.go_mod_sha256" "$(file_sha256 "${mod_dir}/go.mod")"
  record_evidence "${label}.go_sum_sha256" "$(file_sha256 "${mod_dir}/go.sum")"
}

# record the tracked local changes the harness applied on top of a checkout
# (appended per call, so a repeated prepare is recorded every time)
function record_tree_diff() {
  local label=$1
  local repo_dir=$2
  local diff_file="${E2E_EVIDENCE_DIR}/${label}-prepare.diff"
  local count base_sha diff_tmp diff_sha="UNKNOWN" files="UNKNOWN"

  mkdir -p "${E2E_EVIDENCE_DIR}" 2>/dev/null || true
  count=$(grep -c "^${label}\.prepare\.[0-9]*\.diff_sha256=" "${E2E_METADATA_FILE}" 2>/dev/null || true)
  count=$((${count:-0} + 1))
  base_sha=$(git -C "${repo_dir}" rev-parse HEAD 2>/dev/null || echo UNKNOWN)
  if ! diff_tmp=$(mktemp 2>/dev/null); then
    echo "warning: could not create a temp file for the ${label} diff" >&2
    record_evidence "${label}.prepare.${count}.base_sha" "${base_sha}"
    record_evidence "${label}.prepare.${count}.diff_sha256" "UNKNOWN"
    return 0
  fi
  if git -C "${repo_dir}" diff --binary HEAD >"${diff_tmp}"; then
    diff_sha=$(sha256sum "${diff_tmp}" | cut -d ' ' -f 1)
    files=$(git -C "${repo_dir}" diff --name-only HEAD | paste -sd ',' -)
  fi
  {
    echo "### ${label} prepare ${count}: base ${base_sha}"
    cat "${diff_tmp}"
  } >>"${diff_file}" 2>/dev/null || echo "warning: could not write ${diff_file}" >&2
  rm -f "${diff_tmp}"
  record_evidence "${label}.prepare.${count}.base_sha" "${base_sha}"
  record_evidence "${label}.prepare.${count}.files" "${files}"
  record_evidence "${label}.prepare.${count}.diff_sha256" "${diff_sha}"
  record_evidence "${label}.prepare.${count}.untracked_count" "$(git -C "${repo_dir}" ls-files --others --exclude-standard 2>/dev/null | wc -l | tr -d ' ')"
}

# record a built binary's sha256 and the vcs stamp Go embedded, if any
function record_binary() {
  local label=$1
  local bin=$2
  local buildinfo go_version revision modified

  if [ ! -f "${bin}" ]; then
    record_evidence "${label}.binary" "MISSING"
    return 0
  fi
  buildinfo=$(go version -m "${bin}" 2>/dev/null || true)
  go_version=$(printf '%s\n' "${buildinfo}" | head -n 1 | awk '{ print $2 }')
  revision=$(printf '%s\n' "${buildinfo}" | awk '$1 == "build" && $2 ~ /^vcs\.revision=/ { sub(/^vcs\.revision=/, "", $2); print $2 }')
  modified=$(printf '%s\n' "${buildinfo}" | awk '$1 == "build" && $2 ~ /^vcs\.modified=/ { sub(/^vcs\.modified=/, "", $2); print $2 }')
  record_evidence "${label}.binary_sha256" "$(file_sha256 "${bin}")"
  record_evidence "${label}.binary_go_version" "${go_version:-UNKNOWN}"
  record_evidence "${label}.binary_vcs_revision" "${revision:-UNKNOWN}"
  record_evidence "${label}.binary_vcs_modified" "${modified:-UNKNOWN}"
}

# check out repo_ref and fail before anything is built when the checkout does
# not match the expected commit. The expected commit is expected_sha, or
# repo_ref itself when it is a full 40-char SHA. Pass a tag/branch together
# with expected_sha when a server refuses to fetch by SHA.
function sync_repo_ref() {
  local repo_url=$1
  local repo_dir=$2
  local repo_ref=$3
  local expected_sha=${4:-}
  local label=${5:-$(basename "${repo_dir}")}
  local resolved_sha

  if [ -z "${expected_sha}" ] && is_full_sha "${repo_ref}"; then
    expected_sha=${repo_ref}
  fi

  cd "${workspace}" || return 1
  if [ ! -d "${repo_dir}/.git" ]; then
    git clone "${repo_url}" "${repo_dir}" || return 1
  fi

  cd "${repo_dir}" || return 1
  git fetch --tags --prune origin "${repo_ref}" || return 1
  git checkout -B codex-ci-ref FETCH_HEAD || return 1
  resolved_sha=$(git rev-parse HEAD) || return 1

  record_evidence "${label}.repo" "${repo_url}"
  record_evidence "${label}.ref" "${repo_ref}"
  record_evidence "${label}.expected_sha" "${expected_sha:-UNPINNED}"
  record_evidence "${label}.resolved_sha" "${resolved_sha}"
  if [ -n "${expected_sha}" ] && [ "${resolved_sha}" != "${expected_sha}" ]; then
    echo "${label}: ${repo_ref} resolved to ${resolved_sha}, expected ${expected_sha}" >&2
    record_evidence "${label}.ref_check" "FAIL"
    return 1
  fi
  record_evidence "${label}.ref_check" "$([ -n "${expected_sha}" ] && echo PASS || echo UNPINNED)"
}

# job-level facts: which SP commit the runner actually tested (for PRs this is
# GitHub's synthetic merge; check its parents against the frozen base/head via
# the GitHub commit API) and which runner image ran it
function record_run_metadata() {
  local parent index=0

  cd "${workspace}" || return 1
  record_evidence "run.repository" "${GITHUB_REPOSITORY:-UNKNOWN}"
  record_evidence "run.workflow" "${GITHUB_WORKFLOW:-UNKNOWN}"
  record_evidence "run.job" "${GITHUB_JOB:-UNKNOWN}"
  record_evidence "run.id" "${GITHUB_RUN_ID:-UNKNOWN}"
  record_evidence "run.attempt" "${GITHUB_RUN_ATTEMPT:-UNKNOWN}"
  record_evidence "run.event" "${GITHUB_EVENT_NAME:-UNKNOWN}"
  record_evidence "run.github_sha" "${GITHUB_SHA:-UNKNOWN}"
  record_evidence "run.pr_number" "${E2E_PR_NUMBER:-UNKNOWN}"
  record_evidence "run.pr_head_sha" "${E2E_PR_HEAD_SHA:-UNKNOWN}"
  record_evidence "run.pr_base_sha" "${E2E_PR_BASE_SHA:-UNKNOWN}"
  record_evidence "runner.os" "${RUNNER_OS:-UNKNOWN}"
  record_evidence "runner.image_os" "${ImageOS:-UNKNOWN}"
  record_evidence "runner.image_version" "${ImageVersion:-UNKNOWN}"
  record_evidence "sp.checkout_sha" "$(git rev-parse HEAD 2>/dev/null || echo UNKNOWN)"
  record_evidence "sp.checkout_tree" "$(git rev-parse 'HEAD^{tree}' 2>/dev/null || echo UNKNOWN)"
  # read parents from the raw commit object: a depth-1 checkout has no parent
  # objects, so HEAD^2 and git log cannot be used here
  while read -r parent; do
    index=$((index + 1))
    record_evidence "sp.checkout_parent.${index}" "${parent}"
  done < <(git cat-file -p HEAD 2>/dev/null | awk '$1 == "parent" { print $2 } /^$/ { exit }')
  record_evidence "sp.checkout_parent_count" "${index}"
  record_go_mod_digest sp "${workspace}"
}

# Build the upload bundle for one job profile. Runs as its own always() step
# after the test steps, so it never changes a test step's exit status; the
# upload step only runs when this succeeds and uploads only the published
# bundle directory.
#
# Each profile names a fixed allowlist of files: the job's metadata, prepare
# diffs and tee'd step logs, the SP log.txt files and the validator node.log
# (BlockSyncer: metadata, its tee log and coverage.txt). Nothing else is ever
# opened: there is no recursive walk of the run tree, so SP data/ (database
# WAL), config files, keystores, account dirs and symlinks are never read, and
# the run tree is never modified. Each listed file is opened once (regular
# files inside the workspace only, symlinks refused) and exactly the bytes it
# held at open time are copied into a fresh staging directory, so a file that
# grows, rotates or is deleted afterwards cannot change the bundle.
# The static copy is scrubbed and re-read; only a fully verified copy is
# renamed to the published bundle, so a failed run leaves no bundle at all.
#
# Known secrets (the per-run test key, the dev key, the localup SP keys in
# sp.json and the suite's info/challenger_info files) are replaced wherever
# they appear; long hex values and 12-24 word runs on lines that name a key or
# mnemonic, and bare mnemonic lines, are replaced too. A key source that exists
# but cannot be read or parsed fails the step; one that was never generated is
# recorded as unavailable. The metadata file and the tee'd log of every step
# that ran are required; a missing required file fails the step. Any other
# missing file (a step that did not run, or a log it never wrote) is recorded
# as missing with evidence UNKNOWN and its step's outcome. Budget: 20 MiB per
# file, 100 MiB in total, 120 seconds; over budget fails closed (nothing is
# truncated). manifest.txt lists every file as captured, missing or failed,
# with its stage outcome, size and sha256 after redaction.
function redact_evidence() {
  local profile=$1

  cd "${workspace}" || return 1
  E2E_REDACT_KEY_1="${TEST_ACCOUNT_PRIVATE_KEY}" E2E_REDACT_KEY_2="${DEV_ACCOUNT_PRIVATE_KEY}" \
    python3 -I - "${profile}" <<'PY'
import errno
import hashlib
import json
import os
import re
import shutil
import signal
import stat
import sys
import time
from pathlib import Path

MIB = 1024 * 1024

ACTION_LOGS = ".github/action-logs"
METADATA = f"{ACTION_LOGS}/e2e-metadata.txt"
SP_LOGS = [f"deployment/localup/local_env/sp{i}/log.txt" for i in range(8)]
NODE_LOG = "moca/deployment/localup/.local/validator0/logs/node.log"


def chain_job(prefix, stages, diffs):
    # (path, stage producing it or None, required once that stage ran)
    files = [(METADATA, None, True)]
    files += [(f"{ACTION_LOGS}/{name}-prepare.diff", stage, False) for name, stage in diffs]
    files += [(f"{ACTION_LOGS}/{prefix}-{stage}.log", stage, True) for stage in stages]
    files += [(path, "start-sp", False) for path in SP_LOGS]
    files.append((NODE_LOG, "start-chain", False))
    return files


SP_STAGES = ["start-chain", "start-sp", "build-cmd", "run-test"]
PROFILES = {
    "sp-e2e": chain_job("sp-e2e", SP_STAGES, [("sdk", "build-cmd"), ("cmd", "build-cmd")]),
    "sp-exit": chain_job("sp-exit", SP_STAGES, [("sdk", "build-cmd"), ("cmd", "build-cmd")]),
    "sdk-e2e": chain_job("sdk-e2e", ["start-chain", "start-sp", "run-test"], [("sdk", "run-test")]),
    "bs-e2e": [
        (METADATA, None, True),
        (f"{ACTION_LOGS}/bs-e2e-run-test.log", "run-test", True),
        ("modular/blocksyncer/coverage.txt", "run-test", False),
    ],
}

MARK = "[REDACTED]"
KEY_WORD = re.compile(r"(?i)priv(ate)?[ _-]?key|mnemonic|seed[ _-]?phrase|secret[ _-]?key")
LONG_HEX = re.compile(r"(?i)\b(0x)?[0-9a-f]{32,}\b")
WORD_LINE = re.compile(r"^\s*([a-z]+\s+){11,23}[a-z]+\s*$")
# a 12-24 word mnemonic inside a line that names a key or mnemonic
WORD_RUN = re.compile(r"\b([a-z]+[ \t]+){11,23}[a-z]+\b")


class Refused(Exception):
    pass


def fail(message):
    print(f"evidence bundle not published: {message}", file=sys.stderr)
    sys.exit(1)


WORKSPACE = Path.cwd().resolve()
# fixed location, not configurable: the cleanup below deletes it recursively
BUNDLE = WORKSPACE / ".github/e2e-diagnostics"
STAGING = BUNDLE.with_name(BUNDLE.name + ".staging")
START = time.monotonic()


def remove(path):
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.exists():
        shutil.rmtree(path)


# the cleanup and every later write go through BUNDLE's parent, so it must be
# a real directory inside the workspace before anything is deleted: a
# symlinked .github would point the rmtree below at a directory elsewhere
try:
    st = os.lstat(BUNDLE.parent)
except FileNotFoundError:
    st = None
if st is not None and not stat.S_ISDIR(st.st_mode):
    kind = "a symlink" if stat.S_ISLNK(st.st_mode) else "not a directory"
    fail(f"{BUNDLE.parent.relative_to(WORKSPACE)} is {kind}; nothing removed or written")

# never leave an earlier bundle behind for the upload step to pick up, even
# when the arguments or budgets below are rejected
remove(BUNDLE)
remove(STAGING)

if len(sys.argv) != 2 or sys.argv[1] not in PROFILES:
    fail(f"unknown profile {sys.argv[1:]!r}; expected one of {sorted(PROFILES)}")
PROFILE = sys.argv[1]


def budget(name, cap, kind):
    """The environment can only tighten a budget (the offline fixtures do)."""
    raw = os.environ.get(name)
    if raw is None:
        return cap
    try:
        value = kind(raw)
    except ValueError:
        fail(f"{name}={raw!r} is not a number")
    if not value > 0:
        fail(f"{name}={raw!r} must be positive")
    return min(cap, value)


FILE_LIMIT = budget("E2E_BUNDLE_FILE_LIMIT", 20 * MIB, int)
TOTAL_LIMIT = budget("E2E_BUNDLE_TOTAL_LIMIT", 100 * MIB, int)
TIME_LIMIT = budget("E2E_BUNDLE_TIME_LIMIT", 120.0, float)


def budget_left(limit):
    if limit < FILE_LIMIT:
        return f"{TOTAL_LIMIT} byte total budget ({limit} bytes left)"
    return f"{FILE_LIMIT} byte budget"


def on_alarm(signum, frame):
    remove(STAGING)
    fail(f"over the {TIME_LIMIT:g}s time budget")


signal.signal(signal.SIGALRM, on_alarm)
signal.setitimer(signal.ITIMER_REAL, TIME_LIMIT)


def snapshot(rel, limit):
    """Bytes the allowlisted file held when it was opened; None if absent.

    Every parent must be a real directory inside the workspace and the file a
    regular file; symlinks are refused, never followed.
    """
    path = Path(rel)
    if path.is_absolute() or ".." in path.parts:
        raise Refused("path escapes the workspace")
    current = WORKSPACE
    for part in path.parts[:-1]:
        current = current / part
        try:
            st = os.lstat(current)
        except FileNotFoundError:
            return None
        if stat.S_ISLNK(st.st_mode):
            raise Refused(f"symlinked directory {current.relative_to(WORKSPACE)}")
        if not stat.S_ISDIR(st.st_mode):
            raise Refused(f"{current.relative_to(WORKSPACE)} is not a directory")
    try:
        fd = os.open(WORKSPACE / path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    except FileNotFoundError:
        return None
    except OSError as err:
        if err.errno == errno.ELOOP:
            raise Refused("symlink")
        raise Refused(f"cannot open ({errno.errorcode.get(err.errno, err.errno)})")
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise Refused("not a regular file")
        size = st.st_size
        if size > limit:
            raise Refused(f"{size} bytes is over the {budget_left(limit)}")
        chunks, remaining = [], size
        while remaining:
            try:
                chunk = os.read(fd, min(remaining, MIB))
            except OSError as err:
                raise Refused(f"read failed ({errno.errorcode.get(err.errno, err.errno)})")
            if not chunk:
                raise Refused(f"shrank below {size} bytes while it was copied")
            chunks.append(chunk)
            remaining -= len(chunk)
        return b"".join(chunks)
    finally:
        os.close(fd)


secrets = set()
sources = {}


def add_secret(value):
    """Remember a key value; True when it is long enough to be one."""
    if not isinstance(value, str):
        return False
    value = value.strip()
    if value[:2].lower() == "0x":
        value = value[2:]
    if len(value) < 16:
        return False
    secrets.add(value)
    return True


# the two SP key files differ only in how a key is written: sp_export.json is
# export_sps output, one `mocad keys export --unarmored-hex` value per field
# (bare hex, leading zeros may be dropped); moca/sp.json is that file after
# normalize_sp_private_keys, which pads the fields it lists to 64 bare hex
# digits and leaves the rest (FundingPrivateKey) as exported
EXPORTED_KEY = ("1 to 64 hex digits", re.compile(r"[0-9A-Fa-f]{1,64}"))
PADDED_KEY = ("64 hex digits", re.compile(r"[0-9A-Fa-f]{64}"))
PADDED_FIELDS = {"OperatorPrivateKey", "SealPrivateKey", "ApprovalPrivateKey", "GcPrivateKey",
                 "MaintenancePrivateKey", "BlsPrivateKey"}


def key_format(rel, field):
    if rel == "moca/sp.json" and field in PADDED_FIELDS:
        return PADDED_KEY
    return EXPORTED_KEY


def walk_json(node, rel):
    """Collect values under key-named fields; returns how many were keys.

    Every key-named field must hold a key in the format its file is written
    in. Anything else (null, a number, an object, a string that is not hex)
    means the redaction set would be missing that key, so it fails rather
    than being skipped.
    """
    found = 0
    if isinstance(node, dict):
        for key, value in node.items():
            if KEY_WORD.search(str(key)):
                if not isinstance(value, str):
                    fail(f"cannot read SP keys from {rel}: {key} holds a {type(value).__name__}, not a key")
                what, pattern = key_format(rel, str(key))
                if not pattern.fullmatch(value):
                    fail(f"cannot read SP keys from {rel}: {key} is not {what}")
                if not add_secret(value):
                    fail(f"cannot read SP keys from {rel}: {key} is too short to redact")
                found += 1
                continue
            found += walk_json(value, rel)
    elif isinstance(node, list):
        for value in node:
            found += walk_json(value, rel)
    return found


HEX_KEY = re.compile(r"(?i)(0x)?[0-9a-f]{64}")


def is_hex_key(line):
    return bool(HEX_KEY.fullmatch(line.strip()))


def is_mnemonic(line):
    return bool(WORD_LINE.match(line))


def is_secret_line(line):
    return is_hex_key(line) or is_mnemonic(line)


# what prepare_sdk_suite_accounts writes and the go-sdk suite reads (its last
# non-empty line): validator0/info a `mocad keys mnemonic` line,
# challenger0/challenger_info the exported hex key (the patched suite also
# takes a mnemonic there)
KEY_FILE_FORMATS = {"info": ("a mnemonic", is_mnemonic),
                    "challenger_info": ("a hex key or mnemonic", is_secret_line)}


def read_key_source(rel):
    # a key source we cannot read means we cannot know its keys: fail
    try:
        data = snapshot(rel, FILE_LIMIT)
    except Refused as err:
        fail(f"cannot read key source {rel}: {err}")
    if data is None:
        sources[rel] = "unavailable"
        return None
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        fail(f"cannot read key source {rel}: not valid UTF-8")
    sources[rel] = "read"
    return text


add_secret(os.environ.get("E2E_REDACT_KEY_1", ""))
add_secret(os.environ.get("E2E_REDACT_KEY_2", ""))
for rel in ("moca/sp.json", "moca/deployment/localup/.local/sp_export.json"):
    text = read_key_source(rel)
    if text is None:
        continue
    try:
        parsed = json.loads(text)
    except ValueError as err:
        fail(f"cannot read SP keys from {rel}: {type(err).__name__}")
    # both files are export_sps output with *PrivateKey fields; one that
    # parses but holds no key cannot be the file we need to redact against
    if not walk_json(parsed, rel):
        fail(f"cannot read SP keys from {rel}: no private key fields")
LOCAL = "moca/deployment/localup/.local"
local_dir = WORKSPACE / LOCAL
if local_dir.is_symlink():
    fail(f"cannot read key sources: {LOCAL} is a symlink")
if local_dir.is_dir():
    for child in sorted(local_dir.iterdir()):
        for name in ("info", "challenger_info"):
            if os.path.lexists(child / name):
                rel = f"{LOCAL}/{child.name}/{name}"
                text = read_key_source(rel)
                if text is None:
                    continue
                lines = [line for line in text.splitlines() if line.strip()]
                what, valid = KEY_FILE_FORMATS[name]
                if not lines or not valid(lines[-1]):
                    fail(f"cannot read key source {rel}: last line is not {what}")
                for line in lines:
                    if is_secret_line(line):
                        add_secret(line)
# the two files prepare_sdk_suite_accounts writes: say so when a run never got
# that far, instead of leaving them out of the manifest
for rel in (f"{LOCAL}/validator0/info", f"{LOCAL}/challenger0/challenger_info"):
    sources.setdefault(rel, "unavailable")

secret_patterns = [re.compile(r"(?i)(0x)?" + re.escape(s)) for s in sorted(secrets, key=len, reverse=True)]


def scrub_line(line):
    for pattern in secret_patterns:
        line = pattern.sub(MARK, line)
    body = line.rstrip("\r\n")
    if WORD_LINE.match(body):
        return MARK + line[len(body):]
    if KEY_WORD.search(line):
        line = WORD_RUN.sub(MARK, LONG_HEX.sub(MARK, line))
    return line


def leftover(text):
    for pattern in secret_patterns:
        if pattern.search(text):
            return "known secret"
    for line in text.splitlines():
        if WORD_LINE.match(line):
            return "mnemonic line"
        if KEY_WORD.search(line) and (LONG_HEX.search(line) or WORD_RUN.search(line)):
            return "hex value or word run on a key line"
    return None


# the job's own "Record stage results" step appends stage.<id>=<outcome>
stages = {}
try:
    meta = snapshot(METADATA, FILE_LIMIT) or b""
except Refused as err:
    fail(f"{METADATA}: {err}")
for line in meta.decode("latin-1").splitlines():
    key, sep, value = line.partition("=")
    if sep and key.startswith("stage."):
        stages[key[len("stage."):]] = value.strip()

STAGING.mkdir(parents=True)
manifest = [f"profile={PROFILE}", f"limits.file_bytes={FILE_LIMIT}", f"limits.total_bytes={TOTAL_LIMIT}",
            f"limits.seconds={TIME_LIMIT:g}"]
manifest += [f"stage.{key}={value}" for key, value in sorted(stages.items())]
manifest += [f"key_source.{rel}={state}" for rel, state in sorted(sources.items())]
problems = []
total = captured = 0
for rel, stage, at_start in PROFILES[PROFILE]:
    outcome = "always" if stage is None else stages.get(stage, "UNKNOWN")
    ran = stage is None or outcome in ("success", "failure")
    required = stage is None or (at_start and ran)
    entry = f"file.{rel}"
    manifest += [f"{entry}.stage={outcome}", f"{entry}.required={'yes' if required else 'no'}"]
    try:
        data = snapshot(rel, min(FILE_LIMIT, TOTAL_LIMIT - total))
    except Refused as err:
        manifest += [f"{entry}.status=failed", f"{entry}.reason={err}"]
        problems.append(f"{rel}: {err}")
        continue
    if data is None:
        reason = "not produced" if ran else "step did not run"
        manifest += [f"{entry}.status=missing", f"{entry}.reason={reason}",
                     f"{entry}.evidence={'FAIL' if required else 'UNKNOWN'}"]
        if required:
            problems.append(f"{rel}: required but missing")
        continue
    total += len(data)
    raw = data.decode("latin-1")
    scrubbed = "".join(scrub_line(line) for line in raw.splitlines(keepends=True)).encode("latin-1")
    target = STAGING / rel
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(scrubbed)
    written = target.read_bytes()
    problem = leftover(written.decode("latin-1"))
    if written != scrubbed or problem:
        manifest += [f"{entry}.status=failed", f"{entry}.reason=redacted copy did not verify: {problem}"]
        problems.append(f"{rel}: redacted copy did not verify: {problem}")
        continue
    captured += 1
    manifest += [f"{entry}.status=captured", f"{entry}.redacted={'yes' if written != data else 'no'}",
                 f"{entry}.bytes={len(written)}", f"{entry}.sha256={hashlib.sha256(written).hexdigest()}"]

manifest += [f"files.captured={captured}", f"files.source_bytes={total}",
             f"elapsed_seconds={time.monotonic() - START:.1f}"]
if problems:
    remove(STAGING)
    print("\n".join(manifest))
    fail("; ".join(problems))
manifest.append("result=success")
(STAGING / "manifest.txt").write_text("\n".join(manifest) + "\n")
signal.setitimer(signal.ITIMER_REAL, 0)
os.rename(STAGING, BUNDLE)
print("\n".join(manifest))
print(f"published {captured} redacted file(s) against {len(secrets)} known secret(s) to {BUNDLE}")
PY
}

# moca-go-sdk and moca-cmd at their pinned 1.2.x refs require sonic v1.14.0,
# which does not build with Go 1.26: its internal/rt/stubs.go uses
# GoMapIterator, which only its pre-1.26 map files define. Pin the sonic
# release that supports Go 1.26 (the SP module and the chain already select
# these versions) in each consumer's own module graph, then check what the
# graph actually selected. Exact versions only, never @latest or -u.
E2E_SONIC_VERSION="v1.15.1"
E2E_SONIC_LOADER_VERSION="v0.5.1"
function pin_go126_deps() {
  go mod edit \
    -require="github.com/bytedance/sonic@${E2E_SONIC_VERSION}" \
    -require="github.com/bytedance/sonic/loader@${E2E_SONIC_LOADER_VERSION}"
}

# The only modules the pin may move, with the version each must end at ("-":
# the pin drops it): sonic and loader, the two modules sonic v1.15.1 requires
# at a higher version, and the three that only sonic v1.14.0 pulled in. Every
# other module in the graph must keep the version it had before the pin.
E2E_GO126_ALLOWED_DRIFT="github.com/bytedance/sonic=${E2E_SONIC_VERSION} github.com/bytedance/sonic/loader=${E2E_SONIC_LOADER_VERSION} github.com/bytedance/gopkg=v0.1.3 github.com/cloudwego/base64x=v0.1.6 github.com/cloudwego/iasm=- github.com/knz/go-libedit=- nullprogram.com/x/optparse=-"

# run in the module dir before the pin: print the module graph its current
# requirements select, resolved through a throwaway copy of go.mod/go.sum so
# the module's own files are left untouched
function go126_graph_before() {
  local tmp rc=0

  tmp=$(mktemp -d)
  cp go.mod "${tmp}/go.mod"
  if [ -f go.sum ]; then cp go.sum "${tmp}/go.sum"; fi
  go list -mod=mod -modfile="${tmp}/go.mod" -m all || rc=$?
  rm -rf "${tmp}"
  return "${rc}"
}

# run in the module dir after go mod tidy; fails when the graph did not
# select exactly the pinned versions, or when any module outside
# E2E_GO126_ALLOWED_DRIFT changed against the graph from before the pin
function check_go126_deps() {
  local label=$1 before=$2
  local sonic loader graph drift

  sonic=$(go list -m -f '{{.Version}}' github.com/bytedance/sonic) || return 1
  loader=$(go list -m -f '{{.Version}}' github.com/bytedance/sonic/loader) || return 1
  graph=$(go list -m all) || return 1
  drift=$(E2E_GRAPH_BEFORE="${before}" E2E_GRAPH_AFTER="${graph}" E2E_GO126_ALLOWED_DRIFT="${E2E_GO126_ALLOWED_DRIFT}" python3 -I -c '
import os
def graph(name):
    rows = (line.split() for line in os.environ[name].splitlines())
    return {row[0]: row[1] if len(row) > 1 else "" for row in rows if row}
before, after = graph("E2E_GRAPH_BEFORE"), graph("E2E_GRAPH_AFTER")
allowed = dict(item.split("=", 1) for item in os.environ["E2E_GO126_ALLOWED_DRIFT"].split())
for path in sorted(set(before) | set(after)):
    old, new = before.get(path, "-"), after.get(path, "-")
    if old != new and allowed.get(path) != new:
        print("%s %s -> %s" % (path, old, new))
') || return 1
  record_evidence "${label}.go_version" "$(go env GOVERSION)"
  record_evidence "${label}.sonic_selected" "${sonic}"
  record_evidence "${label}.sonic_loader_selected" "${loader}"
  record_evidence "${label}.module_graph_before_sha256" "$(printf '%s\n' "${before}" | sha256sum | cut -d ' ' -f 1)"
  record_evidence "${label}.module_graph_sha256" "$(printf '%s\n' "${graph}" | sha256sum | cut -d ' ' -f 1)"
  # the raw graph names cmd's local go-sdk replace by absolute path, so its
  # hash changes with the runner's workspace; this one is comparable across
  # runners (the workspace prefix is written as the literal ${GITHUB_WORKSPACE})
  local portable=${graph}
  if [ -n "${workspace}" ]; then
    # shellcheck disable=SC2016 # the token is written literally
    portable=${graph//"${workspace}"/'${GITHUB_WORKSPACE}'}
  fi
  record_evidence "${label}.module_graph_portable_sha256" "$(printf '%s\n' "${portable}" | sha256sum | cut -d ' ' -f 1)"
  local ok=1
  if [ "${sonic}" != "${E2E_SONIC_VERSION}" ] || [ "${loader}" != "${E2E_SONIC_LOADER_VERSION}" ]; then
    echo "${label}: selected sonic ${sonic} / loader ${loader}, expected ${E2E_SONIC_VERSION} / ${E2E_SONIC_LOADER_VERSION}" >&2
    ok=0
  fi
  if [ -n "${drift}" ]; then
    echo "${label}: the sonic pin moved modules outside the allowed set:" >&2
    printf '  %s\n' "${drift}" >&2
    record_evidence "${label}.go126_unexpected_drift" "$(printf '%s\n' "${drift}" | wc -l)"
    ok=0
  fi
  if [ "${ok}" -ne 1 ]; then
    record_evidence "${label}.go126_deps_check" "FAIL"
    return 1
  fi
  record_evidence "${label}.go126_deps_check" "PASS"
}

function prepare_moca_go_sdk() {
  set -e
  sync_repo_ref https://github.com/mocachain/moca-go-sdk.git "${workspace}/moca-go-sdk" "${MOCA_GO_SDK_TAG}" "${MOCA_GO_SDK_EXPECTED_SHA:-}" sdk

  SP_REQUEST_HOST="${SP_REQUEST_HOST}" python3 - <<'PY'
import os
from pathlib import Path

api_client = Path("client/api_client.go")
api_client_text = api_client.read_text()
old = """\tif adminAPIInfo.isAdminAPI {\n\t\tif meta.txnMsg != \"\" {\n\t\t\treq.Header.Set(types.HTTPHeaderUnsignedMsg, meta.txnMsg)\n\t\t}\n\t} else {\n\t\t// set request host\n\t\tif c.host != \"\" {\n\t\t\treq.Host = c.host\n\t\t} else if req.URL.Host != \"\" {\n\t\t\treq.Host = req.URL.Host\n\t\t}\n\t}\n"""
new = """\tif adminAPIInfo.isAdminAPI {\n\t\tif meta.txnMsg != \"\" {\n\t\t\treq.Header.Set(types.HTTPHeaderUnsignedMsg, meta.txnMsg)\n\t\t}\n\t}\n\n\t// set request host for both admin and non-admin APIs so local e2e can reach\n\t// SP endpoints by localhost while still sending the configured virtual host.\n\tif c.host != \"\" {\n\t\treq.Host = c.host\n\t} else if req.URL.Host != \"\" {\n\t\treq.Host = req.URL.Host\n\t}\n"""
if old in api_client_text:
    api_client.write_text(api_client_text.replace(old, new, 1))
elif new not in api_client_text:
    raise SystemExit("failed to patch client/api_client.go host handling")

suite = Path("e2e/basesuite/suite.go")
suite_text = suite.read_text()
sp_request_host = os.environ["SP_REQUEST_HOST"]
legacy_challenge = "client.Option{\n\t\tDefaultAccount: challengeAcc,\n\t})"
legacy_challenge_new = "client.Option{\n\t\tDefaultAccount: challengeAcc,\n\t\tHost:           \"" + sp_request_host + "\",\n\t})"
legacy_account = "client.Option{\n\t\tDefaultAccount: account,\n\t})"
legacy_account_new = "client.Option{\n\t\tDefaultAccount: account,\n\t\tHost:           \"" + sp_request_host + "\",\n\t})"
local_option_old = """func LocalE2EClientOption(account *types.Account, transport http.RoundTripper) client.Option {\n\treturn client.Option{\n\t\tDefaultAccount: account,\n\t\tGrpcAddress:    GRPCEndpoint,\n\t\tGrpcDialOption: grpc.WithTransportCredentials(insecure.NewCredentials()),\n\t\tTransport:      transport,\n\t}\n}\n"""
local_option_new = f"""func LocalE2EClientOption(account *types.Account, transport http.RoundTripper) client.Option {{\n\treturn client.Option{{\n\t\tDefaultAccount: account,\n\t\tGrpcAddress:    GRPCEndpoint,\n\t\tGrpcDialOption: grpc.WithTransportCredentials(insecure.NewCredentials()),\n\t\tTransport:      transport,\n\t\tHost:           \"{sp_request_host}\",\n\t}}\n}}\n"""

updated_suite = suite_text
if legacy_challenge in updated_suite:
    updated_suite = updated_suite.replace(legacy_challenge, legacy_challenge_new)
if legacy_account in updated_suite:
    updated_suite = updated_suite.replace(legacy_account, legacy_account_new)
if local_option_old in updated_suite:
    updated_suite = updated_suite.replace(local_option_old, local_option_new)

# the 1.2.x suite hardcodes another environment's endpoint and chain id;
# point it at the localup chain (newer refs resolve these from env instead,
# so only enforce the rewrite when the constant style is present)
updated_suite = updated_suite.replace(
    'Endpoint    = "http://localhost:26750"',
    'Endpoint    = "http://localhost:26657"')
updated_suite = updated_suite.replace(
    'ChainID     = "moca_1000000-121"',
    'ChainID     = "moca_5151-1"')
if 'Endpoint    = "' in updated_suite and 'Endpoint    = "http://localhost:26657"' not in updated_suite:
    raise SystemExit("failed to point e2e/basesuite/suite.go at the localup endpoint")
if 'ChainID     = "' in updated_suite and 'ChainID     = "moca_5151-1"' not in updated_suite:
    raise SystemExit("failed to point e2e/basesuite/suite.go at the localup chain id")

# challenger_info carries the chain challenger's exported hex key (localup
# discards its mnemonic), so let the challenge client accept either form
challenge_mnemonic_old = """\tmnemonic := ParseMnemonicFromFile(fmt.Sprintf("../../moca/deployment/localup/.local/challenger%d/challenger_info", 0))\n\tchallengeAcc, err := types.NewAccountFromMnemonic("challenge_account", mnemonic)\n\ts.Require().NoError(err)\n\tpriKey, err := keys.GetPriKeyFromMnemonic(mnemonic)\n\ts.Require().NoError(err)\n"""
challenge_mnemonic_new = """\tmnemonic := ParseMnemonicFromFile(fmt.Sprintf("../../moca/deployment/localup/.local/challenger%d/challenger_info", 0))\n\tvar challengeAcc *types.Account\n\tvar priKey string\n\tvar err error\n\tif len(mnemonic) == 64 {\n\t\tchallengeAcc, err = types.NewAccountFromPrivateKey("challenge_account", mnemonic)\n\t\ts.Require().NoError(err)\n\t\tpriKey = mnemonic\n\t} else {\n\t\tchallengeAcc, err = types.NewAccountFromMnemonic("challenge_account", mnemonic)\n\t\ts.Require().NoError(err)\n\t\tpriKey, err = keys.GetPriKeyFromMnemonic(mnemonic)\n\t\ts.Require().NoError(err)\n\t}\n"""
if challenge_mnemonic_old in updated_suite:
    updated_suite = updated_suite.replace(challenge_mnemonic_old, challenge_mnemonic_new, 1)
elif 'challengeAcc, err := types.NewAccountFromMnemonic("challenge_account", mnemonic)' in updated_suite:
    raise SystemExit("failed to patch e2e/basesuite/suite.go challenge key handling")

if updated_suite == suite_text and 'Host:           "' + sp_request_host + '"' not in suite_text:
    raise SystemExit("failed to patch e2e/basesuite/suite.go host handling")

suite.write_text(updated_suite)

# The 1.2.x migrate suite assumes the 4+2 EC genesis (GVG spans 7 SPs) and polls
# bucket status right after an evm broadcast that returns before the tx is mined,
# so every wait loop breaks out before migration even starts. Size the GVG assert
# for the localup 1+1 genesis (3 SPs) and make each poll also wait until the
# family primary actually becomes the migration target. No-ops on newer refs.
migrate_test = Path("e2e/e2e_migrate_bucket_test.go")
if migrate_test.exists():
    mt = migrate_test.read_text()
    if "Equal(len(spIDs), 7)" in mt:
        mt = mt.replace("Equal(len(spIDs), 7)", "Equal(len(spIDs), 3)")
        wait_if = "\t\tif bucketInfo.BucketStatus != storageTypes.BUCKET_STATUS_MIGRATING {\n\t\t\tbreak\n\t\t}"
        def moved_wait(target):
            return ("\t\tfamily, ferr := s.Client.QueryVirtualGroupFamily(s.ClientContext, bucketInfo.GlobalVirtualGroupFamilyId)\n"
                    "\t\ts.Require().NoError(ferr)\n"
                    "\t\tif bucketInfo.BucketStatus != storageTypes.BUCKET_STATUS_MIGRATING && family.PrimarySpId == " + target + " {\n\t\t\tbreak\n\t\t}")
        # scope each rewrite to its enclosing function so upstream reordering
        # can never wire a wait loop to the wrong migration target
        for func_name, target in (
            ("func (s *BucketMigrateTestSuite) waitUntilBucketMigrateFinish", "destSP.GetId()"),
            ("func (s *BucketMigrateTestSuite) Test_Bucket_Migrate_Simple_Conflict_Case", "conflictSPID"),
            ("func (s *BucketMigrateTestSuite) Test_Empty_Bucket_Migrate_Simple_Case", "destSP.GetId()"),
        ):
            start = mt.find(func_name)
            if start == -1:
                raise SystemExit("missing " + func_name + " in e2e_migrate_bucket_test.go")
            end = mt.find("\nfunc ", start)
            if end == -1:
                end = len(mt)
            seg = mt[start:end]
            if seg.count(wait_if) != 1:
                raise SystemExit("expected exactly one wait loop in " + func_name)
            mt = mt[:start] + seg.replace(wait_if, moved_wait(target), 1) + mt[end:]
        if wait_if in mt:
            raise SystemExit("unexpected extra wait loop in e2e_migrate_bucket_test.go")
        if mt.count("\n\tfor {\n") != 2:
            raise SystemExit("failed to bound e2e_migrate_bucket_test.go wait loops")
        mt = mt.replace("\n\tfor {\n", "\n\tfor i := 0; i < 100; i++ {\n")
        # 1+1 EC keeps redundancy indexes -1..1; the 4+2-era challenge probe walks to 5
        if "for j := -1; j < 6; j++ {" not in mt:
            raise SystemExit("failed to patch e2e_migrate_bucket_test.go challenge range")
        mt = mt.replace("for j := -1; j < 6; j++ {", "for j := -1; j < 2; j++ {")
        migrate_test.write_text(mt)
PY

  local graph_before
  graph_before=$(go126_graph_before)
  pin_go126_deps
  go mod tidy
  check_go126_deps sdk "${graph_before}"
  record_tree_diff sdk "${workspace}/moca-go-sdk"
  record_go_mod_digest sdk "${workspace}/moca-go-sdk"
  cd "${workspace}"
}

function normalize_sp_private_keys() {
  local sp_json_file=$1
  local tmp_file

  tmp_file=$(mktemp)
  jq '
    def pad64:
      if (test("^[0-9A-Fa-f]+$") | not) then
        error("non-hex private key")
      elif length > 64 then
        error("private key longer than 64 hex chars")
      else
        (reduce range(0; 64 - length) as $i (""; . + "0")) + .
      end;
    with_entries(
      .value |= (
        .OperatorPrivateKey |= pad64 |
        .SealPrivateKey |= pad64 |
        .ApprovalPrivateKey |= pad64 |
        .GcPrivateKey |= pad64 |
        .MaintenancePrivateKey |= pad64 |
        .BlsPrivateKey |= pad64
      )
    )
  ' "${sp_json_file}" > "${tmp_file}"
  mv "${tmp_file}" "${sp_json_file}"
}

#########################################
# build and start Moca blockchain #
#########################################
function moca_chain() {
  set -e
  # build Moca chain
  echo "${workspace}"
  # check every pinned ref before the chain starts; the go-sdk and moca-cmd
  # checkouts are synced (and checked) again where they are used
  sync_repo_ref https://github.com/mocachain/moca-go-sdk.git "${workspace}/moca-go-sdk" "${MOCA_GO_SDK_TAG}" "${MOCA_GO_SDK_EXPECTED_SHA:-}" sdk
  sync_repo_ref https://github.com/mocachain/moca-cmd.git "${workspace}/moca-cmd" "${MOCA_CMD_TAG}" "${MOCA_CMD_EXPECTED_SHA:-}" cmd
  sync_repo_ref https://github.com/mocachain/moca.git "${workspace}/moca" "${MOCA_TAG}" "${MOCA_EXPECTED_SHA:-}" chain
  cd "${workspace}"/moca/
  make proto-gen &
  make build
  record_go_mod_digest chain "${workspace}/moca"
  record_binary chain.mocad "${workspace}/moca/build/mocad"

  # start Moca chain
  bash ./deployment/localup/localup.sh all 1 "${E2E_SP_NUM}"
  bash ./deployment/localup/localup.sh export_sps 1 "${E2E_SP_NUM}"
  cp ./deployment/localup/.local/sp_export.json ./sp.json
  normalize_sp_private_keys ./sp.json

  # transfer some amoca tokens
  transfer_account
}

#############################################
# transfer some amoca tokens to test accounts #
#############################################
function transfer_account() {
  set -e
  cd "${workspace}"/moca/
  # the release-line mocad home has no client.toml chain-id default, so pass it
  # explicitly; the chain also enforces a fixed per-msg global fee, so a send
  # without --fees is rejected at CheckTx with "insufficient fee"
  ./build/mocad tx bank send validator0 "${TEST_ACCOUNT_ADDRESS}" 500000000000000000000amoca --home "${workspace}"/moca/deployment/localup/.local/validator0 --keyring-backend test --node http://localhost:26657 --chain-id moca_5151-1 --fees 200000000000000amoca -y
  sleep 2
  ./build/mocad q bank balances "${TEST_ACCOUNT_ADDRESS}" --node http://localhost:26657
}

#################################
# build and start Moca SP #
#################################
function moca_sp() {
  set -e
  cd "${workspace}"
  make install-tools
  make build
  record_binary sp.moca-sp "${workspace}/build/moca-sp"
  sed -i -e "s/^SP_NUM=.*/SP_NUM=${E2E_SP_NUM}/g" ./deployment/localup/env.info
  bash ./deployment/localup/localup.sh generate "${workspace}"/moca/sp.json ${MYSQL_USER} ${MYSQL_PASSWORD} ${MYSQL_ADDRESS}
  bash ./deployment/localup/localup.sh reset
  start_sp_stack
  sleep 30
  for sp_dir in ./deployment/localup/local_env/sp*; do
    if [ ! -d "${sp_dir}" ]; then
      continue
    fi

    sp_name=$(basename "${sp_dir}")
    sp_bin="${sp_dir}/moca-${sp_name}"
    sp_config="${sp_dir}/config.toml"
    if [ -x "${sp_bin}" ] && [ -f "${sp_config}" ]; then
      update_sp_quota "${sp_name}" "${sp_bin}" "${sp_config}"
    fi
  done
  dump_sp_logs
  ps -ef | grep moca-sp | wc -l
}

############################################
# build Moca cmd and set cmd config  #
############################################
# check out moca-cmd against the prepared local go-sdk and build it
function prepare_moca_cmd() {
  set -e
  sync_repo_ref https://github.com/mocachain/moca-cmd.git "${workspace}/moca-cmd" "${MOCA_CMD_TAG}" "${MOCA_CMD_EXPECTED_SHA:-}" cmd
  cd "${workspace}"/moca-cmd/
  go mod edit -replace github.com/mocachain/moca-go-sdk="${workspace}/moca-go-sdk"
  # the replaced go-sdk already requires the pinned versions; pin them in
  # cmd's own go.mod too and check cmd's graph, which is the one make builds
  local graph_before
  graph_before=$(go126_graph_before)
  pin_go126_deps
  go mod tidy
  check_go126_deps cmd "${graph_before}"
  record_evidence "cmd.go_sdk_replace" "$(go mod edit -json | jq -r '.Replace[]? | select(.Old.Path == "github.com/mocachain/moca-go-sdk") | .New.Path')"
  record_tree_diff cmd "${workspace}/moca-cmd"
  record_go_mod_digest cmd "${workspace}/moca-cmd"
  make build
  record_binary cmd.moca-cmd "${workspace}/moca-cmd/build/moca-cmd"
}

function build_cmd() {
  set -e
  cd "${workspace}"
  prepare_moca_go_sdk
  prepare_moca_cmd
  cd "${workspace}"/moca-cmd/build/

  # generate a keystore file to manage private key information
  touch key.txt &
  echo "${TEST_ACCOUNT_PRIVATE_KEY}" >key.txt
  touch dev-key.txt &
  echo "${DEV_ACCOUNT_PRIVATE_KEY}" >dev-key.txt
  touch password.txt &
  echo "test_sp_function" >password.txt
  ./moca-cmd --home ./ --passwordfile password.txt account import key.txt
  ./moca-cmd --home ./ --passwordfile password.txt --keystore ./dev-account.json account import dev-key.txt

  # construct config.toml
  touch config.toml
  {
    echo rpcAddr = \"http://localhost:26657\"
    echo chainId = \"moca_5151-1\"
    echo evmRpcAddr = \"http://localhost:8545\"
    echo host = \"${SP_REQUEST_HOST}\"
  } >config.toml
  cat config.toml
  retry_cmd 12 10 "validate moca-cmd config with sp ls" \
    ./moca-cmd -c ./config.toml --home ./ sp ls
  ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt --keystore ./dev-account.json bank transfer --toAddress "${TEST_ACCOUNT_ADDRESS}" --amount 500000000000000000000
  sleep 2
  ./moca-cmd -c ./config.toml --home ./ bank balance --address "${TEST_ACCOUNT_ADDRESS}"
}

############################################
# build Moca go-sdk                  #
############################################
function build_moca-go-sdk() {
  set -e
  prepare_moca_go_sdk
}

######################
# test create bucket #
######################
function test_create_bucket() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  retry_cmd 12 10 "list storage providers" \
    ./moca-cmd -c ./config.toml --home ./ sp ls
  retry_cmd 6 10 "create bucket ${BUCKET_NAME}" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt bucket create moca://${BUCKET_NAME}
  retry_cmd 12 10 "head bucket ${BUCKET_NAME}" \
    ./moca-cmd -c ./config.toml --home ./ bucket head moca://${BUCKET_NAME}
}

###########################################################
# test upload and download file which size less than 16MB #
###########################################################
function test_file_size_less_than_16_mb() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  retry_cmd 6 10 "put example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/json" "${workspace}"/test/e2e/spworkflow/testdata/example.json moca://${BUCKET_NAME}
  retry_cmd 12 10 "head example.json" \
    ./moca-cmd -c ./config.toml --home ./ object head moca://${BUCKET_NAME}/example.json
  retry_cmd 12 10 "get example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://${BUCKET_NAME}/example.json ./test_data.json
  check_md5 "${workspace}"/test/e2e/spworkflow/testdata/example.json ./test_data.json
  cat test_data.json
}

##############################################################
# test upload and download file which size greater than 16MB #
##############################################################
function test_file_size_greater_than_16_mb() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  dd if=/dev/urandom of=./random_file bs=17M count=1
  retry_cmd 6 10 "put random_file" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/octet-stream" ./random_file moca://${BUCKET_NAME}/random_file
  retry_cmd 12 10 "head random_file" \
    ./moca-cmd -c ./config.toml --home ./ object head moca://${BUCKET_NAME}/random_file
  retry_cmd 12 10 "get random_file" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://${BUCKET_NAME}/random_file ./new_random_file
  check_md5 ./random_file ./new_random_file
}

################
# test sp exit #
################
function test_sp_exit() {
  set -xe
  local exit_sp_dir
  local exit_sp_name
  local exit_sp_bin

  exit_sp_dir=$(select_exit_sp_dir)
  exit_sp_name=$(basename "${exit_sp_dir}")
  exit_sp_bin="./moca-${exit_sp_name}"

  cd "${exit_sp_dir}"
  operator_address=$(echo "$(grep "SpOperatorAddress" ./config.toml)" | grep -o "0x[0-9a-zA-Z]*")
  echo "${operator_address}"
  cd "${workspace}"/moca-cmd/build/
  ls
  dd if=/dev/urandom of=./random_file bs=17M count=1
  retry_cmd 6 10 "create spexit bucket" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt bucket create --primarySP "${operator_address}" moca://spexit
  retry_cmd 12 10 "head spexit bucket" \
    ./moca-cmd -c ./config.toml --home ./ bucket head moca://spexit
  retry_cmd 6 10 "put spexit random_file" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/octet-stream" ./random_file moca://spexit/random_file
  retry_cmd 6 10 "put spexit example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/json" "${workspace}"/test/e2e/spworkflow/testdata/example.json moca://spexit/example.json
  retry_cmd 12 10 "head spexit random_file" \
    ./moca-cmd -c ./config.toml --home ./ object head moca://spexit/random_file
  retry_cmd 12 10 "get spexit random_file" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://spexit/random_file ./new_random_file
  retry_cmd 12 10 "head spexit example.json" \
    ./moca-cmd -c ./config.toml --home ./ object head moca://spexit/example.json
  retry_cmd 12 10 "get spexit example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://spexit/example.json ./new.json

  check_md5 "${workspace}"/test/e2e/spworkflow/testdata/example.json ./new.json
  check_md5 ./random_file ./new_random_file

  # start exiting the selected non-primary SP
  cd "${exit_sp_dir}"
  "${exit_sp_bin}" -c ./config.toml sp.exit -operatorAddress "${operator_address}"
  cd "${workspace}"/moca-cmd/build/
  retry_cmd 12 10 "list storage providers before exit settle" \
    ./moca-cmd -c ./config.toml --home ./ sp ls
  retry_cmd 24 10 "head spexit bucket after exit" \
    ./moca-cmd -c ./config.toml --home ./ bucket head moca://spexit
  retry_cmd 24 10 "head spexit example.json after exit" \
    ./moca-cmd -c ./config.toml --home ./ object head moca://spexit/example.json
  retry_cmd 24 10 "get spexit example.json after exit" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://spexit/example.json ./new1.json
  retry_cmd 24 10 "get spexit random_file after exit" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get moca://spexit/random_file ./new_random_file1
  check_md5 "${workspace}"/test/e2e/spworkflow/testdata/example.json ./new1.json
  check_md5 ./random_file ./new_random_file1
}

##################################
# check two md5 whether is equal #
##################################
function check_md5() {
  set -e
  if [ $# != 2 ]; then
    echo "failed to check md5 value; this function needs two args"
    exit 1
  fi
  file1=$1
  file2=$2
  md5_1=$(md5sum "${file1}" | cut -d ' ' -f 1)
  md5_2=$(md5sum "${file2}" | cut -d ' ' -f 1)
  echo "${md5_1}"
  echo "${md5_2}"

  if [ "$md5_1" = "$md5_2" ]; then
    echo "The md5 values are the same."
  else
    echo "The md5 values are different."
    exit 1
  fi
}

#######################
# run sp workflow e2e #
#######################
function run_e2e() {
  set -e
  echo 'run test_create_bucket'
  test_create_bucket
  echo 'run put object case less than 16 MB'
  test_file_size_less_than_16_mb
  echo 'run put object case greater than 16 MB'
  test_file_size_greater_than_16_mb
}

###################
# run sp exit e2e #
###################
# TODO: use this function in sp exit e2e for speeding all e2e process which will be overwritten in the future
function run_sp_exit_e2e() {
  set -e
  echo 'run sp exit e2e test'
  test_sp_exit
}

###################
# run go-sdk e2e #
###################
# The 1.2.x go-sdk suite reads its accounts as mnemonics from
# .local/validator0/info and .local/challenger0/challenger_info (last
# non-empty line of each). The release-line chain imports its validator
# keys from preset raw private keys, so those mnemonic files never exist.
# Mint a fresh mnemonic per file, write it where the suite looks, and fund
# the derived address from validator0 so the suite's default account can
# pay for its buckets and migrations.
function prepare_sdk_suite_accounts() {
  set -e
  local mocad="${workspace}/moca/build/mocad"
  local localdir="${workspace}/moca/deployment/localup/.local"
  local spec dir file name amount mnemonic addr

  for spec in "validator0:info:sdk-e2e-default:100000000000000000000000"; do
    IFS=':' read -r dir file name amount <<<"${spec}"
    mnemonic=$("${mocad}" keys mnemonic 2>/dev/null)
    if [ -z "${mnemonic}" ]; then
      echo "failed to generate a mnemonic for ${name}"
      return 1
    fi
    mkdir -p "${localdir}/${dir}"
    printf '%s\n' "${mnemonic}" >"${localdir}/${dir}/${file}"
    addr=$(printf '%s\n' "${mnemonic}" | "${mocad}" keys add "${name}" --recover --keyring-backend test --home "${localdir}/validator0" --output json 2>/dev/null | jq -r .address)
    if [ -z "${addr}" ] || [ "${addr}" = "null" ]; then
      echo "failed to derive the ${name} address"
      return 1
    fi
    "${mocad}" tx bank send validator0 "${addr}" "${amount}amoca" --home "${localdir}/validator0" --keyring-backend test --node http://localhost:26657 --chain-id moca_5151-1 --fees 200000000000000amoca -y
    sleep 5
    "${mocad}" q bank balances "${addr}" --node http://localhost:26657
    if [ "$("${mocad}" q bank balances "${addr}" --node http://localhost:26657 --output json | jq -r '.balances | length')" = "0" ]; then
      echo "funding ${name} (${addr}) did not land"
      return 1
    fi
  done

  # the SP authenticates challenge requests against the bonded validator's
  # challenger address, so hand the suite the chain's actual challenger key
  # (exported as hex; the patched suite accepts either form)
  local challenger_key
  challenger_key=$(printf 'y\n' | "${mocad}" keys unsafe-export-eth-key challenger0 --keyring-backend test --home "${localdir}/challenger0" 2>/dev/null | tail -1)
  if [ "${#challenger_key}" != "64" ]; then
    echo "failed to export the challenger0 key"
    return 1
  fi
  printf '%s\n' "${challenger_key}" >"${localdir}/challenger0/challenger_info"
}

function run_go_sdk_e2e() {
  prepare_sdk_suite_accounts
  set +e
  cd "${workspace}"/moca-go-sdk/
  echo 'run moca go sdk e2e test'
  export MOCA_E2E_ENDPOINT="http://localhost:26657"
  export MOCA_E2E_EVM_ENDPOINT="http://localhost:8545"
  export MOCA_E2E_CHAIN_ID="moca_5151-1"
  export MOCA_E2E_LOCALUP_DIR="${workspace}/moca/deployment/localup/.local"
  go test -count=1 -timeout 30m -v ./e2e -run TestBucketMigrateTestSuiteTestSuite
  exit_status_command=$?
  if [ $exit_status_command -eq 0 ]; then
    echo "make e2e_test successful."
  else
    dump_sp_logs
    exit $exit_status_command
  fi
}

function main() {
  CMD=$1
  case ${CMD} in
  --startChain)
    moca_chain
    ;;
  --startSP)
    moca_sp
    ;;
  --buildCmd)
    build_cmd
    ;;
  --runTest)
    run_e2e
    ;;
  --runSPExit)
    run_sp_exit_e2e
    ;;
  --runSDKE2E)
    build_moca-go-sdk
    run_go_sdk_e2e
    ;;
  --recordMetadata)
    record_run_metadata
    ;;
  --redactEvidence)
    redact_evidence "$2"
    ;;
  esac
}

main $@
