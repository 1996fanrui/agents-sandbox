#!/bin/bash
# Usage:
#   Build this image and run it through agents-sandbox or Docker with HOST_UID
#   and HOST_GID set.
#
# Required env vars:
#   HOST_UID
#   HOST_GID
#
# Optional env vars:
#   AGENTS_SANDBOX_SUPPLEMENTAL_GROUPS comma-separated numeric group IDs to
#   register on the runtime user.
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

if [ "$HOST_UID" = "0" ]; then
    # Host is root: add an agbox alias for uid=0 so "docker exec --user agbox" works.
    if ! getent passwd agbox >/dev/null 2>&1; then
        echo "agbox:x:0:0:agbox:${USER_HOME}:/bin/bash" >> /etc/passwd
    fi
else
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
fi

if [ "$HOST_UID" != "0" ] && [ -n "${AGENTS_SANDBOX_SUPPLEMENTAL_GROUPS:-}" ]; then
    IFS=',' read -ra SUPPLEMENTAL_GROUP_IDS <<< "$AGENTS_SANDBOX_SUPPLEMENTAL_GROUPS"
    for GROUP_ID in "${SUPPLEMENTAL_GROUP_IDS[@]}"; do
        if [ -z "$GROUP_ID" ]; then
            continue
        fi
        if ! [[ "$GROUP_ID" =~ ^[0-9]+$ ]]; then
            echo "ERROR: AGENTS_SANDBOX_SUPPLEMENTAL_GROUPS contains non-numeric group ID: $GROUP_ID" >&2
            exit 1
        fi
        GROUP_NAME=$(getent group "$GROUP_ID" | cut -d: -f1 || true)
        if [ -z "$GROUP_NAME" ]; then
            GROUP_NAME="agbox-supplemental-${GROUP_ID}"
            groupadd -g "$GROUP_ID" "$GROUP_NAME"
        fi
        usermod -aG "$GROUP_NAME" "$USERNAME"
    done
fi

mkdir -p "$USER_HOME"
chown "$HOST_UID:$HOST_GID" "$USER_HOME" /workspace
# Grant passwordless sudo so the runtime user can install system packages.
echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/runtime-user
chmod 440 /etc/sudoers.d/runtime-user

for dir in "$USER_HOME/.claude" "$USER_HOME/.codex" "$USER_HOME/.agents" "$USER_HOME/.cache" "$USER_HOME/.npm" "$USER_HOME/.config"; do
    if [ -d "$dir" ]; then
        chown "$HOST_UID:$HOST_GID" "$dir" 2>/dev/null || true
    fi
done

export HOME="$USER_HOME"
export USER="$USERNAME"

# Docker Desktop's magic SSH agent socket is owned by root:root (srw-rw----).
# Make it accessible to the non-root runtime user.
if [ -S "/ssh-agent" ]; then
    chmod 666 /ssh-agent
fi

exec gosu "$HOST_UID:$HOST_GID" "$@"
