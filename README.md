# agents-sandbox

`agents-sandbox` is an independent sandbox foundation repository.

## Quickstart

The official recommended runtime image for coding workloads is:

- quickstart alias: `ghcr.io/agents-sandbox/coding-runtime:latest`
- release tag form: `ghcr.io/agents-sandbox/coding-runtime:<release-version>`

This image is only a recommended example value. It is not a daemon default, and every sandbox
request must still pass `image` explicitly.

Python SDK example:

```python
import asyncio

from agents_sandbox import AgentsSandboxClient

async def main() -> None:
    async with AgentsSandboxClient() as client:
        sandbox = await client.create_sandbox(
            image="ghcr.io/agents-sandbox/coding-runtime:latest",
            sandbox_owner="demo",
        )
        try:
            result = await client.run(
                sandbox.sandbox_id,
                ("python", "-c", "print('hello from sandbox')"),
            )
            print(result.stdout.strip())
        finally:
            await client.delete_sandbox(sandbox.sandbox_id)


asyncio.run(main())
```

For a full one-command example that installs and runs Codex inside the sandbox, use:

```bash
uv run python examples/codex-cli/main.py
```

## Repository Layout

- Go entrypoints live under `cmd/`
- Runtime implementation is organized under `internal/`
- Protocol source files live under `api/proto/`
- The Python SDK lives under `sdk/python/`
- The minimal base runtime image asset lives under `images/base-runtime/`
- The home-aligned coding runtime image asset lives under `images/coding-runtime/`
- Examples live under `examples/`
