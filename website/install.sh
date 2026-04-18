#!/bin/bash
# Install or upgrade agents-sandbox (agboxd daemon + agbox CLI) from GitHub Releases.
#
# Usage:
#   curl -fsSL https://agents-sandbox.com/install.sh | bash
#   curl -fsSL https://agents-sandbox.com/install.sh | bash -s -- v0.1.1
#   curl -fsSL https://agents-sandbox.com/install.sh | bash -s -- --pre
#
# Examples:
#   bash install.sh                   # install latest stable release
#   bash install.sh v0.1.1            # install specific stable version
#   bash install.sh v0.1.1-alpha.3    # install specific pre-release
#   bash install.sh --pre             # install latest release (including pre-releases)
#
# Prerequisites: curl and Docker must be installed.
#
# What this script does:
#   1. Checks that Docker is installed and the daemon is running.
#   2. Downloads agboxd and agbox binaries for your OS/arch from GitHub Releases.
#   3. Installs binaries to ~/.local/bin.
#   4. Linux : creates/updates ~/.config/systemd/user/agboxd.service and restarts it.
#   5. macOS : creates/updates ~/Library/LaunchAgents/io.github.agents-sandbox.agboxd.plist
#              and restarts the launchd agent.

set -e

# ---------------------------------------------------------------------------
# Inline service helpers (so the script works standalone via curl|bash).
# Self-contained so the script works standalone via curl|bash.
# ---------------------------------------------------------------------------

_setup_systemd() {
  local agboxd_bin="$1"
  local service_dir="${HOME}/.config/systemd/user"
  local service_file="${service_dir}/agboxd.service"

  mkdir -p "${service_dir}"

  cat > "${service_file}.tmp" <<EOF
[Unit]
Description=Agents Sandbox Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${agboxd_bin}
SuccessExitStatus=143
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF

  rm -f "${service_file}"
  mv "${service_file}.tmp" "${service_file}"

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

_setup_launchd() {
  local agboxd_bin="$1"
  local agents_dir="${HOME}/Library/LaunchAgents"
  local plist_label="io.github.agents-sandbox.agboxd"
  local plist_file="${agents_dir}/${plist_label}.plist"

  mkdir -p "${agents_dir}"

  cat > "${plist_file}" <<EOF2
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${plist_label}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${agboxd_bin}</string>
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
EOF2

  launchctl unload "${plist_file}" 2>/dev/null || true
  launchctl load -w "${plist_file}"

  echo ""
  echo "agboxd launchd agent loaded."
  echo "  Status : launchctl list | grep agboxd"
  echo "  Logs   : tail -f ${HOME}/Library/Logs/agboxd.log"
}

setup_agboxd_service() {
  local agboxd_bin="$1"
  local os
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"

  echo ""
  echo "Setting up agboxd service..."
  case "${os}" in
    linux)  _setup_systemd "${agboxd_bin}" ;;
    darwin) _setup_launchd "${agboxd_bin}" ;;
    *)
      echo "Error: unsupported OS for service setup: ${os}" >&2
      exit 1
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Pre-flight: check Docker
# ---------------------------------------------------------------------------
check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "Error: Docker is not installed." >&2
    echo "" >&2
    case "$(uname -s)" in
      Darwin) echo "  Install Docker Desktop: https://docs.docker.com/desktop/setup/install/mac-install/" >&2 ;;
      Linux)  echo "  Install Docker Engine:  https://docs.docker.com/engine/install/" >&2 ;;
    esac
    exit 1
  fi

  if ! docker info >/dev/null 2>&1; then
    echo "Error: Docker daemon is not running." >&2
    echo "" >&2
    case "$(uname -s)" in
      Darwin) echo "  Please start Docker Desktop and try again." >&2 ;;
      Linux)  echo "  Try: sudo systemctl start docker" >&2 ;;
    esac
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Install directory: ~/.local/bin (XDG standard, same convention as uv, pipx, etc.)
# ---------------------------------------------------------------------------
detect_install_dir() {
  echo "${HOME}/.local/bin"
}

GITHUB_REPO="1996fanrui/agents-sandbox"

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
# Pre-flight checks
# ---------------------------------------------------------------------------
check_docker

if ! command -v curl >/dev/null 2>&1; then
  echo "Error: curl is required but not found." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Detect install directory
# ---------------------------------------------------------------------------
INSTALL_DIR=$(detect_install_dir)

# ---------------------------------------------------------------------------
# Resolve version.
# Default: latest stable release.
# --pre : latest release including pre-releases.
# v*    : exact version.
# ---------------------------------------------------------------------------
INCLUDE_PRE=false
VERSION="${1:-}"
if [[ "${VERSION}" == "--pre" ]]; then
  INCLUDE_PRE=true
  VERSION=""
fi

if [[ -z "${VERSION}" ]]; then
  if [[ "${INCLUDE_PRE}" == true ]]; then
    echo "Fetching latest release version (including pre-releases)..."
    API_RESPONSE=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases")
    VERSION=$(echo "${API_RESPONSE}" | grep -m1 '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  else
    echo "Fetching latest stable release version..."
    # /releases/latest returns only the most recent non-prerelease.
    API_RESPONSE=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest")
    VERSION=$(echo "${API_RESPONSE}" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  fi
  if [[ -z "${VERSION}" ]]; then
    echo "Error: could not determine latest version from GitHub API." >&2
    exit 1
  fi
fi
echo "Version : ${VERSION}"
echo "OS/Arch : ${OS}/${ARCH}"
echo "Install : ${INSTALL_DIR}"

# ---------------------------------------------------------------------------
# Check if already up-to-date.
# ---------------------------------------------------------------------------
WANT_VERSION="${VERSION#v}"
NEED_DOWNLOAD=true

if [[ -x "${INSTALL_DIR}/agbox" ]]; then
  CURRENT_VERSION=$("${INSTALL_DIR}/agbox" version 2>/dev/null || true)
  if [[ "${CURRENT_VERSION}" == "${WANT_VERSION}" ]]; then
    NEED_DOWNLOAD=false
    echo ""
    echo "Already at version ${VERSION}, skipping download."
  fi
fi

# ---------------------------------------------------------------------------
# Download binaries via curl (no gh CLI required)
# ---------------------------------------------------------------------------
SUFFIX="${OS}_${ARCH}"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "${TMP_DIR}"' EXIT

if [[ "${NEED_DOWNLOAD}" == true ]]; then
  BASE_URL="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"

  echo ""
  echo "Downloading agboxd_${SUFFIX}..."
  curl -fSL "${BASE_URL}/agboxd_${SUFFIX}" -o "${TMP_DIR}/agboxd_${SUFFIX}"

  echo "Downloading agbox_${SUFFIX}..."
  curl -fSL "${BASE_URL}/agbox_${SUFFIX}" -o "${TMP_DIR}/agbox_${SUFFIX}"

  mkdir -p "${INSTALL_DIR}"
  install -m 755 "${TMP_DIR}/agboxd_${SUFFIX}" "${INSTALL_DIR}/agboxd"
  install -m 755 "${TMP_DIR}/agbox_${SUFFIX}"  "${INSTALL_DIR}/agbox"
  echo ""
  echo "Installed: ${INSTALL_DIR}/agboxd"
  echo "Installed: ${INSTALL_DIR}/agbox"

  # Grant CAP_NET_ADMIN so the daemon can manage nftables rules for sandbox
  # network isolation without running as root. Only applicable on Linux.
  #
  # Also install a polkit rule so agboxd can create transient systemd slices
  # (agbox.slice / agbox-<id>.slice) via system D-Bus without prompting the
  # user during normal sandbox usage. Resource limits (cpu_limit/memory_limit)
  # require this; macOS does not use cgroup slices so no polkit rule is needed.
  if [[ "${OS}" == "linux" ]]; then
    if command -v setcap >/dev/null 2>&1; then
      echo "Granting CAP_NET_ADMIN and installing polkit rule (requires sudo)..."
      sudo setcap cap_net_admin+ep "${INSTALL_DIR}/agboxd"
    else
      echo "Installing polkit rule (requires sudo)..."
    fi

    POLKIT_RULE_FILE="/etc/polkit-1/rules.d/50-agbox-slice.rules"
    CURRENT_USER="$(id -un)"
    POLKIT_TMP="$(mktemp)"
    cat > "${POLKIT_TMP}" <<POLKIT
// Allow ${CURRENT_USER} to manage agbox systemd slices without authentication.
// Required for agboxd to create per-sandbox cgroup slices (cpu_limit/memory_limit)
// via system D-Bus. Installed by agents-sandbox install.sh.
polkit.addRule(function(action, subject) {
    if (action.id === "org.freedesktop.systemd1.manage-units" &&
        subject.user === "${CURRENT_USER}" &&
        action.lookup("unit") !== undefined &&
        action.lookup("unit").match(/^agbox/)) {
        return polkit.Result.YES;
    }
});
POLKIT
    if ! sudo diff -q "${POLKIT_RULE_FILE}" "${POLKIT_TMP}" > /dev/null 2>&1; then
      sudo cp "${POLKIT_TMP}" "${POLKIT_RULE_FILE}"
      sudo systemctl restart polkit
    fi
    rm -f "${POLKIT_TMP}"
  fi
fi

# ---------------------------------------------------------------------------
# Ensure install directory is in PATH.
# Auto-adds to shell profile unless AGBOX_NO_MODIFY_PATH=1.
# ---------------------------------------------------------------------------
_ensure_path() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) return ;;
  esac

  local path_line="export PATH=\"${INSTALL_DIR}:\$PATH\""
  local shell_name
  shell_name=$(basename "${SHELL:-/bin/bash}")

  local profile=""
  case "${shell_name}" in
    zsh)  profile="${HOME}/.zshrc" ;;
    bash)
      # On macOS, bash reads .bash_profile for login shells; on Linux, .bashrc.
      if [[ "$(uname -s)" == "Darwin" ]]; then
        profile="${HOME}/.bash_profile"
      else
        profile="${HOME}/.bashrc"
      fi
      ;;
    *)    profile="${HOME}/.profile" ;;
  esac

  if [[ -n "${profile}" ]]; then
    # Avoid duplicate entries.
    if ! grep -qF "${INSTALL_DIR}" "${profile}" 2>/dev/null; then
      echo "" >> "${profile}"
      echo "# Added by agents-sandbox installer" >> "${profile}"
      echo "${path_line}" >> "${profile}"
      echo "Added ${INSTALL_DIR} to PATH in ${profile}"
    fi
    # Make it available in the current process for the rest of this script.
    export PATH="${INSTALL_DIR}:${PATH}"
  fi
}

_ensure_path

# ---------------------------------------------------------------------------
# Setup service for the current OS
# ---------------------------------------------------------------------------
setup_agboxd_service "${INSTALL_DIR}/agboxd"

echo ""
echo "Installation complete. agents-sandbox ${VERSION} is ready."
echo "  agboxd : ${INSTALL_DIR}/agboxd"
echo "  agbox  : ${INSTALL_DIR}/agbox"
