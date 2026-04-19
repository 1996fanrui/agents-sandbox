#!/bin/bash
# Openclaw-runtime entrypoint: delegates to coding-runtime entrypoint for
# user setup (HOST_UID/HOST_GID/HOME + gosu), then performs idempotent
# openclaw initialization as the container user.
set -eu

# Phase 1 (root): delegate to base entrypoint which creates the container user
# and re-execs this script via gosu. Use a marker env var to detect re-entry.
if [ -z "${_OPENCLAW_INIT_DONE:-}" ]; then
    export _OPENCLAW_INIT_DONE=1
    exec /usr/local/bin/entrypoint.sh "$0" "$@"
fi

# Phase 2 (container user): idempotent config initialization.
if [ ! -f "$HOME/.openclaw/config/openclaw.json" ]; then
    openclaw onboard --mode local --non-interactive --accept-risk \
        --gateway-auth token --gateway-bind lan --gateway-port 18789 \
        --no-install-daemon --skip-channels --skip-skills --skip-health --skip-search --skip-ui \
        --auth-choice skip
    openclaw config set gateway.controlUi.dangerouslyDisableDeviceAuth true --strict-json
fi

openclaw config set agents.defaults.elevatedDefault full

exec "$@"
