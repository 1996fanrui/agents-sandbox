# Configuration Reference

This document is the source of truth for daemon and operator configuration in `agents-sandbox`.

If a change adds, removes, renames, or changes the default of a config key, update this document in the same change.

## Configuration Surface

`agents-sandbox` uses a `config.toml + secrets` model:

- `config.toml` stores the structured daemon settings.
- Secrets stay outside the repository and outside generated documentation.
- Daemon runtime paths are fixed platform paths, not operator-tunable config.

Northbound request fields are not part of this configuration surface. Request-time lifecycle inputs such as image selection, filesystem ingress (`mounts`, `copies`, `builtin_tools`), and companion container declarations belong to the RPC contract, not to `config.toml`.

The AgentsSandbox daemon always derives its runtime paths internally and then auto-loads the platform-default `config.toml` if it exists. There is no CLI flag, environment variable, or config key that overrides the socket path, lock path, config path, or ID store path.

## Fixed Platform Paths

| Resource | Linux | macOS |
|----------|-------|-------|
| Socket | `$XDG_RUNTIME_DIR/agbox/agboxd.sock` | `~/Library/Application Support/agbox/agboxd.sock` |
| Host lock | `$XDG_RUNTIME_DIR/agbox/agboxd.lock` | `~/Library/Application Support/agbox/agboxd.lock` |
| Config | `$XDG_CONFIG_HOME/agents-sandbox/config.toml`, or `~/.config/agents-sandbox/config.toml` when `XDG_CONFIG_HOME` is unset | `~/Library/Application Support/agents-sandbox/config.toml` |
| Historical ID store | `$XDG_DATA_HOME/agents-sandbox/ids.db`, or `~/.local/share/agents-sandbox/ids.db` when `XDG_DATA_HOME` is unset | `~/Library/Application Support/agents-sandbox/ids.db` |

The host lock always lives next to the socket so the lock protects the exact runtime socket path the daemon serves.

## Configuration Keys

| Key | Type | Recommended Default | Override Scope | Purpose |
|-----|------|---------------------|----------------|---------|
| `runtime.idle_ttl` | duration string | `"10m"` | Daemon config only | Global idle stop threshold based on `last_terminal_run_finished_at`. Set to `"0"` to disable idle detection globally. |
| `runtime.cleanup_ttl` | duration string | `"360h"` | Daemon config only | Time after which STOPPED sandboxes have their Docker resources cleaned up and DB records deleted, and DELETED sandbox event history is purged |
| `runtime.log_level` | string | `"info"` | Daemon config only | Log verbosity: `debug`, `info`, `warn`, `error` |
| `artifacts.exec_output_root` | string | Platform default: Linux: `$XDG_DATA_HOME/agents-sandbox/exec-logs`; macOS: `~/Library/Application Support/agents-sandbox/exec-logs` | Daemon config only | Root directory for exec log files; bind-mounted into the primary container at `/var/log/agents-sandbox/` so exec output is written directly to the host |
| `artifacts.exec_output_template` | string | `"{sandbox_id}/{exec_id}"` | Daemon config only | Relative path prefix expanded against `artifacts.exec_output_root`; supported fields are `sandbox_id` and `exec_id`; daemon appends `.stdout.log` and `.stderr.log` suffixes |

## Request-Time Overrides

The northbound API may override only a narrow subset of behavior:

| Surface | Allowed Request Override | Notes |
|---------|--------------------------|-------|
| Primary sandbox image | Yes | Every sandbox request must provide its own runtime image; this is not a daemon config default |
| Generic mounts | Yes | Each sandbox may bind explicit host paths to explicit container targets. `source` and `target` support `~` prefix: `source` expands to the host user's home directory; `target` expands to the container user's home directory. `~username` syntax is not supported. |
| Generic copies | Yes | Each sandbox may copy explicit host files or trees into explicit container targets. `source` and `target` support `~` prefix: `source` expands to the host user's home directory; `target` expands to the container user's home directory. `~username` syntax is not supported. |
| Built-in resources | Yes | Each sandbox may request daemon-defined resource shortcuts such as `claude`, `codex`, `git`, `uv`, `npm`, or `apt` |
| Caller-provided `config_yaml` | Yes | Inline YAML configuration; when provided, field-level values from `CreateSpec` override the YAML (RFC 7396 merge semantics) |
| Caller-provided `sandbox_id` | Yes | If omitted, the daemon reserves a UUID v4 before accepting the request |
| Caller-provided `exec_id` | Yes | If omitted, the daemon reserves a UUID v4 before accepting the request |
| `companion_containers` | Yes | Each sandbox declares companion containers started concurrently with the primary container |
| `runtime.idle_ttl` | Yes | `CreateSpec.idle_ttl` overrides the global threshold per sandbox. `nil` (unset) uses the daemon global default; `0` disables idle stop for that sandbox. |
| `runtime.cleanup_ttl` | No | Cleanup policy stays daemon-owned |
| Resource limits | No | V1 does not support request-scoped resource limits |

The daemon persists sandbox event history in `ids.db`. For STOPPED sandboxes, once `runtime.cleanup_ttl` elapses since the sandbox entered STOPPED state, the daemon automatically removes Docker resources (containers, network) and deletes the sandbox record from the database. For DELETED sandboxes, once `runtime.cleanup_ttl` elapses since deletion, the daemon purges the retained event history and deletion metadata.

## Singleton Deployment Rule

The AgentsSandbox daemon is a single-writer control plane for one host runtime. Starting a second daemon against the same Docker daemon is unsafe because cleanup and lifecycle decisions are host-global.

Supported deployments must satisfy all of these constraints:

- The daemon acquires an exclusive non-blocking lock before it removes any socket path or starts gRPC serving.
- The daemon exits fail-fast if the lock is already held.
- A consumer that bind-mounts the host runtime directory must expose the same platform-derived runtime directory for both the socket and host lock; per-stack private Docker volumes are not a safe singleton mechanism.

In practice, the host-managed safe default is the same platform-derived runtime root:

- socket path: `$XDG_RUNTIME_DIR/agbox/agboxd.sock` on Linux
- host lock path: `$XDG_RUNTIME_DIR/agbox/agboxd.lock` on Linux

With that layout, accidental duplicate daemons in the same user runtime contend on the same lock file and the later daemon fails before it can mutate the shared socket or Docker-managed runtime state.
