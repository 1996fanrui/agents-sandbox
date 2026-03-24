#!/bin/bash
# Usage:
#   Build this image and run it through agents-sandbox or Docker with HOST_UID
#   and HOST_GID set.
#
# Required env vars:
#   HOST_UID
#   HOST_GID
#
# This entrypoint creates a non-root runtime user whose home directory stays
# aligned with the built-in resource targets under /home/agbox.
set -e

if [ -z "$HOST_UID" ] || [ -z "$HOST_GID" ]; then
    echo "ERROR: HOST_UID and HOST_GID must be set" >&2
    exit 1
fi

USERNAME="agbox"
USER_HOME="/home/agbox"

if ! getent group "$HOST_GID" >/dev/null 2>&1; then
    groupadd -g "$HOST_GID" "$USERNAME"
fi

if ! getent passwd "$HOST_UID" >/dev/null 2>&1; then
    useradd -m -s /bin/bash -u "$HOST_UID" -g "$HOST_GID" -d "$USER_HOME" "$USERNAME"
else
    EXISTING_USER=$(getent passwd "$HOST_UID" | cut -d: -f1)
    if [ "$EXISTING_USER" != "$USERNAME" ]; then
        usermod -l "$USERNAME" "$EXISTING_USER" 2>/dev/null || true
        EXISTING_USER="$USERNAME"
    fi
    if [ "$(getent passwd "$HOST_UID" | cut -d: -f6)" != "$USER_HOME" ]; then
        usermod -d "$USER_HOME" -m "$EXISTING_USER" 2>/dev/null || true
    fi
    USERNAME="$EXISTING_USER"
fi

mkdir -p "$USER_HOME"
chown "$HOST_UID:$HOST_GID" "$USER_HOME" /workspace

for dir in "$USER_HOME/.claude" "$USER_HOME/.codex" "$USER_HOME/.agents" "$USER_HOME/.cache" "$USER_HOME/.npm" "$USER_HOME/.config"; do
    if [ -d "$dir" ]; then
        chown "$HOST_UID:$HOST_GID" "$dir" 2>/dev/null || true
    fi
done

export HOME="$USER_HOME"
export USER="$USERNAME"

exec gosu "$HOST_UID:$HOST_GID" "$@"
