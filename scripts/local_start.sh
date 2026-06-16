#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${root_dir}/.local"
pid_file="${state_dir}/clawman.pid"
legacy_pid_file="${state_dir}/tinyclaw.pid"
log_file="${state_dir}/clawman.log"
bin_file="${state_dir}/clawman"

database_url="${DATABASE_URL:-postgres://tinyclaw:tinyclaw@127.0.0.1:54329/tinyclaw?sslmode=disable}"
api_token="${CLAWMAN_API_TOKEN:-dev-token}"
admin_secret="${CLAWMAN_ADMIN_SECRET:-dev-admin}"
control_addr="${CONTROL_API_ADDR:-127.0.0.1:8081}"
metrics_addr="${METRICS_ADDR:-127.0.0.1:9090}"
woc_panel_base_url="${WOC_PANEL_BASE_URL:-http://127.0.0.1:36080}"
woc_username="${WOC_USERNAME:-admin}"
woc_password="${WOC_PASSWORD:-wechat}"
agent_runner="${AGENT_RUNNER:-codex}"
codex_workdir="${CODEX_WORKDIR:-${root_dir}}"
codex_runner_timeout="${CODEX_RUNNER_TIMEOUT:-5m}"
build_control="${TINYCLAW_BUILD_CONTROL:-true}"
foreground="${TINYCLAW_FOREGROUND:-false}"

for existing_pid_file in "${pid_file}" "${legacy_pid_file}"; do
  if [[ -f "${existing_pid_file}" ]]; then
    existing_pid="$(cat "${existing_pid_file}")"
    if [[ "${existing_pid}" != "" ]] && kill -0 "${existing_pid}" 2>/dev/null; then
      echo "clawman already running with pid ${existing_pid}"
      exit 0
    fi
    rm -f "${existing_pid_file}"
  fi
done

mkdir -p "${state_dir}"

docker compose -f "${root_dir}/compose.local.yml" up -d postgres >/dev/null

if [[ "${build_control}" == "true" ]]; then
  (cd "${root_dir}/web/control" && pnpm install --silent && pnpm run build)
fi

(
  cd "${root_dir}"
  go build -o "${bin_file}" .
  if [[ "${foreground}" == "true" ]]; then
    sh -c 'echo "$PPID"' >"${pid_file}"
    echo "clawman running in foreground at http://${control_addr}"
    DATABASE_URL="${database_url}" \
    CLAWMAN_API_TOKEN="${api_token}" \
    CLAWMAN_ADMIN_SECRET="${admin_secret}" \
    CONTROL_API_ADDR="${control_addr}" \
    METRICS_ADDR="${metrics_addr}" \
    WOC_PANEL_BASE_URL="${woc_panel_base_url}" \
    WOC_USERNAME="${woc_username}" \
    WOC_PASSWORD="${woc_password}" \
    AGENT_RUNNER="${agent_runner}" \
    CODEX_WORKDIR="${codex_workdir}" \
    CODEX_RUNNER_TIMEOUT="${codex_runner_timeout}" \
    exec "${bin_file}"
  fi
  DATABASE_URL="${database_url}" \
  CLAWMAN_API_TOKEN="${api_token}" \
  CLAWMAN_ADMIN_SECRET="${admin_secret}" \
  CONTROL_API_ADDR="${control_addr}" \
  METRICS_ADDR="${metrics_addr}" \
  WOC_PANEL_BASE_URL="${woc_panel_base_url}" \
  WOC_USERNAME="${woc_username}" \
  WOC_PASSWORD="${woc_password}" \
  AGENT_RUNNER="${agent_runner}" \
  CODEX_WORKDIR="${codex_workdir}" \
  CODEX_RUNNER_TIMEOUT="${codex_runner_timeout}" \
  python3 - "${bin_file}" "${log_file}" "${pid_file}" <<'PY'
import os
import subprocess
import sys

bin_file, log_file, pid_file = sys.argv[1:4]
with open(log_file, "ab", buffering=0) as log:
    proc = subprocess.Popen(
        [bin_file],
        stdin=subprocess.DEVNULL,
        stdout=log,
        stderr=subprocess.STDOUT,
        env=os.environ.copy(),
        start_new_session=True,
    )
with open(pid_file, "w", encoding="utf-8") as pid:
    pid.write(f"{proc.pid}\n")
PY
)

for _ in $(seq 1 60); do
  if curl -fsS "http://${control_addr}/healthz" >/dev/null 2>&1; then
    echo "clawman running at http://${control_addr}"
    echo "admin ui: http://${control_addr}/admin/"
    echo "metrics: http://${metrics_addr}/metrics"
    echo "log: ${log_file}"
    exit 0
  fi
  sleep 1
done

echo "clawman did not become healthy; last log lines:" >&2
tail -n 40 "${log_file}" >&2 || true
exit 1
