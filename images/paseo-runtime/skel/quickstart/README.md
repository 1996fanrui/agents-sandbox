# Quickstart

This sandbox is the result of combining two projects:

- **agents-sandbox** — provides the isolated container, the `agbox` user, mounts, and the tool capabilities (`claude`, `codex`, `npm`, `uv`, `apt`).
- **paseo** — provides the in-browser UI. The paseo daemon runs as this container's main process and relays through `app.paseo.sh`.

You don't need to start anything manually. The daemon is already running on `0.0.0.0:6767` inside the container.

## Open the web UI

Run this on the host that manages your sandbox (not inside the container):

```
paseo-stack url <your-name>
```

Open the printed `https://app.paseo.sh/#offer=...` link in a browser. The relay bridges your browser to the in-container daemon — no host ports are published.

## Agent CLIs available inside

- `claude`  — Claude Code
- `codex`   — OpenAI Codex CLI
- `opencode`
- `paseo`

Example:

```
claude
codex
opencode
paseo --help
```

## Shell

Default shell is `zsh` with oh-my-zsh (`git`, `docker`, `zsh-autosuggestions`, `zsh-syntax-highlighting` plugins). Shell history is backed by `atuin` — press `Ctrl-R` for fuzzy search.

## Where things live

- `~/.paseo/`  — paseo daemon state (persisted via a host mount).
- `~/.claude/`, `~/.codex/`, `~/.agents/`, `~/.npm/`  — per-tool state, mounted from the host so it survives container restarts.
- `/workspace` — working directory, mounted from the host.

## Upgrading the agent CLIs

All four CLIs are baked into the image. To pick up a newer version without rebuilding the image:

```
npm install -g @anthropic-ai/claude-code
npm install -g @openai/codex
npm install -g opencode-ai
npm install -g @getpaseo/cli
```
