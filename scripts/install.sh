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

  # Also update any other copies of agboxd/agbox that exist under ~/bin/,
  # which on some systems precedes ~/.local/bin/ in PATH.
  if [[ "${INSTALL_DIR}" != "${HOME}/bin" && -d "${HOME}/bin" ]]; then
    for bin in agboxd agbox; do
      if [[ -f "${HOME}/bin/${bin}" ]]; then
        install -m 755 "${TMP_DIR}/${bin}_${SUFFIX}" "${HOME}/bin/${bin}"
        echo "Updated  : ${HOME}/bin/${bin}"
      fi
    done
  fi
fi

# ---------------------------------------------------------------------------
# Linux: systemd user service
# ---------------------------------------------------------------------------
_setup_systemd() {
  local service_dir="${HOME}/.config/systemd/user"
  local service_file="${service_dir}/agboxd.service"

  mkdir -p "${service_dir}"

  # Replace symlink or regular file with the managed service unit.
  cat > "${service_file}.tmp" <<EOF
[Unit]
Description=Agents Sandbox Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/agboxd
SuccessExitStatus=143
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF

  # Atomically replace (handles symlink case: unlink then rename).
  rm -f "${service_file}"
  mv "${service_file}.tmp" "${service_file}"

  # Kill any agboxd process not managed by this service (e.g. from a previous
  # source-build startup) so it releases the host lock before we restart.
  pkill -x agboxd 2>/dev/null || true
  sleep 1

  systemctl --user daemon-reload
  systemctl --user enable agboxd
  systemctl --user restart agboxd

  echo ""
  echo "agboxd service restarted."
  echo "  Status : systemctl --user status agboxd"
  echo "  Logs   : journalctl --user -u agboxd -f"
}

# ---------------------------------------------------------------------------
# macOS: launchd user agent
# ---------------------------------------------------------------------------
_setup_launchd() {
  local agents_dir="${HOME}/Library/LaunchAgents"
  local plist_label="io.github.agents-sandbox.agboxd"
  local plist_file="${agents_dir}/${plist_label}.plist"

  mkdir -p "${agents_dir}"

  cat > "${plist_file}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${plist_label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${INSTALL_DIR}/agboxd</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>${HOME}/Library/Logs/agboxd.log</string>
  <key>StandardErrorPath</key>
  <string>${HOME}/Library/Logs/agboxd.log</string>
</dict>
</plist>
EOF

  # Unload existing agent if running, then reload.
  launchctl unload "${plist_file}" 2>/dev/null || true
  launchctl load -w "${plist_file}"

  echo ""
  echo "agboxd launchd agent loaded."
  echo "  Status : launchctl list | grep agboxd"
  echo "  Logs   : tail -f ${HOME}/Library/Logs/agboxd.log"
}

# ---------------------------------------------------------------------------
# Setup service for the current OS
# ---------------------------------------------------------------------------
echo ""
echo "Setting up agboxd service..."
if [[ "${OS}" == "linux" ]]; then
  _setup_systemd
elif [[ "${OS}" == "darwin" ]]; then
  _setup_launchd
fi

echo ""
echo "Installation complete. agents-sandbox ${VERSION} is ready."
echo "  agboxd : ${INSTALL_DIR}/agboxd"
echo "  agbox  : ${INSTALL_DIR}/agbox"
