#!/usr/bin/env bash
# Usage:
#   ./scripts/run_test.sh
#   ./scripts/run_test.sh lint
#   ./scripts/run_test.sh test

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${PROJECT_ROOT}"

resolve_dev_lints_command() {
  if command -v dev-lints >/dev/null 2>&1; then
    command -v dev-lints
    return 0
  fi

  return 1
}

run_lints() {
  local dev_lints_cmd=""
  if dev_lints_cmd="$(resolve_dev_lints_command)"; then
    "${dev_lints_cmd}" --project-root "${PROJECT_ROOT}"
  else
    echo "Shared dev-lints command not found in PATH. Skipping shared lints."
  fi

  for lint_script in "${PROJECT_ROOT}"/scripts/lints/*.sh; do
    [ -f "${lint_script}" ] || continue
    bash "${lint_script}"
  done
}

run_go_tests() {
  if ! command -v go >/dev/null 2>&1; then
    echo "go command not found." >&2
    exit 1
  fi

  go test ./...
}

case "${1:-all}" in
  lint)
    run_lints
    ;;
  test)
    run_go_tests
    ;;
  all)
    run_lints
    run_go_tests
    ;;
  *)
    echo "Unsupported mode: ${1}" >&2
    exit 2
    ;;
esac
