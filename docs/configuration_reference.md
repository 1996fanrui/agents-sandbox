# Configuration Reference

This document is the source of truth for daemon and operator configuration in `agents-sandbox`.

If a change adds, removes, renames, or changes the default of a config key, update this document in the same change.

## Configuration Layering

`agents-sandbox` uses a `config.toml + env + secrets` layering model:

- `config.toml` stores the structured daemon and operator settings.
- Environment-specific injection may provide deployment-local values such as socket paths, mounted files, or secret file locations.
- Secrets stay outside the repository and outside generated documentation.

Northbound request fields are not part of this configuration surface. Request-time lifecycle inputs such as image selection, filesystem ingress (`mounts`, `copies`, `builtin_resources`), and dependency declarations belong to the RPC contract, not to `config.toml`.

The AgentsSandbox daemon startup values resolve with this precedence:

1. CLI flags such as `--socket` and `--config`
2. Environment variables such as `AGBOX_SOCKET` and `AGBOX_CONFIG_FILE`
3. Auto-discovered `config.toml`
4. Built-in defaults

The singleton host lock is not an operator-tunable config key. The AgentsSandbox daemon derives it from the effective socket path:

- the host-managed deployment path `/run/agbox/agboxd.sock` uses the fixed host lock `/run/agbox/agboxd.lock`
- standalone or foreground paths outside `/run/agbox` co-locate the lock next to the chosen socket, for example `/tmp/dev/agboxd.sock` uses `/tmp/dev/agboxd.lock`
- socket-scoped locks are acceptable only for standalone or explicit foreground usage; they are not a supported way to run multiple host-global control planes against the same Docker daemon

## Configuration File Locations

| Scenario | Path |
|----------|------|
| Linux standalone | `$XDG_CONFIG_HOME/agents-sandbox/config.toml`, or `~/.config/agents-sandbox/config.toml` when `XDG_CONFIG_HOME` is unset |
| macOS standalone | `~/Library/Application Support/agents-sandbox/config.toml` |
| Host-managed daemon deployment | `/etc/agents-sandbox/config.toml` managed by the host `systemd` service |

The AgentsSandbox daemon auto-loads the platform-default file only when no explicit `--config` flag or `AGBOX_CONFIG_FILE` override is provided.

## Configuration Keys

| Key | Type | Recommended Default | Override Scope | Purpose |
|-----|------|---------------------|----------------|---------|
| `server.socket_path` | string | `$XDG_RUNTIME_DIR/agbox/agboxd.sock` on Linux standalone, `~/Library/Application Support/agbox/run/agboxd.sock` on macOS standalone, `/run/agbox/agboxd.sock` for host-managed deployment | Daemon config only | Canonical Unix domain socket path for the daemon |
| `runtime.idle_ttl` | duration string | `"30m"` | Daemon config only | Idle stop threshold based on `last_terminal_run_finished_at` |
| `runtime.state_root` | string | unset | Daemon config only | Root for durable-copy workspace materialization, generic copy inputs, and shadow-copy projection state |
| `artifacts.exec_output_root` | string | unset | Daemon config only | Root directory where runtime-owned exec output files are created |
| `artifacts.exec_output_template` | string | `"{sandbox_id}/{exec_id}.jsonl"` | Daemon config only | Relative template expanded against `artifacts.exec_output_root`; supported fields are `sandbox_id` and `exec_id` |

## Request-Time Overrides

The northbound API may override only a narrow subset of behavior:

| Surface | Allowed Request Override | Notes |
|---------|--------------------------|-------|
| Primary sandbox image | Yes | Every sandbox request must provide its own runtime image; this is not a daemon config default |
| Generic mounts | Yes | Each sandbox may bind explicit host paths to explicit container targets |
| Generic copies | Yes | Each sandbox may copy explicit host files or trees into explicit container targets |
| Built-in resources | Yes | Each sandbox may request daemon-defined resource shortcuts such as `.claude`, `.codex`, `uv`, or `ssh-agent` |
| Dependency list | Yes | Each sandbox declares its own dependency set |
| `runtime.idle_ttl` | No | Idle stop policy stays daemon-owned |
| Resource limits | No | V1 does not support request-scoped resource limits |

Legacy `workspace`, `cache_projections`, and `tooling_projections` fields still exist in the protocol for internal and compatibility paths, but they are not the preferred public SDK surface.

Replay retention is not an operator-tunable config key in V1. The current daemon keeps per-sandbox event history in memory for the daemon process lifetime, which is enough for `from_cursor="0"` replay while the daemon remains alive.

## Singleton Deployment Rule

The AgentsSandbox daemon is a single-writer control plane for one host runtime. Starting a second daemon against the same Docker daemon is unsafe because cleanup and lifecycle decisions are host-global.

Supported deployments must satisfy all of these constraints:

- The daemon acquires an exclusive non-blocking lock before it removes any socket path or starts gRPC serving.
- The daemon exits fail-fast if the lock is already held.
- A consumer that bind-mounts the host runtime directory must use the same directory for both `server.socket_path` and the derived host lock; per-stack private Docker volumes are not a safe singleton mechanism.

In practice, the host-managed safe default is:

- `server.socket_path = "/run/agbox/agboxd.sock"`
- derived host lock path: `/run/agbox/agboxd.lock`

With that layout, accidental duplicate compose stacks on the same host contend on the same lock file and the later daemon fails before it can mutate the shared socket or Docker-managed runtime state.
