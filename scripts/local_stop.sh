#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${root_dir}/.local"
pid_file="${state_dir}/tinyclaw.pid"

if [[ ! -f "${pid_file}" ]]; then
  echo "tinyclaw pid file not found"
  exit 0
fi

pid="$(cat "${pid_file}")"
rm -f "${pid_file}"

if [[ "${pid}" == "" ]] || ! kill -0 "${pid}" 2>/dev/null; then
  echo "tinyclaw is not running"
  exit 0
fi

child_pids="$(pgrep -P "${pid}" 2>/dev/null || true)"
if [[ "${child_pids}" != "" ]]; then
  while read -r child_pid; do
    if [[ "${child_pid}" != "" ]] && kill -0 "${child_pid}" 2>/dev/null; then
      kill "${child_pid}" 2>/dev/null || true
    fi
  done <<<"${child_pids}"
fi

kill "${pid}"
for _ in $(seq 1 20); do
  children_alive=false
  if [[ "${child_pids}" != "" ]]; then
    while read -r child_pid; do
      if [[ "${child_pid}" != "" ]] && kill -0 "${child_pid}" 2>/dev/null; then
        children_alive=true
      fi
    done <<<"${child_pids}"
  fi
  if ! kill -0 "${pid}" 2>/dev/null && [[ "${children_alive}" == "false" ]]; then
    echo "tinyclaw stopped"
    exit 0
  fi
  sleep 1
done

if [[ "${child_pids}" != "" ]]; then
  while read -r child_pid; do
    if [[ "${child_pid}" != "" ]]; then
      kill -9 "${child_pid}" 2>/dev/null || true
    fi
  done <<<"${child_pids}"
fi
kill -9 "${pid}" 2>/dev/null || true
echo "tinyclaw stopped"
