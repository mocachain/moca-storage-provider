#!/usr/bin/env bash
set -euo pipefail

export CGO_CFLAGS="-O -D__BLST_PORTABLE__"
export CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

workspace=${GITHUB_WORKSPACE:?GITHUB_WORKSPACE must point at the checkout}

# some constants
# Keep refs override-friendly and default the whole e2e stack to main so the
# chain, cmd and go-sdk all move in lockstep.
MOCA_TAG="${MOCA_TAG:-main}"
MOCA_CMD_TAG="${MOCA_CMD_TAG:-main}"
MOCA_GO_SDK_TAG="${MOCA_GO_SDK_TAG:-main}"
SDK_E2E_RUN="${SDK_E2E_RUN:-TestBucketMigrateTestSuiteTestSuite}"
MYSQL_USER="root"
MYSQL_PASSWORD="root"
MYSQL_ADDRESS="127.0.0.1:3306"
TEST_ACCOUNT_ADDRESS=${ACCOUNT_ADDR:?ACCOUNT_ADDR must be the test account address}
TEST_ACCOUNT_PRIVATE_KEY=${PRIVATE_KEY:?PRIVATE_KEY must be the test account private key}
DEV_ACCOUNT_PRIVATE_KEY="2228e392584d902843272c37fd62b8c73c10c81a5ecb901773c9ebe366e937bb"
echo "TEST_ACCOUNT_ADDRESS is ""$TEST_ACCOUNT_ADDRESS"
echo "TEST_ACCOUNT_PRIVATE_KEY is ""$TEST_ACCOUNT_PRIVATE_KEY"

BUCKET_NAME="spbucket"
SP_REQUEST_HOST="${SP_REQUEST_HOST:-gnfd.test-sp.com}"
E2E_SP_NUM=8
CHAIN_RPC="http://localhost:26657"
CHAIN_REST="http://localhost:1317"
EVM_RPC="http://localhost:8545"

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

# moca-cmd reports failures on stdout with exit 0, so the output decides
CMD_FAILURE_PATTERN='run command error:|The operator account have no balance|no such bucket|no such object|no such group|no such virtual group family|the user does not exist in the group'
RETRY_CMD_OUTPUT=""

function retry_cmd() {
  local max_attempts=$1
  local sleep_seconds=$2
  local description=$3
  shift 3
  local attempt out rc

  for attempt in $(seq 1 "${max_attempts}"); do
    rc=0
    out=$("$@" 2>&1) || rc=$?
    echo "${out}"
    if [ "${rc}" -eq 0 ] && ! grep -qE "${CMD_FAILURE_PATTERN}" <<<"${out}"; then
      RETRY_CMD_OUTPUT="${out}"
      echo "${description} succeeded"
      return 0
    fi

    echo "${description} failed on attempt ${attempt}/${max_attempts} (exit code ${rc})"
    if [ "${attempt}" -lt "${max_attempts}" ]; then
      sleep "${sleep_seconds}"
    fi
  done

  echo "${description} failed after ${max_attempts} attempts"
  dump_sp_logs
  return 1
}

# wait_until <timeout seconds> <interval seconds> <description> <predicate...>
function wait_until() {
  local timeout=$1
  local interval=$2
  local description=$3
  shift 3
  local start=${SECONDS}
  local state last=""

  while :; do
    if state=$("$@" 2>&1); then
      echo "${description}: ${state:-ok} (after $((SECONDS - start))s)"
      return 0
    fi
    if [ "${state}" != "${last}" ]; then
      echo "${description}: waiting, ${state:-no state yet} ($((SECONDS - start))s)"
      last=${state}
    fi
    if [ $((SECONDS - start)) -ge "${timeout}" ]; then
      echo "${description}: not satisfied within ${timeout}s, last state: ${state:-none}"
      return 1
    fi
    sleep "${interval}"
  done
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

function sync_repo_ref() {
  local repo_url=$1
  local repo_dir=$2
  local repo_ref=$3

  cd "${workspace}"
  if [ ! -d "${repo_dir}/.git" ]; then
    git clone "${repo_url}" "${repo_dir}"
  fi

  cd "${repo_dir}"
  git fetch --tags --prune origin "${repo_ref}"
  git checkout -B codex-ci-ref FETCH_HEAD
}

function prepare_moca_go_sdk() {
  set -e
  sync_repo_ref https://github.com/mocachain/moca-go-sdk.git "${workspace}/moca-go-sdk" "${MOCA_GO_SDK_TAG}"

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
legacy_challenge = "client.Option{\\n\\t\\tDefaultAccount: challengeAcc,\\n\\t})"
legacy_challenge_new = "client.Option{\\n\\t\\tDefaultAccount: challengeAcc,\\n\\t\\tHost:           \\\"" + sp_request_host + "\\\",\\n\\t})"
legacy_account = "client.Option{\\n\\t\\tDefaultAccount: account,\\n\\t})"
legacy_account_new = "client.Option{\\n\\t\\tDefaultAccount: account,\\n\\t\\tHost:           \\\"" + sp_request_host + "\\\",\\n\\t})"
local_option_old = """func LocalE2EClientOption(account *types.Account, transport http.RoundTripper) client.Option {\n\treturn client.Option{\n\t\tDefaultAccount: account,\n\t\tGrpcAddress:    GRPCEndpoint,\n\t\tGrpcDialOption: grpc.WithTransportCredentials(insecure.NewCredentials()),\n\t\tTransport:      transport,\n\t}\n}\n"""
local_option_new = f"""func LocalE2EClientOption(account *types.Account, transport http.RoundTripper) client.Option {{\n\treturn client.Option{{\n\t\tDefaultAccount: account,\n\t\tGrpcAddress:    GRPCEndpoint,\n\t\tGrpcDialOption: grpc.WithTransportCredentials(insecure.NewCredentials()),\n\t\tTransport:      transport,\n\t\tHost:           \"{sp_request_host}\",\n\t}}\n}}\n"""

updated_suite = suite_text
if legacy_challenge in updated_suite:
    updated_suite = updated_suite.replace(legacy_challenge, legacy_challenge_new)
if legacy_account in updated_suite:
    updated_suite = updated_suite.replace(legacy_account, legacy_account_new)
if local_option_old in updated_suite:
    updated_suite = updated_suite.replace(local_option_old, local_option_new)

if updated_suite == suite_text and "Host:           \\\"" + sp_request_host + "\\\"" not in suite_text:
    raise SystemExit("failed to patch e2e/basesuite/suite.go host handling")

suite.write_text(updated_suite)
PY

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
  sync_repo_ref https://github.com/mocachain/moca.git "${workspace}/moca" "${MOCA_TAG}"
  cd "${workspace}"/moca/
  make proto-gen
  make build

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
  local result attempt
  # the chain rejects a cosmos tx without the global minimum fee (code 13), and
  # the CLI exits 0 either way, so the broadcast result is checked explicitly
  result=$(./build/mocad tx bank send validator0 "${TEST_ACCOUNT_ADDRESS}" 500000000000000000000amoca --fees 5000000000000000amoca --home "${workspace}"/moca/deployment/localup/.local/validator0 --keyring-backend test --node http://localhost:26657 -y --output json)
  echo "${result}"
  if [ "$(echo "${result}" | jq -r '.code')" != "0" ]; then
    echo "funding transfer to ${TEST_ACCOUNT_ADDRESS} was rejected"
    exit 1
  fi
  for attempt in $(seq 1 15); do
    if ./build/mocad q bank balances "${TEST_ACCOUNT_ADDRESS}" --node http://localhost:26657 --output json | jq -e '.balances[] | select(.denom == "amoca") | (.amount | tonumber) > 0' >/dev/null; then
      ./build/mocad q bank balances "${TEST_ACCOUNT_ADDRESS}" --node http://localhost:26657
      return 0
    fi
    sleep 2
  done
  echo "test account ${TEST_ACCOUNT_ADDRESS} still has no amoca balance after the funding transfer"
  exit 1
}

#################################
# build and start Moca SP #
#################################
function moca_sp() {
  set -e
  cd "${workspace}"
  make install-tools
  make build
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
function build_cmd() {
  set -e
  cd "${workspace}"
  prepare_moca_go_sdk
  # build sp
  sync_repo_ref https://github.com/mocachain/moca-cmd.git "${workspace}/moca-cmd" "${MOCA_CMD_TAG}"
  cd "${workspace}"/moca-cmd/
  go mod edit -replace github.com/mocachain/moca-go-sdk="${workspace}/moca-go-sdk"
  go mod tidy
  make build
  cd build/

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
  assert_bucket_head "${BUCKET_NAME}"
}

###########################################################
# test upload and download file which size less than 16MB #
###########################################################
function test_file_size_less_than_16_mb() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  local example="${workspace}"/test/e2e/spworkflow/testdata/example.json
  retry_cmd 6 10 "put example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/json" "${example}" moca://${BUCKET_NAME}
  wait_object_sealed "${BUCKET_NAME}" example.json "$(wc -c <"${example}" | tr -d ' ')" "application/json"
  get_object_until_match moca://${BUCKET_NAME}/example.json ./test_data.json "${example}"
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
  wait_object_sealed "${BUCKET_NAME}" random_file "$(wc -c <./random_file | tr -d ' ')" "application/octet-stream"
  get_object_until_match moca://${BUCKET_NAME}/random_file ./new_random_file ./random_file
}

# sp exit helpers: the exit is judged on chain state
function sp_operator_address() {
  grep "SpOperatorAddress" "$1/config.toml" | grep -o "0x[0-9a-zA-Z]*"
}

# the SP directory (local_env/spN) whose operator address is $1
function sp_dir_by_operator() {
  local sp_dir
  for sp_dir in "${workspace}"/deployment/localup/local_env/sp*; do
    if [ "$(sp_operator_address "${sp_dir}" | tr 'A-F' 'a-f')" = "$(echo "$1" | tr 'A-F' 'a-f')" ]; then
      echo "${sp_dir}"
      return 0
    fi
  done
  return 1
}

# sp_chain_field <operator address> <field>: empty when the SP is not on chain (any more)
function sp_chain_field() {
  mocad_q sp storage-provider-by-operator-address "$1" 2>/dev/null | jq -r "(.storage_provider // .storageProvider).$2 // empty" || true
}

function sp_chain_field_by_id() {
  mocad_q sp storage-provider "$1" 2>/dev/null | jq -r "(.storage_provider // .storageProvider).$2 // empty" || true
}

function sp_status_is() {
  local status
  status=$(sp_chain_field "$1" status)
  echo "status=${status:-unknown}"
  [ "${status}" = "$2" ]
}

# 0: the SP is not listed, 1: still listed, 2: the list query or its JSON is unusable
function sp_left_chain() {
  local json view
  if ! json=$(mocad_q sp storage-providers 2>&1); then
    echo "sp list query failed: $(head -n 1 <<<"${json}")"
    return 2
  fi
  if ! view=$(jq -er --arg op "$1" '
    .sps | if type == "array" then . else error("sps is not a list") end
    | if any(.[]; (.operator_address | ascii_downcase) == ($op | ascii_downcase))
      then "listed " + ([.[] | "\(.id):\(.status)"] | join(" "))
      else "gone \(length)" end' <<<"${json}" 2>&1); then
    echo "sp list is unreadable: ${view:0:200}"
    return 2
  fi
  if [ "${view%% *}" = "listed" ]; then
    echo "still listed (${view#listed })"
    return 1
  fi
  echo "not in the SP list any more (${view#gone } SPs remain)"
}

function bucket_family_id() {
  mocad_q storage head-bucket "$1" | jq -r '.bucket_info.global_virtual_group_family_id // empty'
}

function family_primary_sp_id() {
  mocad_q virtualgroup global-virtual-group-family "$1" 2>/dev/null | jq -r '.global_virtual_group_family.primary_sp_id // empty' || true
}

# succeeds once the chain holds no family or GVG membership against an SP (empty families count)
# 0: drained, 1: memberships remain, 2: a chain answer is missing or unusable
function sp_gvgs_drained() {
  local sp_id=$1
  local families view primaries fam_ids fam gvgs n secondaries=0

  if ! families=$(mocad_q virtualgroup global-virtual-group-families 1000 2>&1); then
    echo "global virtual group families query failed: $(head -n 1 <<<"${families}")"
    return 2
  fi
  if ! view=$(jq -er --argjson sp "${sp_id}" '
    .gvg_families | if type == "array" then . else error("gvg_families is not a list") end
    | "\(map(select((.primary_sp_id | tonumber) == $sp)) | length) \(map(.id | tonumber) | join(" "))"' <<<"${families}" 2>&1); then
    echo "global virtual group families are unreadable: ${view:0:200}"
    return 2
  fi
  read -r primaries fam_ids <<<"${view}"
  for fam in ${fam_ids}; do
    if ! gvgs=$(mocad_q virtualgroup global-virtual-group-by-family-id "${fam}" 2>&1); then
      echo "global virtual groups query of family ${fam} failed: $(head -n 1 <<<"${gvgs}")"
      return 2
    fi
    if ! n=$(jq -er --argjson sp "${sp_id}" '
      .global_virtual_groups | if type == "array" then . else error("global_virtual_groups is not a list") end
      | map(select(.secondary_sp_ids | map(tonumber) | index($sp) != null)) | length' <<<"${gvgs}" 2>&1); then
      echo "global virtual groups of family ${fam} are unreadable: ${n:0:200}"
      return 2
    fi
    secondaries=$((secondaries + n))
  done
  echo "primary_families=${primaries} secondary_gvgs=${secondaries}"
  [ "${primaries}" = "0" ] && [ "${secondaries}" = "0" ]
}

function family_primary_left() {
  local now
  now=$(family_primary_sp_id "$1")
  echo "family $1 primary_sp_id=${now:-unknown}"
  [ -n "${now}" ] && [ "${now}" != "$2" ]
}

# the swap outs the exiting SP's manager planned, as "key->successor"
function sp_exit_plan_units() {
  local sp_dir=$1
  local plan
  plan=$(cd "${sp_dir}" && "./moca-$(basename "${sp_dir}")" query.sp.exit -c ./config.toml 2>&1) || true
  jq -r '[.swap_out_src[]? | "\(.swap_out_key)->\(.successor_sp_id)"] | join(" ")' <<<"${plan}" 2>/dev/null || echo "unparsable plan: ${plan:0:200}"
}

function sp_process_stopped() {
  ! pgrep -f "[m]oca-$1 --config" >/dev/null
}

function sp_exit_plan_ready() {
  local units
  units=$(sp_exit_plan_units "$1")
  echo "swap out units: ${units:-none yet}"
  [[ "${units}" == *"->"* ]]
}

function hex_of_b64() {
  python3 -c 'import base64, sys; d = base64.b64decode(sys.argv[1], validate=True); print(d.hex()) if d else sys.exit(1)' "$1" 2>/dev/null
}

# the integrity hash challenge.piece printed (hex) must be the chain checksum (base64): Checksums[0] primary, Checksums[i+1] secondary i
function integrity_hash_matches_chain() {
  local out=$1 head_json=$2 redundancy_idx=$3
  local printed b64 chain

  printed=$(sed -n 's/^integrity hash\[\([0-9a-fA-F][0-9a-fA-F]*\)\]$/\1/p' <<<"${out}" | head -n 1 | tr 'A-F' 'a-f')
  if [ -z "${printed}" ]; then
    echo "challenge.piece printed no integrity hash"
    return 1
  fi
  if ! b64=$(jq -er --argjson i "$((redundancy_idx + 1))" '
    .object_info.checksums | if type == "array" and $i >= 0 then .[$i] else null end
    | select(type == "string" and length > 0)' <<<"${head_json}" 2>/dev/null) \
    || ! chain=$(hex_of_b64 "${b64}"); then
    echo "the chain has no readable checksum for redundancy index ${redundancy_idx}"
    return 1
  fi
  if [ "${printed}" != "${chain}" ]; then
    echo "integrity hash ${printed} is not the on-chain checksum ${chain} of redundancy index ${redundancy_idx}"
    return 1
  fi
  echo "integrity hash ${printed} is the on-chain checksum of redundancy index ${redundancy_idx}"
}

# the SP in $1 serves a piece of the object from its own store, whose integrity hash is the on-chain checksum (redundancy -1 = primary)
function challenge_piece_ok() {
  local sp_dir=$1 bucket=$2 object=$3 redundancy_idx=$4 segment_idx=$5
  local sp_name info id out verdict label
  sp_name=$(basename "${sp_dir}")
  label="${bucket}/${object} redundancy ${redundancy_idx} segment ${segment_idx} on ${sp_name}"

  if ! info=$(mocad_q storage head-object "${bucket}" "${object}" 2>&1); then
    echo "challenge ${label} failed: head-object query failed: $(head -n 1 <<<"${info}")"
    return 1
  fi
  if ! id=$(jq -er '.object_info.id | tostring | select(test("^[0-9]+$"))' <<<"${info}" 2>/dev/null); then
    echo "challenge ${label} failed: head-object has no object id: ${info:0:200}"
    return 1
  fi
  out=$(cd "${sp_dir}" && "./moca-${sp_name}" challenge.piece -c ./config.toml --object.id "${id}" --redundancy.index="${redundancy_idx}" --segment.index "${segment_idx}" 2>&1) || true
  if ! grep -q "succeed to check integrity hash" <<<"${out}"; then
    echo "challenge ${label} failed: ${out:0:300}"
    return 1
  fi
  if ! verdict=$(integrity_hash_matches_chain "${out}" "${info}" "${redundancy_idx}"); then
    echo "challenge ${label} failed: ${verdict}"
    return 1
  fi
  echo "${label}: ${verdict}"
}

function dump_sp_exit_diagnostics() {
  local sp_dir

  echo "===== chain SP list ====="
  mocad_q sp storage-providers 2>&1 | jq -c '.sps[] | {id, status, operator_address}' || true
  echo "===== chain virtual group families ====="
  mocad_q virtualgroup global-virtual-group-families 1000 2>&1 | jq -c '.gvg_families[]?' || true
  for sp_dir in "${workspace}"/deployment/localup/local_env/sp*; do
    echo "===== $(basename "${sp_dir}") query.sp.exit ====="
    (cd "${sp_dir}" && "./moca-$(basename "${sp_dir}")" query.sp.exit -c ./config.toml 2>&1 | head -c 3000) || true
    echo
    echo "===== $(basename "${sp_dir}") swap out / exit log lines ====="
    grep -iE 'swap.?out|sp exit|complete sp exit|migrate gvg|migrate piece' "${sp_dir}/log.txt" 2>/dev/null | tail -n 40 | cut -c1-400 || true
  done
  dump_sp_logs
}

function sp_exit_fail() {
  echo "SP exit e2e failed: $1"
  dump_sp_exit_diagnostics
  exit 1
}

################
# test sp exit #
################
# the family primary and a secondary slot both move to successors, then every object is re-read
function test_sp_exit() {
  set -e
  local exit_sp_dir exit_sp_name exit_sp_bin operator_address exit_sp_id
  local primary_operator example out status drained
  local family_id sec_family_id sec_gvg sec_redundancy_idx
  local successor_id successor_operator successor_dir successor_endpoint
  local sec_holder_id sec_holder_operator sec_holder_dir

  exit_sp_dir=$(select_exit_sp_dir)
  exit_sp_name=$(basename "${exit_sp_dir}")
  exit_sp_bin="./moca-${exit_sp_name}"
  operator_address=$(sp_operator_address "${exit_sp_dir}")
  primary_operator=$(sp_operator_address "${workspace}"/deployment/localup/local_env/sp0)
  exit_sp_id=$(sp_chain_field "${operator_address}" id)
  status=$(sp_chain_field "${operator_address}" status)
  echo "exiting ${exit_sp_name}: operator ${operator_address}, sp id ${exit_sp_id:-unknown}, status ${status:-unknown}"
  if [ -z "${exit_sp_id}" ] || [ "${status}" != "STATUS_IN_SERVICE" ]; then
    echo "${exit_sp_name} is not an in-service SP on chain"
    exit 1
  fi

  cd "${workspace}"/moca-cmd/build/
  example="${workspace}"/test/e2e/spworkflow/testdata/example.json
  dd if=/dev/urandom of=./random_file bs=17M count=1
  echo "sp exit secondary object $(date) ${RANDOM}" >./secondary_object.txt

  echo "--- bucket spexit has ${exit_sp_name} as its primary SP ---"
  retry_cmd 6 10 "create spexit bucket" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt bucket create --primarySP "${operator_address}" moca://spexit
  assert_bucket_head spexit
  retry_cmd 6 10 "put spexit random_file" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/octet-stream" ./random_file moca://spexit/random_file
  wait_object_sealed spexit random_file "$(wc -c <./random_file | tr -d ' ')" "application/octet-stream"
  retry_cmd 6 10 "put spexit example.json" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/json" "${example}" moca://spexit/example.json
  wait_object_sealed spexit example.json "$(wc -c <"${example}" | tr -d ' ')" "application/json"
  get_object_until_match moca://spexit/random_file ./new_random_file ./random_file
  get_object_until_match moca://spexit/example.json ./new.json "${example}"

  echo "--- bucket spexit-sec has ${exit_sp_name} as a secondary SP of its GVG ---"
  retry_cmd 6 10 "create spexit-sec bucket" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt bucket create --primarySP "${primary_operator}" moca://spexit-sec
  assert_bucket_head spexit-sec
  retry_cmd 6 10 "put spexit-sec secondary_object.txt" \
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "text/plain" ./secondary_object.txt moca://spexit-sec/secondary_object.txt
  wait_object_sealed spexit-sec secondary_object.txt "$(wc -c <./secondary_object.txt | tr -d ' ')" "text/plain"
  get_object_until_match moca://spexit-sec/secondary_object.txt ./new_secondary_object.txt ./secondary_object.txt

  echo "--- chain state before the exit ---"
  family_id=$(bucket_family_id spexit)
  sec_family_id=$(bucket_family_id spexit-sec)
  if [ "$(family_primary_sp_id "${family_id}")" != "${exit_sp_id}" ]; then
    echo "family ${family_id} of spexit is not led by ${exit_sp_name} (sp id ${exit_sp_id})"
    exit 1
  fi
  sec_gvg=$(mocad_q virtualgroup global-virtual-group-by-family-id "${sec_family_id}" \
    | jq -r --argjson sp "${exit_sp_id}" '[.global_virtual_groups[] | select(.secondary_sp_ids | map(tonumber) | index($sp) != null)][0].id // empty')
  sec_redundancy_idx=$(mocad_q virtualgroup global-virtual-group-by-family-id "${sec_family_id}" \
    | jq -r --argjson sp "${exit_sp_id}" '[.global_virtual_groups[] | select(.secondary_sp_ids | map(tonumber) | index($sp) != null)][0].secondary_sp_ids | map(tonumber) | index($sp) // empty')
  if [ -z "${sec_gvg}" ] || [ -z "${sec_redundancy_idx}" ]; then
    echo "${exit_sp_name} (sp id ${exit_sp_id}) is not a secondary SP in a GVG of family ${sec_family_id}"
    exit 1
  fi
  echo "family ${family_id}: primary sp ${exit_sp_id}; family ${sec_family_id}: gvg ${sec_gvg} has sp ${exit_sp_id} as secondary #${sec_redundancy_idx}"
  drained=0
  sp_gvgs_drained "${exit_sp_id}" || drained=$?
  if [ "${drained}" -ne 1 ]; then
    echo "expected virtual group memberships against sp ${exit_sp_id} before its exit, the drain check returned ${drained}"
    exit 1
  fi

  echo "--- sp.exit on ${exit_sp_name} ---"
  out=$(cd "${exit_sp_dir}" && "${exit_sp_bin}" -c ./config.toml sp.exit -operatorAddress "${operator_address}" 2>&1) || true
  echo "${out}"
  if ! grep -q "send sp exit txn successfully" <<<"${out}"; then
    sp_exit_fail "sp.exit did not send the exit transaction"
  fi
  assert_tx_ok "${out}" "sp exit" || sp_exit_fail "the exit transaction is not confirmed on chain"
  wait_until 120 3 "chain status of ${exit_sp_name}" sp_status_is "${operator_address}" STATUS_GRACEFUL_EXITING \
    || sp_exit_fail "${exit_sp_name} never became STATUS_GRACEFUL_EXITING"
  wait_until 900 5 "exit plan of ${exit_sp_name}" sp_exit_plan_ready "${exit_sp_dir}" \
    || sp_exit_fail "the exit scheduler of ${exit_sp_name} produced no swap out plan"

  echo "--- successor takes over the primary role of family ${family_id} ---"
  wait_until 900 5 "family ${family_id}" family_primary_left "${family_id}" "${exit_sp_id}" \
    || sp_exit_fail "family ${family_id} was never swapped out of sp ${exit_sp_id}"
  successor_id=$(family_primary_sp_id "${family_id}")
  successor_operator=$(sp_chain_field_by_id "${successor_id}" operator_address)
  status=$(sp_chain_field_by_id "${successor_id}" status)
  successor_endpoint=$(sp_chain_field_by_id "${successor_id}" endpoint)
  successor_dir=$(sp_dir_by_operator "${successor_operator}") || sp_exit_fail "no local SP directory for successor ${successor_operator}"
  echo "successor of family ${family_id}: sp ${successor_id} ($(basename "${successor_dir}"), ${successor_endpoint}), status ${status}"
  if [ "${status}" != "STATUS_IN_SERVICE" ]; then
    sp_exit_fail "successor sp ${successor_id} is ${status}, expected STATUS_IN_SERVICE"
  fi

  echo "--- the exiting SP drains out of every GVG and family ---"
  wait_until 900 5 "virtual group membership of sp ${exit_sp_id}" sp_gvgs_drained "${exit_sp_id}" \
    || sp_exit_fail "the chain never confirmed sp ${exit_sp_id} drained of its virtual group memberships"

  echo "--- complete exit ---"
  if ! wait_until 300 5 "${exit_sp_name} completing its exit on its own" sp_left_chain "${operator_address}"; then
    echo "the exit was not completed automatically, sending sp.complete.exit"
    out=$(cd "${exit_sp_dir}" && "${exit_sp_bin}" -c ./config.toml sp.complete.exit -operatorAddress "${operator_address}" 2>&1) || true
    echo "${out}"
    wait_until 300 5 "${exit_sp_name} after sp.complete.exit" sp_left_chain "${operator_address}" \
      || sp_exit_fail "the chain never confirmed ${exit_sp_name} out of the SP list after sp.complete.exit"
  fi
  retry_cmd 12 10 "list storage providers after the exit" \
    ./moca-cmd -c ./config.toml --home ./ sp ls
  if grep -qi "${operator_address}" <<<"${RETRY_CMD_OUTPUT}" || grep -q "GRACEFUL_EXITING" <<<"${RETRY_CMD_OUTPUT}"; then
    sp_exit_fail "sp ls still shows the exited ${exit_sp_name}"
  fi

  echo "--- the exited SP stops, the objects keep downloading from the successor ---"
  pkill -f "[m]oca-${exit_sp_name} --config" || true
  wait_until 30 1 "${exit_sp_name} process" sp_process_stopped "${exit_sp_name}" \
    || sp_exit_fail "${exit_sp_name} is still running"
  retry_cmd 24 10 "head spexit bucket after the exit" \
    ./moca-cmd -c ./config.toml --home ./ bucket head moca://spexit
  if ! grep -q "primary SP ID: ${successor_id}" <<<"${RETRY_CMD_OUTPUT}"; then
    sp_exit_fail "bucket head does not report the successor sp ${successor_id} as the primary SP of spexit"
  fi
  assert_object_sealed spexit random_file "$(wc -c <./random_file | tr -d ' ')" "application/octet-stream"
  assert_object_sealed spexit example.json "$(wc -c <"${example}" | tr -d ' ')" "application/json"
  get_object_until_match moca://spexit/example.json ./new1.json "${example}" 40
  get_object_until_match moca://spexit/random_file ./new_random_file1 ./random_file 40
  get_object_until_match moca://spexit-sec/secondary_object.txt ./new_secondary_object1.txt ./secondary_object.txt 40

  echo "--- the new primary and the new secondary hold the migrated pieces ---"
  challenge_piece_ok "${successor_dir}" spexit random_file -1 0 || sp_exit_fail "successor failed the piece check of segment 0 of random_file"
  challenge_piece_ok "${successor_dir}" spexit random_file -1 1 || sp_exit_fail "successor failed the piece check of segment 1 of random_file"
  challenge_piece_ok "${successor_dir}" spexit example.json -1 0 || sp_exit_fail "successor failed the piece check of segment 0 of example.json"
  # a secondary slot is swapped out per GVG, to an SP chosen independently of the family successor
  sec_holder_id=$(mocad_q virtualgroup global-virtual-group "${sec_gvg}" \
    | jq -r --argjson idx "${sec_redundancy_idx}" '.global_virtual_group.secondary_sp_ids[$idx] // empty')
  if [ -z "${sec_holder_id}" ] || [ "${sec_holder_id}" = "${exit_sp_id}" ]; then
    sp_exit_fail "slot ${sec_redundancy_idx} of gvg ${sec_gvg} is held by sp '${sec_holder_id}', not by a successor of sp ${exit_sp_id}"
  fi
  sec_holder_operator=$(sp_chain_field_by_id "${sec_holder_id}" operator_address)
  sec_holder_dir=$(sp_dir_by_operator "${sec_holder_operator}") || sp_exit_fail "no local SP directory for sp ${sec_holder_id}"
  challenge_piece_ok "${sec_holder_dir}" spexit-sec secondary_object.txt "${sec_redundancy_idx}" 0 \
    || sp_exit_fail "sp ${sec_holder_id} failed the piece check of redundancy piece ${sec_redundancy_idx} of secondary_object.txt"
  echo "SP exit completed: ${exit_sp_name} (sp ${exit_sp_id}) left, sp ${successor_id} took over its family, sp ${sec_holder_id} its slot in gvg ${sec_gvg}, all objects verified by sha256 and the migrated pieces by their on-chain checksums"
}

#######################################################################
# download an object until it matches the expected file: reads go     #
# through the primary SP's metadata service, which can lag the seal   #
# by a few blocks, and moca-cmd exits 0 on a failed get, so a plain   #
# retry_cmd never retries it                                          #
#######################################################################
function sha256_of() {
  sha256sum "$1" | cut -d ' ' -f 1
}

function get_object_until_match() {
  local url=$1
  local dest=$2
  local expected=$3
  local attempts=${4:-20}
  local attempt expected_sha

  expected_sha=$(sha256_of "${expected}")
  for attempt in $(seq 1 "${attempts}"); do
    rm -f "${dest}" "$(dirname "${dest}")/.$(basename "${dest}").tmp"
    ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object get "${url}" "${dest}" || true
    if [ -f "${dest}" ] && [ "$(sha256_of "${dest}")" = "${expected_sha}" ]; then
      echo "downloaded ${url} matches ${expected} (sha256 ${expected_sha})"
      return 0
    fi
    sleep 5
  done
  echo "downloaded ${url} never matched ${expected} after ${attempts} attempts"
  dump_sp_logs
  return 1
}

##########################################################
# chain query and tx helpers shared by the storage cases #
##########################################################
# chain queries are retried on transient rpc failures (the local rpc times out
# under load); a "not found" answer is returned at once so deletion polls work
function mocad_q() {
  local attempt out
  for attempt in $(seq 1 5); do
    if out=$("${workspace}"/moca/build/mocad q "$@" --node "${CHAIN_RPC}" --output json 2>&1); then
      echo "${out}"
      return 0
    fi
    if echo "${out}" | grep -qiE "not found|not exist|no such"; then
      echo "${out}" >&2
      return 1
    fi
    sleep 2
  done
  echo "${out}" >&2
  return 1
}

function evm_rpc() {
  local method=$1
  local params=$2
  curl -s -X POST -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}" "${EVM_RPC}"
}

function evm_nonce() {
  evm_rpc eth_getTransactionCount "[\"$1\",\"latest\"]" | jq -r '.result'
}

# amoca balances exceed 2^63, so integer maths goes through python
function bigsub() {
  python3 -c 'import sys; print(int(sys.argv[1]) - int(sys.argv[2]))' "$1" "$2"
}

function bigcmp() {
  python3 -c '
import operator, sys
ops = {"<": operator.lt, "<=": operator.le, "==": operator.eq, ">=": operator.ge, ">": operator.gt}
sys.exit(0 if ops[sys.argv[2]](int(sys.argv[1]), int(sys.argv[3])) else 1)
' "$1" "$2" "$3"
}

function bank_balance() {
  mocad_q bank balances "$1" | jq -r '.balances[] | select(.denom == "amoca") | .amount'
}

# stream records are read over REST: the CLI query goes through the payment
# precompile, whose ABI cannot return the negative netflow of a paying account
function stream_field() {
  local attempt rec
  for attempt in $(seq 1 5); do
    if rec=$(curl -sf --max-time 10 "${CHAIN_REST}/moca/payment/stream_record/$1"); then
      echo "${rec}" | jq -r ".stream_record.$2 // empty"
      return 0
    fi
    sleep 2
  done
  echo "failed to read the stream record of $1" >&2
  return 1
}

function payment_accounts_of() {
  local json
  json=$(mocad_q payment get-payment-accounts-by-owner "$1" 2>/dev/null || echo '{}')
  echo "${json}" | jq -c '.paymentAccounts // .payment_accounts // []'
}

function moca_cmd() {
  ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt "$@"
}

# moca-cmd would otherwise take the first SP the chain lists as the bucket's
# primary, whatever its status
function in_service_primary_sp() {
  mocad_q sp storage-providers | jq -r '[.sps[] | select(.status == "STATUS_IN_SERVICE")][0].operator_address // empty'
}

# The last 64-hex hash moca-cmd printed must be confirmed on chain, as an EVM
# receipt with status 0x1 or a cosmos tx with code 0. The exit code alone does
# not prove inclusion: bucket create never waits for its EVM receipt.
function assert_tx_ok() {
  local out=$1
  local label=$2
  local hash status code attempt

  hash=$(echo "${out}" | grep -oiE '(0x)?[0-9a-f]{64}' | tail -1 | sed 's/^0x//' || true)
  if [ -z "${hash}" ]; then
    echo "${label}: no tx hash in the command output"
    return 1
  fi
  for attempt in $(seq 1 30); do
    status=$(evm_rpc eth_getTransactionReceipt "[\"0x${hash}\"]" | jq -r '.result.status // empty' || true)
    if [ "${status}" = "0x1" ]; then
      echo "${label}: EVM receipt status=0x1 (0x${hash:0:12}...)"
      return 0
    fi
    if [ "${status}" = "0x0" ]; then
      echo "${label}: EVM tx 0x${hash} reverted"
      return 1
    fi
    code=$(curl -s "${CHAIN_RPC}/tx?hash=0x${hash}" | jq -r '.result.tx_result.code // empty' || true)
    if [ "${code}" = "0" ]; then
      echo "${label}: cosmos tx code=0 (0x${hash:0:12}...)"
      return 0
    fi
    if [ -n "${code}" ]; then
      echo "${label}: cosmos tx 0x${hash} failed with code ${code}"
      return 1
    fi
    sleep 2
  done
  echo "${label}: tx 0x${hash} not found on chain"
  return 1
}

# object rm prints no hash and swallows a failed result: gate deletes on the
# resource actually disappearing from chain state
function wait_gone() {
  local label=$1
  shift
  local attempt

  for attempt in $(seq 1 30); do
    if ! mocad_q "$@" >/dev/null 2>&1; then
      echo "${label}: gone on chain"
      return 0
    fi
    sleep 2
  done
  echo "${label}: still on chain after deletion"
  return 1
}

function assert_object_sealed() {
  local bucket=$1
  local object=$2
  local want_size=$3
  local want_type=$4
  local info status is_updating size ctype

  info=$(mocad_q storage head-object "${bucket}" "${object}")
  status=$(echo "${info}" | jq -r '.object_info.object_status')
  is_updating=$(echo "${info}" | jq -r '.object_info.is_updating')
  size=$(echo "${info}" | jq -r '.object_info.payload_size')
  ctype=$(echo "${info}" | jq -r '.object_info.content_type')
  if [ "${status}" != "OBJECT_STATUS_SEALED" ] || [ "${is_updating}" = "true" ]; then
    echo "object ${object}: status=${status} is_updating=${is_updating}, expected sealed with no update pending"
    return 1
  fi
  if [ "${size}" != "${want_size}" ] || [ "${ctype}" != "${want_type}" ]; then
    echo "object ${object}: payload_size=${size} content_type=${ctype}, expected ${want_size} / ${want_type}"
    return 1
  fi
  echo "object ${object} sealed on chain with payload_size=${size} content_type=${ctype}"
}

# poll until the object is sealed on chain with the expected size and type
function wait_object_sealed() {
  local bucket=$1
  local object=$2
  local want_size=$3
  local want_type=$4
  local timeout=${5:-180}

  wait_until "${timeout}" 5 "object ${bucket}/${object}" assert_object_sealed_quiet "${bucket}" "${object}" "${want_size}" "${want_type}"
  retry_cmd 12 10 "head object ${bucket}/${object}" \
    ./moca-cmd -c ./config.toml --home ./ object head "moca://${bucket}/${object}"
  if ! grep -q "object_status: OBJECT_STATUS_SEALED" <<<"${RETRY_CMD_OUTPUT}"; then
    echo "object head does not show OBJECT_STATUS_SEALED for ${bucket}/${object}"
    return 1
  fi
}

function assert_object_sealed_quiet() {
  assert_object_sealed "$@" 2>/dev/null
}

function bucket_head_shows_owner() {
  local bucket=$1
  local out

  out=$(./moca-cmd -c ./config.toml --home ./ bucket head "moca://${bucket}" 2>&1) || true
  if grep -q "bucket_name:\"${bucket}\"" <<<"${out}" && grep -qi "owner:\"${TEST_ACCOUNT_ADDRESS}\"" <<<"${out}"; then
    echo "bucket_name=${bucket} owner=${TEST_ACCOUNT_ADDRESS}"
    return 0
  fi
  echo "$(head -n 1 <<<"${out}" | cut -c1-160)"
  return 1
}

# bucket create only broadcasts: poll bucket head until it shows the bucket under the test account
function assert_bucket_head() {
  wait_until "${2:-180}" "${3:-5}" "bucket ${1} head" bucket_head_shows_owner "$1"
}

#############################################################
# bucket ls / object ls are served from the SP's BsDB, which #
# only the blocksyncer populates from chain events           #
#############################################################
function test_list_via_metadata() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  retry_cmd 24 5 "bucket ls shows ${BUCKET_NAME}" \
    bash -c "./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt bucket ls | grep -w ${BUCKET_NAME}"
  retry_cmd 24 5 "object ls shows example.json" \
    bash -c "./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object ls moca://${BUCKET_NAME} | grep -w example.json"
  retry_cmd 24 5 "object ls shows random_file" \
    bash -c "./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object ls moca://${BUCKET_NAME} | grep -w random_file"
}

###################################################################
# storage fee lifecycle: deposit -> store -> delete -> withdraw    #
# fees stream from the bucket's payment account while the object  #
# is stored, stop on delete, and the unstreamed deposit comes back #
###################################################################
function test_storage_fee_reclaim() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  local owner="${TEST_ACCOUNT_ADDRESS}"
  local bucket
  bucket="spfee-$(date +%s)"
  local object="fee_reclaim_object.bin"
  local deposit="1000000000000000000"      # 1 MOCA
  local withdraw="500000000000000000"      # 0.5 MOCA, under the withdraw timelock threshold
  local min_remaining="900000000000000000" # a tiny object stored for about reserve_time costs dust
  local gas_allowance="10000000000000000"  # 0.01 MOCA for the withdraw tx gas
  local reserve_time pa_count_before pa_addr bucket_payment object_status
  local bank_pre bank_post static_0 sealed_at netflow buffer wait_secs
  local netflow_after lock_after static_after bank_before bank_after static_final expected_static
  local out

  # the reserve window is the minimum charge on early deletion; the localup
  # genesis sets 60s like the live networks, the code default is 180 days
  reserve_time=$(mocad_q payment params | jq -r '.params.versioned_params.reserve_time // empty')
  if [ -z "${reserve_time}" ] || [ "${reserve_time}" -gt 300 ]; then
    echo "reserve_time is '${reserve_time}', expected the localup genesis value of at most 300s"
    exit 1
  fi
  echo "reserve_time=${reserve_time}s"

  echo "--- dedicated payment account ---"
  pa_count_before=$(payment_accounts_of "${owner}" | jq 'length')
  out=$(moca_cmd payment-account create)
  echo "${out}"
  assert_tx_ok "${out}" "create payment account"
  pa_addr=""
  for _ in $(seq 1 15); do
    pa_addr=$(payment_accounts_of "${owner}" | jq -r "select(length > ${pa_count_before}) | .[-1] // empty")
    if [ -n "${pa_addr}" ]; then
      break
    fi
    sleep 2
  done
  if [ -z "${pa_addr}" ]; then
    echo "no new payment account for ${owner}"
    exit 1
  fi
  echo "payment account: ${pa_addr}"

  bank_pre=$(bank_balance "${owner}")
  out=$(moca_cmd payment-account deposit --toAddress "${pa_addr}" --amount "${deposit}")
  echo "${out}"
  assert_tx_ok "${out}" "deposit"
  sleep 4
  static_0=$(stream_field "${pa_addr}" static_balance)
  bank_post=$(bank_balance "${owner}")
  # a fresh account with no flows holds the deposit exactly
  if [ "${static_0:-0}" != "${deposit}" ]; then
    echo "fresh payment account static balance is '${static_0}', expected exactly ${deposit}"
    exit 1
  fi
  if ! bigcmp "$(bigsub "${bank_pre}" "${bank_post}")" ">=" "${deposit}"; then
    echo "owner bank did not decrease by the deposit (pre=${bank_pre} post=${bank_post})"
    exit 1
  fi
  echo "deposit reflected: static_balance=${static_0}, owner bank down by at least the deposit"

  echo "--- store: bucket and sealed object billed to the payment account ---"
  out=$(moca_cmd bucket create --primarySP "$(in_service_primary_sp)" --paymentAddress "${pa_addr}" moca://${bucket})
  echo "${out}"
  assert_tx_ok "${out}" "create bucket"
  retry_cmd 12 5 "head bucket ${bucket}" mocad_q storage head-bucket "${bucket}"
  bucket_payment=$(mocad_q storage head-bucket "${bucket}" | jq -r '.bucket_info.payment_address // empty' | tr 'A-F' 'a-f')
  if [ "${bucket_payment}" != "$(echo "${pa_addr}" | tr 'A-F' 'a-f')" ]; then
    echo "bucket payment address is '${bucket_payment}', expected ${pa_addr}"
    exit 1
  fi
  echo "bucket billed to the dedicated payment account"

  echo "fee reclaim test $(date) ${RANDOM}" >./fee_reclaim_object.bin
  # object put polls until OBJECT_STATUS_SEALED; the timeout caps its one-hour
  # wait when a regression leaves the object unsealed
  timeout 600 ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --contentType "application/octet-stream" ./fee_reclaim_object.bin moca://${bucket}/${object}
  sealed_at=$(date +%s)
  object_status=$(mocad_q storage head-object "${bucket}" "${object}" | jq -r '.object_info.object_status // empty')
  if [ "${object_status}" != "OBJECT_STATUS_SEALED" ]; then
    echo "object status is '${object_status}', expected OBJECT_STATUS_SEALED"
    exit 1
  fi
  netflow=$(stream_field "${pa_addr}" netflow_rate)
  buffer=$(stream_field "${pa_addr}" buffer_balance)
  echo "stored: netflow_rate=${netflow} buffer_balance=${buffer}"
  if ! bigcmp "${netflow:-0}" "<" 0; then
    echo "expected a negative netflow rate while the object is stored, got '${netflow}'"
    exit 1
  fi
  if ! bigcmp "${buffer:-0}" ">" 0; then
    echo "expected a positive buffer balance while the object is stored, got '${buffer}'"
    exit 1
  fi

  echo "--- delete after the reserve window ---"
  wait_secs=$((reserve_time - ($(date +%s) - sealed_at) + 10))
  if [ "${wait_secs}" -gt 0 ]; then
    echo "waiting ${wait_secs}s for the reserve window to lapse"
    sleep "${wait_secs}"
  fi
  moca_cmd object rm moca://${bucket}/${object}
  wait_gone "object ${object}" storage head-object "${bucket}" "${object}"
  out=$(moca_cmd bucket rm moca://${bucket})
  echo "${out}"
  assert_tx_ok "${out}" "delete bucket"
  wait_gone "bucket ${bucket}" storage head-bucket "${bucket}"
  sleep 4

  netflow_after=$(stream_field "${pa_addr}" netflow_rate)
  lock_after=$(stream_field "${pa_addr}" lock_balance)
  static_after=$(stream_field "${pa_addr}" static_balance)
  echo "deleted: netflow_rate=${netflow_after} lock_balance=${lock_after} static_balance=${static_after}"
  if [ "${netflow_after:-1}" != "0" ]; then
    echo "netflow rate should return to 0 after deletion, got '${netflow_after}'"
    exit 1
  fi
  if [ "${lock_after:-1}" != "0" ]; then
    echo "lock balance should be 0 after deletion, got '${lock_after}'"
    exit 1
  fi
  # storage was charged for the stored seconds, and for nothing beyond them
  if ! bigcmp "${static_after:-0}" "<" "${deposit}"; then
    echo "static balance did not decrease at all (${static_after}); storage was never charged"
    exit 1
  fi
  if ! bigcmp "${static_after:-0}" ">=" "${min_remaining}"; then
    echo "expected at least 90% of the deposit to remain after a short store, got ${static_after}"
    exit 1
  fi

  echo "--- reclaim: withdraw the unstreamed deposit ---"
  bank_before=$(bank_balance "${owner}")
  out=$(moca_cmd payment-account withdraw --fromAddress "${pa_addr}" --amount "${withdraw}")
  echo "${out}"
  assert_tx_ok "${out}" "withdraw"
  sleep 4
  static_final=$(stream_field "${pa_addr}" static_balance)
  bank_after=$(bank_balance "${owner}")
  expected_static=$(bigsub "${static_after}" "${withdraw}")
  echo "withdrawn: static_balance=${static_final}, owner bank ${bank_before} -> ${bank_after}"
  if [ "${static_final:-0}" != "${expected_static}" ]; then
    echo "static balance after withdraw is '${static_final}', expected ${expected_static}"
    exit 1
  fi
  if ! bigcmp "$(bigsub "${bank_after}" "${bank_before}")" ">=" "$(bigsub "${withdraw}" "${gas_allowance}")"; then
    echo "owner bank did not receive the withdrawal (before=${bank_before} after=${bank_after})"
    exit 1
  fi
  echo "storage fees streamed while stored, stopped on delete, and the deposit was reclaimed"
}

##########################################################################
# delegated object lifecycle: the primary SP creates the object on chain #
# on the uploader's behalf (MsgDelegateCreateObject) and later replaces  #
# its content (MsgDelegateUpdateObjectContent); the uploader signs no tx #
##########################################################################
function test_delegated_object() {
  set -e
  cd "${workspace}"/moca-cmd/build/
  local bucket
  bucket="spdelegated-$(date +%s)"
  local object="delegated_object.txt"
  local url="moca://${bucket}/${object}"
  local content_type="application/octet-stream"
  local put_help nonce_before out

  # a moca-cmd ref that predates the flag would fail on an unknown option, not on the flow
  put_help=$(./moca-cmd object put -h 2>/dev/null || true)
  if ! echo "${put_help}" | grep -q -- '--delegate'; then
    echo "moca-cmd at ${MOCA_CMD_TAG} has no 'object put --delegate'; the delegated case needs mocachain/moca-cmd#23"
    exit 1
  fi

  echo "delegated put $(date) ${RANDOM}" >./delegated_v1.txt
  echo "delegated update $(date) ${RANDOM} - second revision, deliberately longer than the first" >./delegated_v2.txt

  echo "--- create bucket ---"
  retry_cmd 6 10 "create bucket ${bucket}" moca_cmd bucket create --primarySP "$(in_service_primary_sp)" moca://${bucket}
  retry_cmd 12 10 "head bucket ${bucket}" moca_cmd bucket head moca://${bucket}
  sleep 4
  nonce_before=$(evm_nonce "${TEST_ACCOUNT_ADDRESS}")

  echo "--- object put --delegate: the SP creates the object on chain and the command blocks until SEALED ---"
  # moca-cmd reports command errors on stdout with exit 0, so assert on the output;
  # the timeout caps its one-hour seal wait when a regression leaves the object unsealed
  out=$(timeout 600 ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object put --delegate --contentType "${content_type}" ./delegated_v1.txt "${url}" 2>&1 || true)
  echo "${out}"
  if ! echo "${out}" | grep -q "upload ${object} to ${url}"; then
    echo "delegated object put did not reach OBJECT_STATUS_SEALED"
    dump_sp_logs
    exit 1
  fi
  if echo "${out}" | grep -q "transaction hash:"; then
    echo "delegated put signed a local transaction"
    exit 1
  fi
  assert_object_sealed "${bucket}" "${object}" "$(wc -c <./delegated_v1.txt | tr -d ' ')" "${content_type}"
  if [ "$(evm_nonce "${TEST_ACCOUNT_ADDRESS}")" != "${nonce_before}" ]; then
    echo "uploader nonce moved during the delegated put"
    exit 1
  fi
  echo "uploader nonce unchanged by the delegated put (${nonce_before})"

  echo "--- object get matches the delegated put ---"
  get_object_until_match "${url}" ./downloaded_object ./delegated_v1.txt

  echo "--- object update --delegate: the SP replaces the content on chain and the command blocks until re-sealed ---"
  out=$(timeout 600 ./moca-cmd -c ./config.toml --home ./ --passwordfile password.txt object update --delegate --contentType "${content_type}" ./delegated_v2.txt "${url}" 2>&1 || true)
  echo "${out}"
  if ! echo "${out}" | grep -q "update ${object} to ${url}"; then
    echo "delegated object update did not reach OBJECT_STATUS_SEALED"
    dump_sp_logs
    exit 1
  fi
  if echo "${out}" | grep -q "transaction hash:"; then
    echo "delegated update signed a local transaction"
    exit 1
  fi
  assert_object_sealed "${bucket}" "${object}" "$(wc -c <./delegated_v2.txt | tr -d ' ')" "${content_type}"
  if [ "$(evm_nonce "${TEST_ACCOUNT_ADDRESS}")" != "${nonce_before}" ]; then
    echo "uploader nonce moved during the delegated update"
    exit 1
  fi
  echo "uploader nonce unchanged by the delegated update (${nonce_before})"

  echo "--- object get matches the delegated update ---"
  get_object_until_match "${url}" ./downloaded_object ./delegated_v2.txt

  echo "--- cleanup ---"
  moca_cmd object rm "${url}"
  wait_gone "object ${object}" storage head-object "${bucket}" "${object}"
  moca_cmd bucket rm moca://${bucket}
  echo "delegated put and update sealed without the uploader signing a transaction"
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
  echo 'run list buckets and objects through the SP metadata service'
  test_list_via_metadata
  echo 'run storage fee reclaim lifecycle'
  test_storage_fee_reclaim
  echo 'run delegated object put and update'
  test_delegated_object
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
# fail unless the go test log shows passing tests and no skipped or failed ones
function check_go_test_results() {
  local log=$1
  local passed skipped failed

  passed=$(grep -cE '^[[:space:]]*--- PASS: ' "${log}" || true)
  skipped=$(grep -cE '^[[:space:]]*--- SKIP: ' "${log}" || true)
  failed=$(grep -cE '^[[:space:]]*--- FAIL: ' "${log}" || true)
  echo "go test results: ${passed} passed, ${skipped} skipped, ${failed} failed"
  if [ "${passed}" -eq 0 ]; then
    echo "no test ran: the -run filter matched nothing"
    return 1
  fi
  if [ "${skipped}" -ne 0 ]; then
    echo "${skipped} test(s) were skipped:"
    grep -E '^[[:space:]]*--- SKIP: ' "${log}"
    return 1
  fi
  [ "${failed}" -eq 0 ]
}

function run_go_sdk_e2e() {
  set +e
  local log="${workspace}/.github/action-logs/go-sdk-e2e-go-test.log"
  local rc

  cd "${workspace}"/moca-go-sdk/
  echo 'run moca go sdk e2e test'
  export MOCA_E2E_ENDPOINT="http://localhost:26657"
  export MOCA_E2E_EVM_ENDPOINT="http://localhost:8545"
  export MOCA_E2E_CHAIN_ID="moca_5151-1"
  export MOCA_E2E_LOCALUP_DIR="${workspace}/moca/deployment/localup/.local"
  mkdir -p "$(dirname "${log}")"
  go test -count=1 -timeout 30m -v ./e2e -run "${SDK_E2E_RUN}" 2>&1 | tee "${log}"
  rc=${PIPESTATUS[0]}
  if [ "${rc}" -ne 0 ]; then
    dump_sp_logs
    exit "${rc}"
  fi
  if ! check_go_test_results "${log}"; then
    dump_sp_logs
    exit 1
  fi
  echo "make e2e_test successful."
}

function main() {
  CMD=${1:-}
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
  --runFeeReclaim)
    test_storage_fee_reclaim
    ;;
  --runDelegated)
    test_delegated_object
    ;;
  --runSPExit)
    run_sp_exit_e2e
    ;;
  --runSDKE2E)
    build_moca-go-sdk
    run_go_sdk_e2e
    ;;
  *)
    echo "unknown mode '${CMD}'" >&2
    exit 1
    ;;
  esac
}

main "$@"
