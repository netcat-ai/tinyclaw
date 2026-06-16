#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${root_dir}/.local"
pid_file="${state_dir}/woc-adapter.pid"
log_file="${state_dir}/woc-adapter.log"
bin_file="${state_dir}/woc-adapter"

if [[ -f "${pid_file}" ]]; then
  existing_pid="$(cat "${pid_file}")"
  if [[ "${existing_pid}" != "" ]] && kill -0 "${existing_pid}" 2>/dev/null; then
    echo "woc adapter already running with pid ${existing_pid}"
    exit 0
  fi
  rm -f "${pid_file}"
fi

mkdir -p "${state_dir}"

(
  cd "${root_dir}/tinybridge"
  go build -o "${bin_file}" ./cmd/woc-adapter
  CLAWMAN_BASE_URL="${CLAWMAN_BASE_URL:-http://127.0.0.1:8081}" \
    CLAWMAN_API_TOKEN="${CLAWMAN_API_TOKEN:-dev-token}" \
    WOC_BASE_URL="${WOC_BASE_URL:-http://127.0.0.1:36080}" \
    WOC_USERNAME="${WOC_USERNAME:-admin}" \
    WOC_PASSWORD="${WOC_PASSWORD:-wechat}" \
    WOC_CURSOR_PATH="${WOC_CURSOR_PATH:-${root_dir}/.local/woc-cursors.json}" \
    WOC_ONCE="${WOC_ONCE:-false}" \
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

echo "woc adapter started"
echo "log: ${log_file}"
