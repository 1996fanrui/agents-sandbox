#!/bin/bash
# Usage:
#   Build this image and run it through agents-sandbox or Docker with HOST_UID
#   and HOST_GID set.
#
# Required env vars:
#   HOST_UID
#   HOST_GID
#
# Creates a non-root agbox user with zsh as the default shell, grants
# passwordless sudo, and seeds oh-my-zsh into the user home when missing.
set -e

if [ -z "$HOST_UID" ] || [ -z "$HOST_GID" ]; then
    echo "ERROR: HOST_UID and HOST_GID must be set" >&2
    exit 1
fi

USERNAME="agbox"
USER_HOME="/home/agbox"
USER_SHELL="/bin/zsh"

if ! getent group "$HOST_GID" >/dev/null 2>&1; then
    groupadd -g "$HOST_GID" "$USERNAME"
fi

if ! getent passwd "$HOST_UID" >/dev/null 2>&1; then
    useradd -m -s "$USER_SHELL" -u "$HOST_UID" -g "$HOST_GID" -d "$USER_HOME" "$USERNAME"
else
    EXISTING_USER=$(getent passwd "$HOST_UID" | cut -d: -f1)
    if [ "$EXISTING_USER" != "$USERNAME" ]; then
        usermod -l "$USERNAME" "$EXISTING_USER" 2>/dev/null || true
        EXISTING_USER="$USERNAME"
    fi
    if [ "$(getent passwd "$HOST_UID" | cut -d: -f6)" != "$USER_HOME" ]; then
        usermod -d "$USER_HOME" -m "$EXISTING_USER" 2>/dev/null || true
    fi
    usermod -s "$USER_SHELL" "$EXISTING_USER" 2>/dev/null || true
    USERNAME="$EXISTING_USER"
fi

mkdir -p "$USER_HOME"
chown "$HOST_UID:$HOST_GID" "$USER_HOME" /workspace
echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/runtime-user
chmod 440 /etc/sudoers.d/runtime-user

# Seed oh-my-zsh for users whose home was created before this image (volume reuse).
if [ ! -d "$USER_HOME/.oh-my-zsh" ] && [ -d /etc/skel/.oh-my-zsh ]; then
    cp -r /etc/skel/.oh-my-zsh "$USER_HOME/.oh-my-zsh"
    chown -R "$HOST_UID:$HOST_GID" "$USER_HOME/.oh-my-zsh"
fi
if [ ! -f "$USER_HOME/.zshrc" ] && [ -f /etc/skel/.zshrc ]; then
    cp /etc/skel/.zshrc "$USER_HOME/.zshrc"
    chown "$HOST_UID:$HOST_GID" "$USER_HOME/.zshrc"
fi
if [ ! -d "$USER_HOME/quickstart" ] && [ -d /etc/skel/quickstart ]; then
    cp -r /etc/skel/quickstart "$USER_HOME/quickstart"
    chown -R "$HOST_UID:$HOST_GID" "$USER_HOME/quickstart"
fi

for dir in "$USER_HOME/.claude" "$USER_HOME/.codex" "$USER_HOME/.agents" "$USER_HOME/.cache" "$USER_HOME/.npm" "$USER_HOME/.config" "$USER_HOME/.local" "$USER_HOME/.local/share"; do
    if [ -d "$dir" ]; then
        chown "$HOST_UID:$HOST_GID" "$dir" 2>/dev/null || true
    fi
done

export HOME="$USER_HOME"
export USER="$USERNAME"
export SHELL="$USER_SHELL"

if [ -S "/ssh-agent" ]; then
    chmod 666 /ssh-agent
fi

exec gosu "$HOST_UID:$HOST_GID" "$@"
