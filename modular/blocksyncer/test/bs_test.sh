#!/usr/bin/env bash

set -euo pipefail

export CGO_CFLAGS="-O -D__BLST_PORTABLE__"
export CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

MYSQL_USER="${MYSQL_USER:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-root}"
MYSQL_ADDRESS="${MYSQL_ADDRESS:-127.0.0.1:3306}"
BLOCKSYNCER_TEST_DB_NAME="${BLOCKSYNCER_TEST_DB_NAME:-block_syncer}"
BLOCKSYNCER_TEST_VERIFY_TIMEOUT_SECONDS="${BLOCKSYNCER_TEST_VERIFY_TIMEOUT_SECONDS:-120}"
BLOCKSYNCER_TEST_VERIFY_INTERVAL_MS="${BLOCKSYNCER_TEST_VERIFY_INTERVAL_MS:-500}"
TESTCOVERAGE_THRESHOLD="${TESTCOVERAGE_THRESHOLD:-60}"
# GITHUB_WORKSPACE=. # for local testing
workspace=${GITHUB_WORKSPACE}

function make_config() {
  cd "${workspace}" || exit 1
  make install-tools
  make build
  if [ ! -x ./build/moca-sp ]; then
    echo "failed to build ./build/moca-sp"
    exit 1
  fi
  ./build/moca-sp config.dump
  if [ ! -f config.toml ]; then
    echo "config.dump did not generate config.toml"
    exit 1
  fi
  cp config.toml "${workspace}"/modular/blocksyncer/config.toml
  cd "${workspace}"/modular/blocksyncer/ || exit 1

  # db
  sed -i -e "s/User = '.*'/User = '${MYSQL_USER}'/g" config.toml
  sed -i -e "s/Passwd = '.*'/Passwd = '${MYSQL_PASSWORD}'/g" config.toml
  sed -i -e "s/^Address = '.*'/Address = '${MYSQL_ADDRESS}'/g" config.toml
  sed -i -e "s/Database = '.*'/Database = '${BLOCKSYNCER_TEST_DB_NAME}'/g" config.toml

  # chain
  sed -i -e "s/ChainID = '.*'/ChainID = 'moca_5151-1'/g" config.toml
  sed -i -e "s/ChainAddress = \[.*\]/ChainAddress = \['http:\/\/127.0.0.1:8080'\]/g" config.toml
  sed -i -e "s/RpcAddress = \[.*\]/RpcAddress = \['http:\/\/127.0.0.1:8545'\]/g" config.toml
  python3 - <<'PY'
from pathlib import Path

path = Path("config.toml")
lines = path.read_text().splitlines()
in_log = False
for i, line in enumerate(lines):
    stripped = line.strip()
    if stripped == "[Log]":
        in_log = True
        continue
    if in_log and stripped.startswith("[") and stripped.endswith("]"):
        in_log = False
    if in_log and stripped.startswith("Path = "):
        lines[i] = "Path = './bs-logs/blocksyncer.log'"
        break
path.write_text("\n".join(lines) + "\n")
PY
  mkdir -p bs-logs

  # blocksyncer
  sed -i -e "s/Modules = \[\]/Modules = \[\'epoch\',\'bucket\',\'object\',\'payment\',\'group\',\'permission\',\'storage_provider\'\,\'prefix_tree\'\,\'virtual_group\'\,\'sp_exit_events\'\,\'object_id_map\'\,\'general\'\]/g" config.toml
  WORKERS=10
  sed -i -e "s/Workers = 0/Workers = ${WORKERS}/g" config.toml
  sed -i -e "s/DataMonitor = .*/DataMonitor = true/g" config.toml
  DURATION=5
  sed -i -e "s/DataStatisticsDuration = 0/DataStatisticsDuration = ${DURATION}/g" config.toml
  sed -i -e "s/EnableStorage = .*/EnableStorage = true/g" config.toml
  sed -i -e "s/MaximumStorageCount = 0/MaximumStorageCount = 50/g" config.toml

  sed -i -e "s/Server = \[.*\]/Server = \['BlockSyncer'\]/g" config.toml

  echo "succeed to make config"
}

function reset_db() {
  hostname="${MYSQL_ADDRESS%:*}"
  port="${MYSQL_ADDRESS##*:}"
  if [ "${MYSQL_ADDRESS}" = "${hostname}" ]; then
    hostname="localhost"
  fi
  if [ "${MYSQL_ADDRESS}" = "${port}" ]; then
    port="3306"
  fi
  DATABASE="${BLOCKSYNCER_TEST_DB_NAME}"
  mysql -u ${MYSQL_USER} -h ${hostname} -P ${port} -p${MYSQL_PASSWORD} -e "drop database if exists ${DATABASE}"
  mysql -u ${MYSQL_USER} -h ${hostname} -P ${port} -p${MYSQL_PASSWORD} -e "create database ${DATABASE}"
}

function test_bs() {
  cd "${workspace}"/modular/blocksyncer/ || exit 1
  export BLOCKSYNCER_TEST_DB_USER="${MYSQL_USER}"
  export BLOCKSYNCER_TEST_DB_PASSWORD="${MYSQL_PASSWORD}"
  export BLOCKSYNCER_TEST_DB_ADDRESS="${MYSQL_ADDRESS}"
  export BLOCKSYNCER_TEST_DB_NAME="${BLOCKSYNCER_TEST_DB_NAME}"
  export BLOCKSYNCER_TEST_VERIFY_TIMEOUT_SECONDS
  export BLOCKSYNCER_TEST_VERIFY_INTERVAL_MS
  if go test -v -coverprofile=coverage.txt -covermode=atomic -coverpkg=github.com/mocachain/moca-storage-provider/modular/blocksyncer/...; then
    echo "bs_e2e_test runs successful."
  else
    echo "blocksyncer go test failed, dumping recent logs if available..."
    test -f bs-logs/blocksyncer.log && tail -n 300 bs-logs/blocksyncer.log
    exit 1
  fi

  go tool cover -func coverage.txt

  echo "Quality Gate: checking test coverage is above threshold ..."
  echo "Threshold             : ${TESTCOVERAGE_THRESHOLD} %"
  totalCoverage=$(go tool cover -func=coverage.txt | grep total | grep -Eo '[0-9]+\.[0-9]+')
  echo "Current test coverage : $totalCoverage %"
  if (($(echo "$totalCoverage ${TESTCOVERAGE_THRESHOLD}" | awk '{print ($1 >= $2)}'))); then
    echo "OK"
  else
    echo "Current test coverage is below threshold. Please add more unit tests or adjust threshold to a lower value."
    echo "Failed"
    exit 1
  fi
}

function main() {
  CMD=$1
  case ${CMD} in
  --makecfg)
    make_config
    ;;
  --reset)
    reset_db
    ;;
  --start_test)
    test_bs
    ;;
  esac
}

main $@
