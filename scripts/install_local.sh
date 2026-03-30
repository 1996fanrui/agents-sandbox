#!/bin/bash
# Build agboxd and agbox from local source and install to ~/.local/bin/,
# then set up (or restart) the agboxd service.
#
# Usage:
#   ./scripts/install_local.sh
#
# Prerequisites: go must be installed.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
INSTALL_DIR="${HOME}/.local/bin"
GO_BIN="${GO_BIN:-go}"

source "${SCRIPT_DIR}/lib/service.sh"

if ! command -v "${GO_BIN}" >/dev/null 2>&1; then
  echo "Error: ${GO_BIN} is required to build." >&2
  exit 1
fi

# Build both binaries
echo "Building agboxd and agbox from source..."
cd "${PROJECT_ROOT}"
mkdir -p "${INSTALL_DIR}"

"${GO_BIN}" build -o "${INSTALL_DIR}/agboxd" ./cmd/agboxd
echo "Installed: ${INSTALL_DIR}/agboxd"

"${GO_BIN}" build -o "${INSTALL_DIR}/agbox" ./cmd/agbox
echo "Installed: ${INSTALL_DIR}/agbox"

sync_bin_copies "${INSTALL_DIR}"

# Set up and restart the service
setup_agboxd_service "${INSTALL_DIR}/agboxd"

echo ""
echo "Local install complete."
echo "  agboxd : ${INSTALL_DIR}/agboxd"
echo "  agbox  : ${INSTALL_DIR}/agbox"
