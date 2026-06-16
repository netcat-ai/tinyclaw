#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
database_url="${DATABASE_URL:-postgres://tinyclaw:tinyclaw@127.0.0.1:54329/tinyclaw?sslmode=disable}"
full="${TINYCLAW_VERIFY_FULL:-false}"

run() {
  echo
  echo "==> $*"
  "$@"
}

run_in_dir() {
  local dir="$1"
  shift
  echo
  echo "==> (cd ${dir} && $*)"
  (cd "${dir}" && "$@")
}

run "${root_dir}/scripts/local_status.sh"

run_in_dir "${root_dir}/web/control" pnpm run lint --fix
run_in_dir "${root_dir}/web/control" pnpm run build

run_in_dir "${root_dir}" golangci-lint run --fix ./...
run_in_dir "${root_dir}" go test ./...

run_in_dir "${root_dir}/tinybridge" golangci-lint run --fix ./...
run_in_dir "${root_dir}/tinybridge" go test ./...

if [[ "${full}" == "true" ]]; then
  echo
  echo "==> (cd ${root_dir} && CORE_E2E_DATABASE_URL=... STORAGE_TEST_DATABASE_URL=... go test ./...)"
  (
    cd "${root_dir}"
    CORE_E2E_DATABASE_URL="${database_url}" \
      STORAGE_TEST_DATABASE_URL="${database_url}" \
      go test ./...
  )
fi

run "${root_dir}/scripts/local_smoke.sh"
run "${root_dir}/scripts/local_admin_smoke.sh"
run "${root_dir}/scripts/local_wechat_adapter_smoke.sh"
run "${root_dir}/scripts/local_status.sh"

echo
echo "local verification passed"
