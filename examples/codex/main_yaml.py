from __future__ import annotations

import asyncio
from pathlib import Path

from agents_sandbox import AgentsSandboxClient

PROMPT = "Compute 1+1 and return a JSON object with keys result and reasoning."
YAML_PATH = Path(__file__).parent / "sandbox.yaml"


async def main() -> None:
    async with AgentsSandboxClient() as client:
        sandbox = await client.create_sandbox(
            config_yaml=YAML_PATH.read_text(),
        )
        try:
            result = await client.run(
                sandbox.sandbox_id,
                ("codex", "exec", "--skip-git-repo-check", PROMPT),
            )
            if result.stdout_log_path:
                print(Path(result.stdout_log_path).read_text().strip())
        finally:
            await client.delete_sandbox(sandbox.sandbox_id)


if __name__ == "__main__":
    asyncio.run(main())
