from __future__ import annotations

import asyncio

from agents_sandbox import AgentsSandboxClient

IMAGE = "ghcr.io/agents-sandbox/coding-runtime:latest"
PROMPT = "Compute 1+1 and return a JSON object with keys result and reasoning."


async def main() -> None:
    async with AgentsSandboxClient() as client:
        sandbox = await client.create_sandbox(
            image=IMAGE,
            builtin_resources=(".codex",),
        )
        try:
            await client.run(sandbox.sandbox_id, ("npm", "install", "-g", "@openai/codex@latest"))
            result = await client.run(
                sandbox.sandbox_id,
                ("codex", "exec", "--skip-git-repo-check", PROMPT),
            )
            print(result.stdout.strip())
        finally:
            await client.delete_sandbox(sandbox.sandbox_id)


if __name__ == "__main__":
    asyncio.run(main())
