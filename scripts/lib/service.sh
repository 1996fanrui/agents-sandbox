#!/usr/bin/env bash
# Shared helpers for install scripts.
#
# Usage (sourced by other scripts):
#   source "$(dirname "${BASH_SOURCE[0]}")/lib/service.sh"
#   sync_bin_copies "/path/to/install_dir"
#   setup_agboxd_service "/path/to/agboxd"

set -e

# ---------------------------------------------------------------------------
# Sync binaries to ~/bin/ if stale copies exist there.
# Some systems have ~/bin/ earlier in PATH than ~/.local/bin/, so leaving
# old copies there shadows the newly installed binaries.
#   sync_bin_copies <install_dir>
# ---------------------------------------------------------------------------
sync_bin_copies() {
  local install_dir="$1"
  if [[ "${install_dir}" != "${HOME}/bin" && -d "${HOME}/bin" ]]; then
    for bin in agboxd agbox; do
      if [[ -f "${HOME}/bin/${bin}" ]]; then
        cp "${install_dir}/${bin}" "${HOME}/bin/${bin}"
        chmod 755 "${HOME}/bin/${bin}"
        echo "Updated  : ${HOME}/bin/${bin}"
      fi
    done
  fi
}

# ---------------------------------------------------------------------------
# Linux: systemd user service
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
  local agboxd_bin="$1"
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
EOF

  launchctl unload "${plist_file}" 2>/dev/null || true
  launchctl load -w "${plist_file}"

  echo ""
  echo "agboxd launchd agent loaded."
  echo "  Status : launchctl list | grep agboxd"
  echo "  Logs   : tail -f ${HOME}/Library/Logs/agboxd.log"
}

# ---------------------------------------------------------------------------
# Public entry point: detect OS and set up the appropriate service.
#   setup_agboxd_service <absolute_path_to_agboxd_binary>
# ---------------------------------------------------------------------------
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
