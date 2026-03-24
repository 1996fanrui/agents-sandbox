# Codex CLI Example

This example shows the shortest supported path for running Codex inside AgentsSandbox.

Prerequisites:

- the daemon is already running
- `OPENAI_API_KEY` is set

The script uses the official recommended runtime image `ghcr.io/agents-sandbox/coding-runtime:latest`.
That image is a quickstart example value only. It is not a daemon default, and every sandbox request
must still pass `image` explicitly.

Run:

```bash
uv run python examples/codex-cli/main.py
```

What the script does:

1. validates `OPENAI_API_KEY` before it creates a sandbox
2. creates a sandbox with `AgentsSandboxClient()`
3. installs `@openai/codex@latest` inside the sandbox
4. runs `codex exec` with `client.run(...)`
5. prints the command result from `ExecHandle.stdout`
6. deletes the sandbox

Expected stdout shape:

```json
{"reasoning":"One plus one equals two.","result":2}
```
