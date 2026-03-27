#!/bin/bash
# agboxd startup script (foreground mode for systemd user service)
#
# Usage:
#   systemctl --user start agboxd
#   systemctl --user stop agboxd
#   journalctl --user -u agboxd -f
#   GO_BIN=go ./scripts/agboxd_start.sh
#
# This script builds and starts agboxd with fixed platform paths.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BUILD_DIR="${PROJECT_ROOT}/.build"
GO_BIN="${GO_BIN:-go}"

if ! command -v "${GO_BIN}" >/dev/null 2>&1; then
    echo "Error: ${GO_BIN} is required to build agboxd." >&2
    exit 1
fi

mkdir -p "${BUILD_DIR}"

cd "${PROJECT_ROOT}"
"${GO_BIN}" build -o "${BUILD_DIR}/agboxd" ./cmd/agboxd

exec "${BUILD_DIR}/agboxd"
