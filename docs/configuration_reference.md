# Configuration Reference

This document is the source of truth for daemon and operator configuration in `agents-sandbox`.

If a change adds, removes, renames, or changes the default of a config key, update this document in the same change.

## Configuration Layering

`agents-sandbox` uses a `config.toml + env + secrets` layering model:

- `config.toml` stores the structured daemon and operator settings.
- Environment-specific injection may provide deployment-local values such as socket paths, mounted files, or secret file locations.
- Secrets stay outside the repository and outside generated documentation.

Northbound request fields are not part of this configuration surface. Request-time lifecycle inputs such as workspace materialization, dependency declarations, and tooling projection requests belong to the RPC contract, not to `config.toml`.

`agboxd` startup values resolve with this precedence:

1. CLI flags such as `--socket` and `--config`
2. Environment variables such as `AGBOX_SOCKET` and `AGBOX_CONFIG_FILE`
3. Auto-discovered `config.toml`
4. Built-in defaults

The singleton host lock is not an operator-tunable config key. `agboxd` derives it from the effective socket path:

- the host-managed deployment path `/run/agbox/agboxd.sock` uses the fixed host lock `/run/agbox/agboxd.lock`
- standalone or foreground paths outside `/run/agbox` co-locate the lock next to the chosen socket, for example `/tmp/dev/agboxd.sock` uses `/tmp/dev/agboxd.lock`
- socket-scoped locks are acceptable only for standalone or explicit foreground usage; they are not a supported way to run multiple host-global control planes against the same Docker daemon

## Configuration File Locations

| Scenario | Path |
|----------|------|
| Linux standalone | `$XDG_CONFIG_HOME/agents-sandbox/config.toml`, with fallback to `~/.config/agents-sandbox/config.toml` |
| macOS standalone | `~/Library/Application Support/agents-sandbox/config.toml` |
| Host-managed daemon deployment | `/etc/agents-sandbox/config.toml` managed by the host `systemd` service |

`agboxd` auto-loads the platform-default file only when no explicit `--config` flag or `AGBOX_CONFIG_FILE` override is provided.

## Configuration Keys

| Key | Type | Recommended Default | Override Scope | Purpose |
|-----|------|---------------------|----------------|---------|
| `server.socket_path` | string | `$XDG_RUNTIME_DIR/agbox/agboxd.sock` on Linux standalone, `~/Library/Application Support/agbox/run/agboxd.sock` on macOS standalone, `/run/agbox/agboxd.sock` for host-managed deployment | Daemon config only | Canonical Unix domain socket path for the daemon |
| `runtime.idle_ttl` | duration string | `"30m"` | Daemon config only | Idle stop threshold based on `last_terminal_run_finished_at` |
| `runtime.state_root` | string | unset | Daemon config only | Reserved location for runtime-owned journal, sequence, and shadow-copy state |
| `runtime.event_replay_window` | integer | `32` | Daemon config only | Number of replayable events retained per sandbox |
| `artifacts.exec_output_root` | string | unset | Daemon config only | Root directory where runtime-owned exec output files are created |
| `artifacts.exec_output_template` | string | `"{sandbox_id}/{exec_id}.jsonl"` | Daemon config only | Relative template expanded against `artifacts.exec_output_root`; supported fields are `sandbox_id` and `exec_id` |

## Request-Time Overrides

The northbound API may override only a narrow subset of behavior:

| Surface | Allowed Request Override | Notes |
|---------|--------------------------|-------|
| Workspace materialization | Yes | Each sandbox provides its own workspace source and materialization mode |
| Dependency list | Yes | Each sandbox declares its own dependency set |
| Cache projections | Yes | Requests select the daemon-defined cache capability set and may disable entries exposed by the daemon |
| Tooling projections | Yes, explicit capability request | Requests may choose from the daemon's built-in capability set |
| `runtime.idle_ttl` | No | Idle stop policy stays daemon-owned |
| Resource limits | No | V1 does not support request-scoped resource limits |

## Singleton Deployment Rule

`agboxd` is a single-writer control plane for one host runtime. Starting a second daemon against the same Docker daemon is unsafe because cleanup and lifecycle decisions are host-global.

Supported deployments must satisfy all of these constraints:

- The daemon acquires an exclusive non-blocking lock before it removes any socket path or starts gRPC serving.
- The daemon exits fail-fast if the lock is already held.
- A consumer that bind-mounts the host runtime directory must use the same directory for both `server.socket_path` and the derived host lock; per-stack private Docker volumes are not a safe singleton mechanism.

In practice, the host-managed safe default is:

- `server.socket_path = "/run/agbox/agboxd.sock"`
- derived host lock path: `/run/agbox/agboxd.lock`

With that layout, accidental duplicate compose stacks on the same host contend on the same lock file and the later daemon fails before it can mutate the shared socket or Docker-managed runtime state.
