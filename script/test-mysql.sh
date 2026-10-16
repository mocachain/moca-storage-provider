#!/usr/bin/env bash
# Provide the MySQL server the blocksyncer tests need on 127.0.0.1:3306 (root/root):
#   up [ip:port]  use a server already answering there (as is: the suite checks the
#                 credentials), otherwise start a loopback-bound mysql:8.0 container
#                 named moca-sp-test-mysql, or adopt the one another invocation is
#                 starting, and wait until root/root answers inside it
#   down          remove that container
# Point BLOCKSYNCER_TEST_DB_ADDRESS (and _USER/_PASSWORD/_NAME) at another server
# to skip this entirely. TEST_MYSQL_TIMEOUT (seconds, default 120) bounds each phase
# of the wait (until the container exists, then until it answers); TEST_MYSQL_POLL
# (seconds, default 2) paces it.
set -euo pipefail
set +o posix # a failed exec redirection in probe must return, not exit the script

container=moca-sp-test-mysql
image=mysql:8.0
timeout="${TEST_MYSQL_TIMEOUT:-120}"
poll="${TEST_MYSQL_POLL:-2}"
address="${2:-127.0.0.1:3306}"
host="${address%:*}"
port="${address##*:}"
st=absent # the container's docker state as last observed
why=      # what the wait is for, for the timeout message
seen=     # set once the container exists, which is when the wait budget starts

fail() {
  echo "--> $*" >&2
  exit 1
}

# probe: a server that speaks first on host:port the way MySQL does is used and the script
# exits 0; otherwise returns 1 for a listener that stays silent, 2 when nothing listens
probe() {
  { exec 3<>"/dev/tcp/${host}/${port}"; } 2>/dev/null || return 2
  if read -r -t 2 -n 1 _ <&3; then
    exec 3<&- 3>&-
    echo "--> blocksyncer tests use the mysql server already answering on ${address}"
    exit 0
  fi
  exec 3<&- 3>&-
  return 1
}

# state: the container's docker state or "absent"; fails when docker itself is unusable
state() {
  local out
  out=$(docker ps -a --filter 'name=^/'"${container}"'$' --format '{{.State}}' 2>/dev/null) || return 1
  echo "${out:-absent}"
}

# published: the host:port the container publishes mysql on
published() {
  docker inspect -f '{{range $p, $b := .HostConfig.PortBindings}}{{if eq $p "3306/tcp"}}{{(index $b 0).HostIp}}:{{(index $b 0).HostPort}}{{end}}{{end}}' "${container}" 2>/dev/null
}

# ready: root/root works over TCP inside the container; the socket-only server the image
# runs while initializing does not count
ready() {
  docker exec "${container}" mysql -h127.0.0.1 -uroot -proot -e 'SELECT 1' >/dev/null 2>&1
}

# round: one look at the container and the port; exits when a server answers or nothing
# sensible can be done, returns to let up() poll again
round() {
  local rc=0 out
  if ! out=$(state); then
    if [ -n "${seen}" ]; then
      why="docker stopped answering" # transient: look again next round
      return 0
    fi
    probe || fail "docker is not usable and no mysql server answers on ${address}; start docker, or a MySQL 8 with root/root there, or set BLOCKSYNCER_TEST_DB_ADDRESS"
  fi
  st=${out}
  case "${st}" in
  absent)
    probe || rc=$?
    if [ "${rc}" -eq 1 ]; then
      why="something listens on ${address} but does not answer like MySQL"
      return 0
    fi
    # the container name is the lock: the daemon lets exactly one invocation create it
    if out=$(docker run -d --name "${container}" -p "${host}:${port}:3306" -e MYSQL_ROOT_PASSWORD=root "${image}" 2>&1); then
      echo "--> started ${image} in docker as ${container} on ${address}"
    else
      case "${out}" in
      *Conflict*) ;; # another invocation created it first: adopt it
      *"port is already allocated"* | *"address already in use"* | *"Bind for "*)
        # the port was taken meanwhile; the created container is looked at next round
        probe || why="docker run failed: ${out}" ;;
      *) echo "${out}" >&2; fail "docker could not start ${container}" ;;
      esac
    fi
    # the wait budget starts once the container exists, whoever created it
    seen=1
    SECONDS=0
    ;;
  dead) fail "${container} is dead; run 'make test-mysql-down' and try again" ;;
  running | created | exited | paused)
    if ! out=$(published); then
      why="docker inspect ${container} failed" # transient: look again next round
      return 0
    fi
    [ "${out}" = "${address}" ] || fail "${container} publishes mysql on '${out:-nothing}', not ${address}; run 'make test-mysql-down' or set BLOCKSYNCER_TEST_DB_ADDRESS to the server you mean"
    [ -n "${seen}" ] || { seen=1; SECONDS=0; }
    why="${container} is ${st}"
    case "${st}" in
    running)
      if ready; then
        echo "--> mysql is ready on ${address}"
        exit 0
      fi
      why="${container} is running but root/root does not answer over TCP yet"
      ;;
    paused) docker unpause "${container}" >/dev/null 2>&1 || true ;;
    *)
      # a concurrent start may win, or the port may meanwhile be served by somebody else
      if ! out=$(docker start "${container}" 2>&1); then
        probe || why="docker start ${container} failed: ${out}"
      fi
      ;;
    esac
    ;;
  *) why="${container} is ${st}" ;; # restarting, removing: look again next round
  esac
}

up() {
  case "${host}" in
  '' | *[!0-9.]*) fail "usage: $0 up [ip:port]" ;;
  esac
  case "${port}" in
  '' | *[!0-9]*) fail "usage: $0 up [ip:port]" ;;
  esac
  if [ -n "${BLOCKSYNCER_TEST_DB_ADDRESS:-}" ]; then
    echo "--> blocksyncer tests use the mysql server at ${BLOCKSYNCER_TEST_DB_ADDRESS} (BLOCKSYNCER_TEST_DB_ADDRESS)"
    exit 0
  fi
  if ! command -v docker >/dev/null 2>&1; then
    probe || true # a server answering there is used; otherwise the suite fails explicitly later
    echo "--> no mysql server on ${address} and no docker to start one; the blocksyncer tests will fail" >&2
    echo "    start a MySQL 8 with root/root there, or set BLOCKSYNCER_TEST_DB_ADDRESS" >&2
    exit 0
  fi
  why="waiting for ${address}"
  while [ "${SECONDS}" -lt "${timeout}" ]; do
    round
    sleep "${poll}"
  done
  echo "--> no mysql server answered on ${address} within ${timeout}s: ${why}" >&2
  if [ "${st}" != absent ]; then
    echo "    'make test-mysql-down' removes the container; its last log lines:" >&2
    docker logs --tail 20 "${container}" >&2 || true
  fi
  exit 1
}

case "${1:-up}" in
up) up ;;
down) docker rm -fv "${container}" >/dev/null 2>&1 && echo "--> removed ${container}" || echo "--> ${container} was not running" ;;
*)
  echo "usage: $0 [up [ip:port]|down]" >&2
  exit 2
  ;;
esac
