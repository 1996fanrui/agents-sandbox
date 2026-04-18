#!/bin/bash
# Build agboxd and agbox from local source and install to the best available
# directory in PATH, then set up (or restart) the agboxd service.
#
# Usage:
#   ./scripts/install_local.sh
#
# Prerequisites: go and Docker must be installed.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_BIN="${GO_BIN:-go}"

if ! command -v "${GO_BIN}" >/dev/null 2>&1; then
  echo "Error: ${GO_BIN} is required to build." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Install directory: ~/.local/bin (XDG standard, same convention as uv, pipx, etc.)
# ---------------------------------------------------------------------------
INSTALL_DIR="${HOME}/.local/bin"

# ---------------------------------------------------------------------------
# Build both binaries
# ---------------------------------------------------------------------------
echo "Building agboxd and agbox from source..."
cd "${PROJECT_ROOT}"
mkdir -p "${INSTALL_DIR}"

"${GO_BIN}" build -ldflags="-s -w" -o "${INSTALL_DIR}/agboxd" ./cmd/agboxd
echo "Installed: ${INSTALL_DIR}/agboxd"

"${GO_BIN}" build -ldflags="-s -w" -o "${INSTALL_DIR}/agbox" ./cmd/agbox
echo "Installed: ${INSTALL_DIR}/agbox"

# Grant CAP_NET_ADMIN so the daemon can manage nftables rules for sandbox
# network isolation without running as root.
#
# Also install a polkit rule so agboxd can create transient systemd slices
# (agbox.slice / agbox-<id>.slice) via system D-Bus without prompting the
# user during normal sandbox usage. Resource limits (cpu_limit/memory_limit)
# require this; macOS does not use cgroup slices so no polkit rule is needed.
if [[ "$(uname -s)" == "Linux" ]]; then
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
// via system D-Bus. Installed by agents-sandbox install_local.sh.
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
      if [[ "$(uname -s)" == "Darwin" ]]; then
        profile="${HOME}/.bash_profile"
      else
        profile="${HOME}/.bashrc"
      fi
      ;;
    *)    profile="${HOME}/.profile" ;;
  esac

  if [[ -n "${profile}" ]]; then
    if ! grep -qF "${INSTALL_DIR}" "${profile}" 2>/dev/null; then
      echo "" >> "${profile}"
      echo "# Added by agents-sandbox installer" >> "${profile}"
      echo "${path_line}" >> "${profile}"
      echo "Added ${INSTALL_DIR} to PATH in ${profile}"
    fi
    export PATH="${INSTALL_DIR}:${PATH}"
  fi
}

_ensure_path

# ---------------------------------------------------------------------------
# Set up and restart the agboxd service
# ---------------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
AGBOXD_BIN="${INSTALL_DIR}/agboxd"

echo ""
echo "Setting up agboxd service..."

case "${OS}" in
  linux)
    SERVICE_DIR="${HOME}/.config/systemd/user"
    SERVICE_FILE="${SERVICE_DIR}/agboxd.service"
    mkdir -p "${SERVICE_DIR}"

    cat > "${SERVICE_FILE}.tmp" <<EOF
[Unit]
Description=Agents Sandbox Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${AGBOXD_BIN}
SuccessExitStatus=143
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF

    rm -f "${SERVICE_FILE}"
    mv "${SERVICE_FILE}.tmp" "${SERVICE_FILE}"

    pkill -x agboxd 2>/dev/null || true
    sleep 1

    systemctl --user daemon-reload
    systemctl --user enable agboxd
    systemctl --user restart agboxd

    echo ""
    echo "agboxd service restarted."
    echo "  Status : systemctl --user status agboxd"
    echo "  Logs   : journalctl --user -u agboxd -f"
    ;;

  darwin)
    AGENTS_DIR="${HOME}/Library/LaunchAgents"
    PLIST_LABEL="io.github.agents-sandbox.agboxd"
    PLIST_FILE="${AGENTS_DIR}/${PLIST_LABEL}.plist"
    mkdir -p "${AGENTS_DIR}"

    cat > "${PLIST_FILE}" <<EOF2
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${PLIST_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${AGBOXD_BIN}</string>
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

    launchctl unload "${PLIST_FILE}" 2>/dev/null || true
    launchctl load -w "${PLIST_FILE}"

    echo ""
    echo "agboxd launchd agent loaded."
    echo "  Status : launchctl list | grep agboxd"
    echo "  Logs   : tail -f ${HOME}/Library/Logs/agboxd.log"
    ;;

  *)
    echo "Error: unsupported OS for service setup: ${OS}" >&2
    exit 1
    ;;
esac

echo ""
echo "Local install complete."
echo "  agboxd : ${INSTALL_DIR}/agboxd"
echo "  agbox  : ${INSTALL_DIR}/agbox"
