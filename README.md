# Agents Sandbox

Unleash the full power of your AI agents.

- **Unrestricted execution** — Agents install anything, run anything, break anything. Your host stays untouched.
- **No more babysitting** — Zero permission prompts, zero manual approvals. Agents run autonomously and deliver results directly.
- **Secure by default** — Every sandbox is fully isolated. No host network. No host filesystem. No exceptions.
- **Host credentials, zero setup** — Sandboxes inherit host authentication (SSH agent, GitHub CLI, etc.) out of the box. Claude Code and Codex work immediately — powered by your flat-rate CLI subscriptions, not per-token API billing.
- **Local-first, cloud-optional** — Zero latency, zero cost, and your data never leaves your machine.

## Why agents-sandbox?

Today's AI agents face an impossible dilemma on the host machine:

- **Grant full permissions?** The agent runs fast — but a single bad command can delete your files, leak credentials, or compromise the entire machine. One prompt injection away from disaster.
- **Keep permissions locked down?** Every `npm install`, every file write, every shell command triggers a manual confirmation. The agent spends more time waiting for "yes" than actually working. You end up babysitting the agent instead of letting it work for you.

Either way, **agents on bare hosts can never reach their full potential.** You are forced to choose between speed and safety — and both options lose.

A sandbox eliminates this tradeoff entirely. The agent gets an isolated container where it has **full, unrestricted permissions** — install anything, run anything, delete anything — while your host sees none of it. No compromise. Full speed. Full safety.

## How agents-sandbox solves this

The first-class principle is: **the sandbox is fully isolated from the host by default. No host network access. No host filesystem access. No exceptions.** A sandbox running on your laptop is as isolated as a remote cloud server — except with zero latency, zero cost, and full data privacy.

| Security Layer | Mechanism |
|----------------|-----------|
| **Host network blocked — permanently** | Each sandbox gets its own isolated Docker network. Cannot reach `localhost`, host services, or the local network. **Will never be supported.** |
| **Internet fully available** | Agents can freely download packages, call APIs, clone repos, and interact with the outside world. |
| **Host filesystem invisible** | Zero access to host files by default. Only explicitly declared mounts or copies are allowed; the daemon rejects anything unsafe. |
| **Minimal credential injection** | Only a small set of daemon-defined credential shortcuts (e.g., `ssh-agent`, `gh-auth`) can enter. Fixed rules, not arbitrary host path passthrough. |
| **Deterministic cleanup** | All runtime resources (containers, networks, filesystem state) are fully removed on delete. No orphans, no leaks. |

## Use Cases

Any AI agent that needs to **take actions** — not just generate text — benefits from a sandbox.

| Scenario | What agents can do in the sandbox |
|----------|----------------------------------|
| **AI coding agents** (Claude Code, Codex) | Freely install, build, test, and iterate at full speed |
| **General-purpose task agents** (OpenClaw) | Execute arbitrary multi-step workflows — browse, download, run scripts |
| **Data analysis agents** | Process untrusted datasets and run user-provided code in complete isolation |
| **DevOps / SRE agents** | Run deployment scripts and CLI tools in disposable, contained environments |
| **Research agents** | Install anything, run any experiment, discard when done |
| **CI / test agents** | Each run gets a clean, reproducible, fully isolated environment |

## Why Local, Not a Remote VPS?

A remote VPS gives you isolation too — but with tradeoffs. agents-sandbox runs locally, giving you the same isolation with none of the downsides:

| | Local Sandbox | Remote VPS |
|---|---|---|
| **Latency** | Near-zero | 10–100ms+ per command round-trip |
| **Cost** | Free — you own the hardware. Agents use flat-rate CLI subscriptions (Claude Code, Codex), not per-token API billing | Pay per hour/VM/GB, plus API metering for every token |
| **Data privacy** | Code and credentials never leave your machine | Source code and API keys travel to a third party |
| **Startup** | Seconds | 30s–minutes for VM provisioning |
| **Local resources** | Direct access to files, GPU via controlled mounts | Must sync files up/down |

**Local-first, cloud-optional.** The same daemon and SDK work in cloud deployments when you need to scale.

## Quickstart

The official recommended runtime image for coding workloads is:

- quickstart alias: `ghcr.io/agents-sandbox/coding-runtime:latest`
- release tag form: `ghcr.io/agents-sandbox/coding-runtime:<release-version>`

This image is only a recommended example value. It is not a daemon default, and every sandbox
request must still pass `image` explicitly.

Python SDK example:

On Linux, `AgentsSandboxClient()` resolves the daemon socket from the fixed
`$XDG_RUNTIME_DIR/agbox/agboxd.sock` path. Run the example from a session where
`XDG_RUNTIME_DIR` is set and the daemon uses the same runtime directory.

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
