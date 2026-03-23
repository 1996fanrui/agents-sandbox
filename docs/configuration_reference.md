# Configuration Reference

This document is the source of truth for daemon and operator configuration in `agents-sandbox`.

If a change adds, removes, renames, or changes the default of a config key, update this document in the same change.

## Configuration Layering

`agents-sandbox` uses a `config.toml + env + secrets` layering model:

- `config.toml` stores the structured daemon and operator settings.
- Environment-specific injection may provide deployment-local values such as socket paths, mounted files, or secret file locations.
- Secrets stay outside the repository and outside generated documentation.

Northbound request fields are not part of this configuration surface. Request-time lifecycle inputs such as workspace materialization, dependency declarations, and tooling projection requests belong to the RPC contract, not to `config.toml`.

## Configuration File Locations

| Scenario | Path |
|----------|------|
| Linux standalone | `$XDG_CONFIG_HOME/agents-sandbox/config.toml`, with fallback to `~/.config/agents-sandbox/config.toml` |
| macOS standalone | `~/Library/Application Support/agents-sandbox/config.toml` |
| Compose deployment | `/etc/agents-sandbox/config.toml` mounted into the `agboxd` container |

## Configuration Keys

| Key | Type | Recommended Default | Override Scope | Purpose |
|-----|------|---------------------|----------------|---------|
| `server.socket_path` | string | Platform default for standalone; `/run/agbox/agboxd.sock` for compose | Daemon config only | Canonical Unix domain socket path for the daemon |
| `runtime.state_root` | string | Linux standalone: `$XDG_STATE_HOME/agents-sandbox` or `~/.local/state/agents-sandbox`; macOS standalone: `~/Library/Application Support/agents-sandbox/state`; compose: `/var/lib/agents-sandbox/state` | Daemon config only | Persistent root for runtime state, event journals, sequence counters, and shadow-copy materialization |
| `runtime.idle_ttl` | duration string | `"30m"` | Daemon config only | Idle stop threshold based on `last_terminal_run_finished_at` |
| `runtime.event_replay_window` | duration string | `"24h"` | Daemon config only | Retention window for event replay and reconnect recovery |
| `artifacts.exec_output_root` | string | `"/var/lib/agents-sandbox/artifacts/exec"` | Daemon config only | Root directory for exec output files |
| `artifacts.exec_output_template` | string template | `"{sandbox_id}/{exec_id}.jsonl"` | Daemon config only | Relative path template for exec output files |
| `artifacts.enforce_root_boundary` | boolean | `true` | Daemon config only | Reject path escapes outside `artifacts.exec_output_root` |
| `projections.cache_defaults.uv` | boolean | `true` | Daemon config only; request may disable | Default enablement for the `uv` cache projection |
| `projections.cache_defaults.npm` | boolean | `true` | Daemon config only; request may disable | Default enablement for the `npm` cache projection |
| `projections.cache_defaults.apt` | boolean | `true` | Daemon config only; request may disable | Default enablement for the `apt` cache projection |

## Request-Time Overrides

The northbound API may override only a narrow subset of behavior:

| Surface | Allowed Request Override | Notes |
|---------|--------------------------|-------|
| Workspace materialization | Yes | Each sandbox provides its own workspace source and materialization mode |
| Dependency list | Yes | Each sandbox declares its own dependency set |
| Cache projections | Yes, disable-only | Requests may turn off daemon-default caches, but may not introduce arbitrary new host paths |
| Tooling projections | Yes, explicit capability request | Requests may choose from the daemon's built-in capability set |
| Artifact output location | No | Output root and template stay daemon-owned |
| `runtime.idle_ttl` | No | Idle stop policy stays daemon-owned |
| Resource limits | No | V1 does not support request-scoped resource limits |

## Built-in Tooling Capabilities

These capabilities are runtime contracts exposed by the daemon. They are not arbitrary user-defined mount rules.

| Capability ID | Default Host Source | Default Container Target | Purpose |
|---------------|---------------------|--------------------------|---------|
| `.claude` | `~/.claude` | `/home/sandbox/.claude` | Claude configuration |
| `.codex` | `~/.codex` | `/home/sandbox/.codex` | Codex configuration |
| `.agents` | `~/.agents` | `/home/sandbox/.agents` | Agent configuration |
| `gh-auth` | `~/.config/gh` | `/home/sandbox/.config/gh` | GitHub CLI authentication |
| `ssh-agent` | `SSH_AUTH_SOCK` from the host environment | `/ssh-agent` | SSH agent forwarding |

## Artifact Output Safety Rules

The daemon owns exec output path construction and must fail fast on unsafe or invalid targets:

- Template expansion must produce a path under `artifacts.exec_output_root`.
- Parent directory resolution must reject `..` escapes.
- Path preparation must reject symlink and hardlink boundary escapes.
- Missing template fields, owner mismatches, or permission failures must surface explicit errors.
