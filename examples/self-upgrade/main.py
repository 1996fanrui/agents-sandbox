from __future__ import annotations

"""
Self-upgrade example: demonstrates how an agent triggers a live service upgrade
inside a running sandbox without any external orchestration.

Flow:
  1. Create sandbox — installs and runs http-server@14.0.0 as the main process
  2. Agent execs into the sandbox and confirms the running version
  3. Agent installs http-server@14.1.1 and kills the main process
  4. Container restarts automatically (unless-stopped policy)
  5. Agent execs again and confirms the new version is now running
"""

import asyncio
from pathlib import Path

from agents_sandbox import AgentsSandboxClient

SANDBOX_YAML = Path(__file__).parent / "sandbox.yaml"
VERSION_CMD = ["sh", "-c", "http-server --version"]
UPGRADE_CMD = ["sh", "-c", "sudo npm install -g http-server@14.1.1 --silent && kill $(pgrep -f 'node.*http-server')"]


async def read_output(result) -> str:
    return Path(result.stdout_log_path).read_text().strip() if result.stdout_log_path else ""


async def main() -> None:
    async with AgentsSandboxClient() as client:
        # Step 1: create sandbox — main process installs and starts http-server@14.0.0
        sandbox = await client.create_sandbox(config_yaml=SANDBOX_YAML.read_text())
        sandbox_id = sandbox.sandbox_id
        print(f"Sandbox {sandbox_id} created, waiting for service to start ...")
        await asyncio.sleep(20)

        try:
            # Step 2: agent confirms the current version
            result = await client.create_exec(sandbox_id, VERSION_CMD, wait=True)
            print(f"[agent] version before upgrade: {await read_output(result)}")

            # Step 3: agent upgrades the package and kills the main process
            print("[agent] upgrading http-server 14.0.0 -> 14.1.1 and restarting ...")
            await client.create_exec(sandbox_id, UPGRADE_CMD, wait=True)

            # Step 4: wait for container to restart automatically
            print("[agent] waiting for container restart ...")
            await asyncio.sleep(15)

            # Step 5: agent confirms the new version is running
            result = await client.create_exec(sandbox_id, VERSION_CMD, wait=True)
            print(f"[agent] version after upgrade:  {await read_output(result)}")

        finally:
            await client.delete_sandbox(sandbox_id)
            print(f"Sandbox {sandbox_id} deleted.")


if __name__ == "__main__":
    asyncio.run(main())
