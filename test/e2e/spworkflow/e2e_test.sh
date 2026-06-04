#!/usr/bin/env bash

export CGO_CFLAGS="-O -D__BLST_PORTABLE__"
export CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

workspace=${GITHUB_WORKSPACE}

# some constants
# Keep refs override-friendly and default the whole e2e stack to main so the
# chain, cmd and go-sdk all move in lockstep.
MOCA_TAG="${MOCA_TAG:-main}"
MOCA_CMD_TAG="${MOCA_CMD_TAG:-main}"
MOCA_GO_SDK_TAG="${MOCA_GO_SDK_TAG:-main}"
MYSQL_USER="root"
MYSQL_PASSWORD="root"
MYSQL_ADDRESS="127.0.0.1:3306"
TEST_ACCOUNT_ADDRESS=${ACCOUNT_ADDR}
TEST_ACCOUNT_PRIVATE_KEY=${PRIVATE_KEY}
DEV_ACCOUNT_PRIVATE_KEY="2228e392584d902843272c37fd62b8c73c10c81a5ecb901773c9ebe366e937bb"
echo "TEST_ACCOUNT_ADDRESS is ""$TEST_ACCOUNT_ADDRESS"
echo "TEST_ACCOUNT_PRIVATE_KEY is ""$TEST_ACCOUNT_PRIVATE_KEY"

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
        .FundingPrivateKey |= pad64 |
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
  make proto-gen &
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
  ./build/mocad tx bank send validator0 "${TEST_ACCOUNT_ADDRESS}" 500000000000000000000amoca --home "${workspace}"/moca/deployment/localup/.local/validator0 --keyring-backend test --node http://localhost:26657 -y
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
function run_go_sdk_e2e() {
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
  esac
}

main $@
