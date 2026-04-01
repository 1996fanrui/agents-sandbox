# Agents Sandbox

**Full power for your agents. Full safety for your machine.**

## What matters most when running AI agents locally?

**Your machine must stay safe** — No host filesystem access. No host network access. No exceptions. Bad commands destroy only the sandbox.
**The agent must run free** — Install anything, run anything, break anything. Zero approval prompts. Deliver results directly.
**Reuse your CLI subscriptions** — Host authentication and flat-rate subscriptions (Claude Max, Codex) carry into the sandbox. Zero extra cost.
**One command to start sandbox** — `agbox agent claude` or `agbox agent codex` with Full Permissions.
**Data never leaves your machine** — Code, credentials, all agent activity stay local.
**No dedicated Mac Mini needed** — Your laptop already has Docker. That's all it takes.

## Why agents-sandbox?

Today's AI agents face an impossible dilemma on the host machine:

- **Grant full permissions?** The agent runs fast — but a single bad command can delete your files, leak credentials, or compromise the entire machine. One prompt injection away from disaster.
- **Keep permissions locked down?** Every `npm install`, every file write, every shell command triggers a manual confirmation. The agent spends more time waiting for "yes" than actually working. You end up babysitting the agent instead of letting it work for you.

This is not a hypothetical problem — it is exactly how today's agent CLIs work:

| | Restricted | Unrestricted |
|---|---|---|
| **Codex** | `workspace-write` — constant approvals, `.git` read-only | `danger-full-access` — host fully exposed |
| **Claude Code** | `default` — approval prompts on every tool call | `--dangerously-skip-permissions` — all checks bypassed |

**Every agent CLI ships these two modes, and neither works.**

Both modes shift the responsibility to **you**. Open permissions? You bear the risk. Lock them down? You become a full-time babysitter — approving every action, reviewing every command, essentially doing QA for the agent. Either way, **agents on bare hosts can never reach their full potential.** The platform should guarantee safety, not make the user choose between risk and manual labor.

A sandbox eliminates this tradeoff entirely. The agent gets an isolated container where it has **full, unrestricted permissions** — install anything, run anything, delete anything — while your host sees none of it. No compromise. Full speed. Full safety. See [Why Not Built-in Agent Sandboxes?](docs/why_not_builtin_sandboxes.md) for a detailed comparison.

## How agents-sandbox solves this

The first-class principle is: **the sandbox is fully isolated from the host by default. No host network access. No host filesystem access. No exceptions.** A sandbox running on your laptop is as isolated as a remote cloud server — except with zero latency, zero cost, and full data privacy.

| Security Layer | Mechanism |
|----------------|-----------|
| **Host network blocked — permanently** | Each sandbox gets its own isolated Docker network. Cannot reach `localhost`, host services, or the local network. **Will never be supported.** |
| **Internet fully available** | Agents can freely download packages, call APIs, clone repos, and interact with the outside world. |
| **Host filesystem invisible** | Zero access to host files by default. Only explicitly declared mounts or copies are allowed; the daemon rejects anything unsafe. |
| **Minimal credential injection** | Only a small set of daemon-defined credential shortcuts (e.g., `ssh-agent`, `gh-auth`) can enter. Fixed rules, not arbitrary host path passthrough. |
| **Deterministic cleanup** | All runtime resources (containers, networks, filesystem state) are fully removed on delete. No orphans, no leaks. |

See [Isolation and Security](docs/isolation_and_security.md) for the full security posture reference.

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

## Installation

**One-line install** (Linux and macOS — requires Docker and curl):

```bash
curl -fsSL https://agents-sandbox.com/install.sh | bash
```

This downloads the latest stable release, installs `agboxd` and `agbox` to a directory in your `PATH`, and starts the daemon as a user service (systemd on Linux, launchd on macOS).

To install a specific version or include pre-releases:

```bash
curl -fsSL https://agents-sandbox.com/install.sh | bash -s -- v0.1.1   # specific version
curl -fsSL https://agents-sandbox.com/install.sh | bash -s -- --pre     # latest including pre-releases
```

## Quickstart

Install and start Claude Code in a sandbox with two commands:

```bash
# Install agents-sandbox (daemon starts automatically)
curl -fsSL https://agents-sandbox.com/install.sh | bash

# Run interactive Claude Code in an isolated sandbox with full permissions.
# Equivalent to running: claude --dangerously-skip-permissions
agbox agent claude

# Run interactive Codex in an isolated sandbox with full permissions.
# Equivalent to running: codex --dangerously-bypass-approvals-and-sandbox
agbox agent codex
```

That's it. The agent has full unrestricted permissions inside the sandbox while your host stays completely untouched. See [CLI Reference](docs/cli_reference.md) for all available commands and options.

### Programmatic Access (Python SDK)

For programmatic control, use the Python SDK:

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

For full examples, see `examples/`.

## Learn More

- Agent needs databases, caches, or other dependencies for debugging? See [Companion Container Guide](docs/companion_container_guide.md)
- Want reusable sandbox environments as YAML? See [Declarative YAML Config](docs/declarative_yaml_config.md)
- Tuning daemon behavior (idle TTL, cleanup, log level)? See [Configuration Reference](docs/configuration_reference.md)

## Repository Layout

- Go entrypoints live under `cmd/`
- Runtime implementation is organized under `internal/`
- Protocol source files live under `api/proto/`
- The Python SDK lives under `sdk/python/`
- The minimal base runtime image asset lives under `images/base-runtime/`
- The home-aligned coding runtime image asset lives under `images/coding-runtime/`
- Examples live under `examples/`
