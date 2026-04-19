# CLI Reference

The AgentsSandbox CLI (`agbox`) communicates with the daemon via gRPC over a Unix socket. Built on [Cobra](https://github.com/spf13/cobra) — every command supports `--help` / `-h`.

## Quick Reference

```bash
# Print version and help hint
agbox
# Print agbox and agboxd versions
agbox version
# Create a sandbox (see flags below)
agbox sandbox create
# List sandboxes
agbox sandbox list
# Get sandbox details
agbox sandbox get
# Delete sandbox(es)
agbox sandbox delete
# Stop a sandbox
agbox sandbox stop
# Resume a stopped sandbox
agbox sandbox resume
# Execute command in sandbox
agbox exec run
# Get exec status
agbox exec get
# Cancel a running exec
agbox exec cancel
# List active execs
agbox exec list
# Launch a pre-registered agent session (top-level command per type)
agbox claude
agbox codex
agbox openclaw
agbox paseo
# Launch a custom agent via --command (only use-case for `agbox agent`)
agbox agent --command "<binary> [args...]"
# Generate shell autocompletion script (bash, zsh, fish, powershell)
agbox completion
# Print usage for any command
agbox sandbox create --help
```

## Sandbox Commands

```bash
agbox sandbox create --image <image> [--label key=value]... [--idle-ttl <duration>] [--json]
agbox sandbox list [--include-deleted] [--label key=value]... [--json]
agbox sandbox get <sandbox_id> [--json]
# Delete by ID
agbox sandbox delete <sandbox_id>
# Delete by label
agbox sandbox delete --label key=value
agbox sandbox stop <sandbox_id>
agbox sandbox resume <sandbox_id>
```

- `create`: `--image` is required. `--label` is repeatable. `--idle-ttl` accepts Go duration syntax (`5m`, `0` to disable).
- `delete`: positional ID and `--label` are mutually exclusive.
- `stop`: stops a running sandbox. Waits until the sandbox reaches the stopped state.
- `resume`: resumes a stopped sandbox. Waits until the sandbox reaches the ready state.

## Exec Commands

```bash
agbox exec run <sandbox_id> [--cwd <path>] [--env-overrides key=value]... -- <command> [args...]
agbox exec get <exec_id> [--json]
agbox exec cancel <exec_id>
agbox exec list [sandbox_id] [--json]
```

- `run`: the `--` separator is required. `--env-overrides` is repeatable. Exit code of the executed command is propagated directly.
- `get`: shows exec status in text or JSON format.
- `cancel`: cancels a running exec. Waits until the exec reaches a terminal state.
- `list`: lists active execs. Optionally filter by sandbox ID.

## Agent Commands

Provide an out-of-the-box workflow: create a sandbox, optionally copy the project directory, run an agent, and manage the sandbox lifecycle.

The CLI exposes one top-level command per registered agent type. Use them directly — there is no `agbox agent <type>` form. The generic `agbox agent` entry point is reserved for the custom-command mode (`--command`).

```bash
# Pre-registered agents (top-level commands)
agbox claude
agbox codex
agbox openclaw                                         # deploy OpenClaw gateway (long-running)
agbox paseo                                            # deploy Paseo daemon (long-running)

# Custom workspace
agbox codex --workspace /path/to/project
# Long-running mode
agbox claude --mode long-running
# Resource limits and environment variables
agbox claude --cpu-limit 2 --memory-limit 4g --disk-limit 10g --env MY_API_KEY=secret
# Override sandbox ID
agbox codex --sandbox-id my-custom-sandbox

# Custom agent via `agbox agent --command` (the ONLY supported form of `agbox agent`)
agbox agent --command "claude --dangerously-skip-permissions" --builtin-tool claude --builtin-tool git --builtin-tool uv --builtin-tool npm
# Long-running custom command (stdout emits sandbox_id for scripting)
SB_ID=$(agbox agent --command "my-agent" --mode long-running --workspace /path/to/project 2>/dev/null)
```

### Session Modes

The agent command supports two session modes, controlled by `--mode`. The mode determines all runtime behavior — users select a mode and the runtime strategy follows implicitly:

| Strategy | Interactive (default) | Long-running |
|----------|----------------------|-------------|
| Execution method | `docker exec -it` (TTY attached, real-time output) | Container primary command (under tini); CLI waits for READY and detaches |
| Wait behavior | Blocks until `docker exec` subprocess exits | Blocks until sandbox reaches READY state |
| Ctrl+C behavior | Signal forwarded to container process → wait for exit → delete sandbox | CLI detaches, sandbox keeps running |
| Output display | Real-time stdout/stderr streamed via TTY | No streaming; prints readyMessage (if defined) and sandbox ID |
| Sandbox cleanup on exit | Always deleted | Cleaned up only on pre-READY failure; sandbox persists after READY |
| idle_ttl | 10d (safety net) | 0 (disabled) |

### Agent Type Capabilities

Agent types declare their own capabilities, orthogonal to session mode. Each capability is controlled by exactly one dimension (mode or agent type), never both:

| Capability | Description | claude | codex | openclaw | paseo | Custom `--command` | User override flag |
|-----------|-------------|--------|-------|----------|-------|-------------------|-------------------|
| mode | Default session mode | interactive | interactive | long-running | long-running | interactive | `--mode` |
| command | Container primary command (under tini in long-running; docker exec in interactive) | Fixed | Fixed | Fixed (gateway run) | Fixed (daemon start) | User-specified | `--command` |
| builtinTools | Pre-installed tools | Fixed | Fixed | Fixed | Fixed (filtered by preFlight) | User-specified | `--builtin-tool` |
| workspace copy | Copy local directory to /workspace | Yes (default: cwd) | Yes (default: cwd) | No | No | No | `--workspace` (explicit to enable) |
| .git check | Confirm when workspace lacks .git | Yes | Yes | No | No | No | None (automatic) |
| envs | Environment variables for container | None | None | None | None | None | `--env` (repeatable, `KEY=VAL` form) |
| cpuLimit | CPU limit | None | None | None | None | None | `--cpu-limit` (Docker `--cpus` format) |
| memoryLimit | Memory limit | None | None | None | None | None | `--memory-limit` (Docker `--memory` format) |
| diskLimit | Disk limit | None | None | None | None | None | `--disk-limit` (Docker `--storage-opt size=` format) |
| sandboxIDGen | Custom ID generator | No | No | openclaw-XXXXXX | paseo-XXXXXX | No | `--sandbox-id` |
| configYaml | Embedded sandbox config | No | No | Yes (image, command, mounts, ports, envs) | Yes (image, command, envs) | No | None |
| preFlight | Pre-flight validation | No | No | Auth check | Builtin tool host-path filter | No | None |
| readyMessage | Custom ready output | No | No | Management commands | Management commands + active tools | No | None |

- `--workspace` is optional at the top level.
  - claude/codex declare workspace copy and default to cwd.
  - openclaw does not copy workspace (it uses host mounts instead).
  - Custom `--command` does not copy by default; passing `--workspace` explicitly enables it.
  - `/` and `$HOME` are rejected as workspace paths (symlinks are resolved before comparison).
- `.git` check is declared per agent type (claude/codex enable it; openclaw and custom `--command` do not). When enabled, it triggers if the workspace directory lacks a `.git` entry.
- openclaw auto-generates sandbox IDs matching `openclaw-XXXXXX` (6 hex chars); paseo auto-generates `paseo-XXXXXX`; other types let the daemon generate IDs. `--sandbox-id` overrides any generator; empty or omitted values fall through to the generator or daemon auto-generation.
- `--env` passes environment variables to `CreateSpec.Envs`. Multiple `--env` flags are merged; duplicate keys use the last value. The daemon performs key-level merge with `configYaml` envs.
- `--cpu-limit`, `--memory-limit`, and `--disk-limit` pass resource limits directly to `CreateSpec` fields. Values are not validated by the CLI; invalid formats are rejected by the daemon or Docker.

### Command Surface

Each registered agent type has its own dedicated top-level command: `agbox claude`, `agbox codex`, `agbox openclaw`, `agbox paseo`. They do not accept positional arguments — the agent type is implicit in the command name. All of them reuse the same underlying session flags (`--mode`, `--workspace`, `--builtin-tool`, `--command`, `--env`, `--cpu-limit`, `--memory-limit`, `--disk-limit`, `--sandbox-id`).

`--command` can be used with registered agent types to override the default command. In interactive mode, it replaces the TTY command launched via `docker exec`. In long-running mode, it replaces the container primary command (under tini). The value is split by whitespace via `strings.Fields` (no shell quoting).

`agbox agent` is reserved exclusively for the custom-command mode: you must pass `--command` and it does not accept a positional agent type. The old `agbox agent <type>` form has been removed — use the per-type top-level command instead.

### Paseo Subcommands

The `agbox paseo` command additionally exposes subcommands:

```bash
# Print the paseo pairing URL (runs `paseo daemon pair` inside the sandbox)
agbox paseo url <sandbox_id>
```

- `url`: creates an exec running `/usr/local/bin/paseo daemon pair` inside the sandbox, waits for it to finish, and prints the stdout (pairing URL). The same URL is also embedded in the ready message printed by `agbox paseo` on successful startup; this subcommand is used to re-fetch it later.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Runtime error (daemon unreachable, operation failed) |
| `2` | Usage error (invalid flags, missing arguments, unknown commands) |
| `N` | `exec run`: propagated exit code of the executed command |
| `125` | `exec run`: exec failed to run or unexpected terminal state |
| `128+N` | `agent`: process killed by signal N |
