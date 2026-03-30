#!/bin/bash
# Install or upgrade agents-sandbox (agboxd daemon + agbox CLI) from GitHub Releases.
#
# Usage:
#   ./scripts/install.sh [VERSION]
#
# Examples:
#   ./scripts/install.sh                   # install latest release (including pre-releases)
#   ./scripts/install.sh v0.1.1            # install specific stable version
#   ./scripts/install.sh v0.1.1-alpha.3    # install specific pre-release
#
# Prerequisites: gh CLI must be installed and authenticated (gh auth login).
#
# What this script does:
#   1. Downloads agboxd and agbox binaries for your OS/arch from GitHub Releases.
#   2. Installs binaries to ~/.local/bin/.
#   3. Linux : creates/updates ~/.config/systemd/user/agboxd.service and restarts it.
#   4. macOS : creates/updates ~/Library/LaunchAgents/io.github.agents-sandbox.agboxd.plist
#              and restarts the launchd agent.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/service.sh"

GITHUB_REPO="1996fanrui/agents-sandbox"
INSTALL_DIR="${HOME}/.local/bin"

# ---------------------------------------------------------------------------
# Detect OS and architecture
# ---------------------------------------------------------------------------
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "${ARCH}" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Error: unsupported architecture: ${ARCH}" >&2
    exit 1
    ;;
esac

if [[ "${OS}" != "linux" && "${OS}" != "darwin" ]]; then
  echo "Error: unsupported OS: ${OS}" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Resolve version (default: latest release, including pre-releases)
# ---------------------------------------------------------------------------
VERSION="${1:-}"
if [[ -z "${VERSION}" ]]; then
  echo "Fetching latest release version..."
  VERSION=$(gh release list --repo "${GITHUB_REPO}" --limit 1 --json tagName -q '.[0].tagName')
  if [[ -z "${VERSION}" ]]; then
    echo "Error: could not determine latest version. Is 'gh' authenticated?" >&2
    exit 1
  fi
fi
echo "Version : ${VERSION}"
echo "OS/Arch : ${OS}/${ARCH}"

# ---------------------------------------------------------------------------
# Check if already up-to-date across all known install locations.
# agbox version returns bare semver, e.g. 0.1.1-alpha.3.
# We must check every location that could appear in PATH to avoid stale copies.
# ---------------------------------------------------------------------------
WANT_VERSION="${VERSION#v}"  # strip leading 'v'

_agbox_version() {
  local bin="$1"
  [[ -x "${bin}" ]] && "${bin}" version 2>/dev/null || true
}

NEED_DOWNLOAD=false
for _loc in "${INSTALL_DIR}/agbox" "${HOME}/bin/agbox"; do
  _ver=$(_agbox_version "${_loc}")
  if [[ -z "${_ver}" ]]; then
    continue  # not installed at this location
  fi
  if [[ "${_ver}" != "${WANT_VERSION}" ]]; then
    NEED_DOWNLOAD=true
    break
  fi
done

# If no existing installation found at any location, we must download.
if [[ "${NEED_DOWNLOAD}" == false ]]; then
  _any_found=false
  for _loc in "${INSTALL_DIR}/agbox" "${HOME}/bin/agbox"; do
    [[ -x "${_loc}" ]] && _any_found=true && break
  done
  [[ "${_any_found}" == false ]] && NEED_DOWNLOAD=true
fi

if [[ "${NEED_DOWNLOAD}" == false ]]; then
  echo ""
  echo "Already at version ${VERSION}, skipping download."
fi

# ---------------------------------------------------------------------------
# Download binaries into a temp directory
# ---------------------------------------------------------------------------
SUFFIX="${OS}_${ARCH}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "${TMP_DIR}"' EXIT

if [[ "${NEED_DOWNLOAD}" == true ]]; then
  echo ""
  echo "Downloading agboxd_${SUFFIX}..."
  gh release download "${VERSION}" --repo "${GITHUB_REPO}" \
    --pattern "agboxd_${SUFFIX}" --dir "${TMP_DIR}"

  echo "Downloading agbox_${SUFFIX}..."
  gh release download "${VERSION}" --repo "${GITHUB_REPO}" \
    --pattern "agbox_${SUFFIX}" --dir "${TMP_DIR}"

  mkdir -p "${INSTALL_DIR}"
  install -m 755 "${TMP_DIR}/agboxd_${SUFFIX}" "${INSTALL_DIR}/agboxd"
  install -m 755 "${TMP_DIR}/agbox_${SUFFIX}"  "${INSTALL_DIR}/agbox"
  echo ""
  echo "Installed: ${INSTALL_DIR}/agboxd"
  echo "Installed: ${INSTALL_DIR}/agbox"

  sync_bin_copies "${INSTALL_DIR}"
fi

# ---------------------------------------------------------------------------
# Setup service for the current OS (shared helper from lib/service.sh)
# ---------------------------------------------------------------------------
setup_agboxd_service "${INSTALL_DIR}/agboxd"

echo ""
echo "Installation complete. agents-sandbox ${VERSION} is ready."
echo "  agboxd : ${INSTALL_DIR}/agboxd"
echo "  agbox  : ${INSTALL_DIR}/agbox"
