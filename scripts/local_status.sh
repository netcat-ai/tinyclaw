#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${root_dir}/.local"
pid_file="${state_dir}/tinyclaw.pid"

control_addr="${CONTROL_API_ADDR:-127.0.0.1:8081}"
metrics_addr="${METRICS_ADDR:-127.0.0.1:9090}"
base_url="${CLAWMAN_BASE_URL:-http://${control_addr}}"
metrics_url="${TINYCLAW_METRICS_URL:-http://${metrics_addr}/metrics}"

status=0

trap 'rm -f /tmp/tinyclaw-local-status.out /tmp/tinyclaw-local-status.err' EXIT

check() {
  local name="$1"
  shift
  if "$@" >/tmp/tinyclaw-local-status.out 2>/tmp/tinyclaw-local-status.err; then
    echo "ok   ${name}"
  else
    status=1
    echo "fail ${name}"
    sed 's/^/     /' /tmp/tinyclaw-local-status.err >&2 || true
  fi
}

check_command() {
  local command_name="$1"
  check "command:${command_name}" command -v "${command_name}"
}

check_command docker
check_command go
check_command pnpm
check_command python3
check_command curl
check_command rg
check_command codex

check "postgres compose service" docker compose -f "${root_dir}/compose.local.yml" ps postgres --format json
check "postgres health" bash -c '[[ "$(docker inspect --format "{{.State.Health.Status}}" tinyclaw-postgres-local)" == "healthy" ]]'

if [[ -f "${pid_file}" ]]; then
  pid="$(cat "${pid_file}")"
  if [[ "${pid}" != "" ]] && kill -0 "${pid}" 2>/dev/null; then
    echo "ok   tinyclaw pid:${pid}"
  else
    status=1
    echo "fail tinyclaw pid:${pid:-empty}"
  fi
else
  status=1
  echo "fail tinyclaw pid file missing"
fi

check "healthz" curl -fsS "${base_url}/healthz"
check "admin ui" curl -fsS "${base_url}/admin/"
check "metrics endpoint" curl -fsS "${metrics_url}"

if curl -fsS "${metrics_url}" | rg -q 'tinyclaw_agent_runs_total'; then
  echo "ok   metric:agent_runs"
else
  echo "skip metric:agent_runs no activity yet"
fi

if curl -fsS "${metrics_url}" | rg -q 'tinyclaw_deliveries_total'; then
  echo "ok   metric:deliveries"
else
  echo "skip metric:deliveries no activity yet"
fi

exit "${status}"
