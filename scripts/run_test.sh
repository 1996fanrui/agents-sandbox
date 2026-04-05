#!/usr/bin/env bash
# Usage:
#   ./scripts/run_test.sh
#   ./scripts/run_test.sh lint
#   ./scripts/run_test.sh test

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${PROJECT_ROOT}"

run_lints() {
  if command -v pre-commit >/dev/null 2>&1; then
    pre-commit run --all-files
  else
    python3 -m pip install pre-commit
    pre-commit run --all-files
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

run_sdk_tests() {
  local sdk_root="${PROJECT_ROOT}/sdk/python"
  if [ ! -f "${sdk_root}/pyproject.toml" ]; then
    return 0
  fi
  if ! command -v uv >/dev/null 2>&1; then
    echo "uv command not found." >&2
    exit 1
  fi

  (
    cd "${sdk_root}"
    uv run pytest tests/ \
      --ignore=tests/test_real_runtime.py \
      --ignore=tests/test_network_isolation.py
  )
}

case "${1:-all}" in
  lint)
    run_lints
    ;;
  test)
    run_go_tests
    run_sdk_tests
    ;;
  all)
    run_lints
    run_go_tests
    run_sdk_tests
    ;;
  *)
    echo "Unsupported mode: ${1}" >&2
    exit 2
    ;;
esac
