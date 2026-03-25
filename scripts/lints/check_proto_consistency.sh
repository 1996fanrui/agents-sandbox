#!/usr/bin/env bash
# Usage:
#   bash scripts/lints/check_proto_consistency.sh
#
# Regenerates proto bindings and verifies the checked-in files are up to date.
# Exits with code 1 if any generated file differs from what is committed.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${PROJECT_ROOT}"

echo "Regenerating proto bindings..."
bash "${PROJECT_ROOT}/scripts/generate_proto.sh"

echo "Checking for uncommitted proto differences..."
if ! git diff --exit-code api/generated/ sdk/python/src/agents_sandbox/_generated/; then
  echo "" >&2
  echo "ERROR: Generated proto bindings are out of date." >&2
  echo "Run 'bash scripts/generate_proto.sh' and commit the results." >&2
  exit 1
fi

echo "Proto bindings are consistent."
