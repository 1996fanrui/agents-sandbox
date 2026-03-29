#!/usr/bin/env bash
# Usage: ./scripts/cleanup.sh
#   Stop agboxd, remove all Docker containers/networks created by agents-sandbox,
#   and wipe local state data. Exec logs are preserved.

set -e

read -r -p "This will stop agboxd and wipe all state data (exec logs preserved). Continue? [y/N] " REPLY
if [[ ! "$REPLY" =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 0
fi

echo "==> Stopping agboxd..."
systemctl --user stop agboxd 2>/dev/null || true

echo "==> Removing Docker containers..."
CONTAINERS=$(docker ps -aq --filter "label=io.github.1996fanrui.agents-sandbox.sandbox-id" 2>/dev/null)
if [ -n "$CONTAINERS" ]; then
    docker rm -f $CONTAINERS
else
    echo "    (none)"
fi

echo "==> Removing Docker networks..."
NETWORKS=$(docker network ls --filter "name=agbox-net" -q 2>/dev/null)
if [ -n "$NETWORKS" ]; then
    docker network rm $NETWORKS
else
    echo "    (none)"
fi

echo "==> Cleaning local state (exec logs preserved)..."
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/agents-sandbox"
RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/agbox"

rm -f "$DATA_DIR/ids.db"
# state/ may contain root-owned files from container shadow copies
if [ -d "$DATA_DIR/state" ]; then
    sudo rm -rf "$DATA_DIR/state"
fi
rm -rf "$RUNTIME_DIR"

echo ""
echo "Done. Preserved exec logs: $DATA_DIR/exec-logs"
