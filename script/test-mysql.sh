#!/usr/bin/env bash
# Provide the MySQL server the blocksyncer tests need on 127.0.0.1:3306 (root/root):
#   up    use a server already listening there, otherwise start a loopback-bound
#         mysql:8.0 container named moca-sp-test-mysql and wait until it answers
#   down  remove that container
# Point BLOCKSYNCER_TEST_DB_ADDRESS (and _USER/_PASSWORD/_NAME) at another server
# to skip this entirely.
set -euo pipefail

container=moca-sp-test-mysql
address="${BLOCKSYNCER_TEST_DB_ADDRESS:-127.0.0.1:3306}"
host="${address%:*}"
port="${address##*:}"

listening() {
  (exec 3<>"/dev/tcp/${host}/${port}") >/dev/null 2>&1
}

case "${1:-up}" in
up)
  if [ -n "${BLOCKSYNCER_TEST_DB_ADDRESS:-}" ]; then
    echo "--> blocksyncer tests use the mysql server at ${address} (BLOCKSYNCER_TEST_DB_ADDRESS)"
    exit 0
  fi
  if listening; then
    echo "--> blocksyncer tests use the mysql server already listening on ${address}"
    exit 0
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "--> no mysql server on ${address} and no docker to start one; the blocksyncer tests will fail" >&2
    echo "    start a MySQL 8 with root/root there, or set BLOCKSYNCER_TEST_DB_ADDRESS" >&2
    exit 0
  fi
  echo "--> starting mysql:8.0 in docker as ${container} on ${address}"
  docker rm -f "${container}" >/dev/null 2>&1 || true
  docker run -d --name "${container}" -p "${host}:${port}:3306" -e MYSQL_ROOT_PASSWORD=root mysql:8.0 >/dev/null
  for _ in $(seq 1 60); do
    if docker exec "${container}" mysql -uroot -proot -e 'SELECT 1' >/dev/null 2>&1; then
      echo "--> mysql is ready"
      exit 0
    fi
    sleep 2
  done
  echo "--> mysql in ${container} did not become ready" >&2
  docker logs --tail 20 "${container}" >&2 || true
  exit 1
  ;;
down)
  docker rm -f "${container}" >/dev/null 2>&1 && echo "--> removed ${container}" || echo "--> ${container} was not running"
  ;;
*)
  echo "usage: $0 [up|down]" >&2
  exit 2
  ;;
esac
