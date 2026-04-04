# Quick Start

Prerequisites: Docker and curl.

## Install

```bash
curl -fsSL https://agents-sandbox.com/install.sh | bash
```

This installs `agboxd` (daemon) and `agbox` (CLI), then starts the daemon as a user service.

## Run an AI Agent in a Sandbox

```bash
# Claude Code — full permissions, fully isolated
agbox agent claude

# Codex
agbox agent codex

# Any custom command
agbox agent --command "aider --yes" --builtin-tool git --builtin-tool uv
```

The agent gets unrestricted permissions inside the sandbox. Your host stays untouched. On exit, the sandbox is automatically deleted.

## CLI Workflow

```bash
# Create a sandbox
agbox sandbox create --image ghcr.io/agents-sandbox/coding-runtime:latest --label project=demo

# Run a command
agbox exec run <sandbox_id> -- python -c "print('hello')"

# Inspect
agbox sandbox list
agbox sandbox get <sandbox_id>

# Clean up
agbox sandbox delete <sandbox_id>
```

See [CLI Reference](cli_reference.md) for all commands and flags.

## SDK Workflow

```python
import asyncio
from agents_sandbox import AgentsSandboxClient

async def main() -> None:
    async with AgentsSandboxClient() as client:
        sandbox = await client.create_sandbox(
            image="ghcr.io/agents-sandbox/coding-runtime:latest",
        )
        try:
            result = await client.run(
                sandbox.sandbox_id,
                ("python", "-c", "print('hello from sandbox')"),
            )
            print(result.stdout_log_path)
        finally:
            await client.delete_sandbox(sandbox.sandbox_id)

asyncio.run(main())
```

See [Python SDK](sdk_python_usage.md) and [Go SDK](sdk_go_usage.md) for full SDK documentation.

## What's Next

- [Declarative YAML Config](declarative_yaml_config.md) — define sandbox environments as YAML
- [Companion Container Guide](companion_container_guide.md) — add databases, caches, and other companion containers to your sandbox
- [Configuration Reference](configuration_reference.md) — daemon configuration keys
