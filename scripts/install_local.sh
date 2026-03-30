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
# Detect best install directory already in PATH.
# Priority: ~/.local/bin (if in PATH) > ~/bin (if in PATH)
#           > /usr/local/bin (if writable) > ~/.local/bin (fallback)
# ---------------------------------------------------------------------------
_in_path() {
  case ":${PATH}:" in
    *":$1:"*) return 0 ;;
    *)        return 1 ;;
  esac
}

if _in_path "${HOME}/.local/bin"; then
  INSTALL_DIR="${HOME}/.local/bin"
elif _in_path "${HOME}/bin"; then
  INSTALL_DIR="${HOME}/bin"
elif [[ -d "/usr/local/bin" && -w "/usr/local/bin" ]]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="${HOME}/.local/bin"
fi

# ---------------------------------------------------------------------------
# Build both binaries
# ---------------------------------------------------------------------------
echo "Building agboxd and agbox from source..."
cd "${PROJECT_ROOT}"
mkdir -p "${INSTALL_DIR}"

"${GO_BIN}" build -o "${INSTALL_DIR}/agboxd" ./cmd/agboxd
echo "Installed: ${INSTALL_DIR}/agboxd"

"${GO_BIN}" build -o "${INSTALL_DIR}/agbox" ./cmd/agbox
echo "Installed: ${INSTALL_DIR}/agbox"

# Sync stale copies in ~/bin/ if they exist and we installed elsewhere.
if [[ "${INSTALL_DIR}" != "${HOME}/bin" && -d "${HOME}/bin" ]]; then
  for bin in agboxd agbox; do
    if [[ -f "${HOME}/bin/${bin}" ]]; then
      cp "${INSTALL_DIR}/${bin}" "${HOME}/bin/${bin}"
      chmod 755 "${HOME}/bin/${bin}"
      echo "Updated  : ${HOME}/bin/${bin}"
    fi
  done
fi

# ---------------------------------------------------------------------------
# Warn if install directory is not in PATH
# ---------------------------------------------------------------------------
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo ""
    echo "WARNING: ${INSTALL_DIR} is not in your PATH."
    echo "  Add it by running:"
    SHELL_NAME=$(basename "${SHELL:-/bin/bash}")
    case "${SHELL_NAME}" in
      zsh)  echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc && source ~/.zshrc" ;;
      fish) echo "    fish_add_path ${INSTALL_DIR}" ;;
      *)    echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc && source ~/.bashrc" ;;
    esac
    ;;
esac

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
