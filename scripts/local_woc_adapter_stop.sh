#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pid_file="${root_dir}/.local/woc-adapter.pid"

if [[ ! -f "${pid_file}" ]]; then
  echo "woc adapter is not running"
  exit 0
fi

pid="$(cat "${pid_file}")"
if [[ "${pid}" != "" ]] && kill -0 "${pid}" 2>/dev/null; then
  kill "${pid}" 2>/dev/null || true
  for _ in $(seq 1 20); do
    if ! kill -0 "${pid}" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done
fi

rm -f "${pid_file}"
echo "woc adapter stopped"
