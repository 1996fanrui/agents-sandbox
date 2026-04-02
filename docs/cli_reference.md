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
# Execute command in sandbox
agbox sandbox exec
# Launch interactive agent session
agbox agent
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
agbox sandbox exec <sandbox_id> [--cwd <path>] [--env-overrides key=value]... -- <command> [args...]
```

- `create`: `--image` is required. `--label` is repeatable. `--idle-ttl` accepts Go duration syntax (`5m`, `0` to disable).
- `delete`: positional ID and `--label` are mutually exclusive.
- `exec`: the `--` separator is required. `--env-overrides` is repeatable. Exit code of the executed command is propagated directly.

## Agent Command

Provides an out-of-the-box workflow: create a sandbox, copy the project directory, attach an interactive TTY session, and automatically clean up the sandbox on exit. Two mutually exclusive modes:

```bash
# Use a registered agent type (resolves command + default builtin tools)
agbox agent claude
# Registered agent type with custom workspace
agbox agent codex --workspace /path/to/project
# Custom command with explicit builtin tools (equivalent to registered types but fully customizable)
agbox agent --command "claude --dangerously-skip-permissions" --builtin-tool claude --builtin-tool git --builtin-tool uv --builtin-tool npm
# Any interactive CLI tool can be used as a custom command
agbox agent --command "my-coding-agent --auto" --builtin-tool git --builtin-tool uv
```

| Flag | Description |
|------|-------------|
| `--command <cmd>` | Custom command (mutually exclusive with agent type) |
| `--workspace <path>` | Directory to copy into sandbox as workspace (default: cwd) |
| `--builtin-tool <name>` | Builtin tool to install (repeatable; overrides defaults when specified) |

**Workspace safety checks:**

- `/` and `$HOME` are rejected as workspace paths (symlinks are resolved before comparison).
- When the workspace directory does not contain a top-level `.git` entry, an interactive confirmation prompt is displayed before proceeding.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Runtime error (daemon unreachable, operation failed) |
| `2` | Usage error (invalid flags, missing arguments, unknown commands) |
| `N` | `sandbox exec`: propagated exit code of the executed command |
| `125` | `sandbox exec`: exec failed to run or unexpected terminal state |
| `128+N` | `agent`: process killed by signal N |
