# Configuration Reference

This document is the source of truth for daemon and operator configuration in `agents-sandbox`.

If a change adds, removes, renames, or changes the default of a config key, update this document in the same change.

## Configuration Surface

`agents-sandbox` uses a `config.toml + secrets` model:

- `config.toml` stores the structured daemon settings.
- Secrets stay outside the repository and outside generated documentation.
- Daemon runtime paths are fixed platform paths, not operator-tunable config.

Northbound request fields are not part of this configuration surface. Request-time lifecycle inputs such as image selection, filesystem ingress (`mounts`, `copies`, `builtin_resources`), and service declarations belong to the RPC contract, not to `config.toml`.

The AgentsSandbox daemon always derives its runtime paths internally and then auto-loads the platform-default `config.toml` if it exists. There is no CLI flag, environment variable, or config key that overrides the socket path, lock path, config path, or ID store path.

## Fixed Platform Paths

| Resource | Linux | macOS |
|----------|-------|-------|
| Socket | `$XDG_RUNTIME_DIR/agbox/agboxd.sock` | `~/Library/Application Support/agbox/run/agboxd.sock` |
| Host lock | `$XDG_RUNTIME_DIR/agbox/agboxd.lock` | `~/Library/Application Support/agbox/run/agboxd.lock` |
| Config | `$XDG_CONFIG_HOME/agents-sandbox/config.toml`, or `~/.config/agents-sandbox/config.toml` when `XDG_CONFIG_HOME` is unset | `~/Library/Application Support/agents-sandbox/config.toml` |
| Historical ID store | `$XDG_DATA_HOME/agents-sandbox/ids.db`, or `~/.local/share/agents-sandbox/ids.db` when `XDG_DATA_HOME` is unset | `~/Library/Application Support/agents-sandbox/ids.db` |

The host lock always lives next to the socket so the lock protects the exact runtime socket path the daemon serves.

## Configuration Keys

| Key | Type | Recommended Default | Override Scope | Purpose |
|-----|------|---------------------|----------------|---------|
| `runtime.idle_ttl` | duration string | `"30m"` | Daemon config only | Idle stop threshold based on `last_terminal_run_finished_at` |
| `runtime.event_retention_ttl` | duration string | `"168h"` | Daemon config only | How long deleted sandbox event history remains queryable before cleanup removes it |
| `runtime.log_level` | string | `"info"` | Daemon config only | Log verbosity: `debug`, `info`, `warn`, `error` |
| `runtime.state_root` | string | unset | Daemon config only | Root for generic copy inputs and builtin-resource shadow-copy state |
| `artifacts.exec_output_root` | string | unset | Daemon config only | Root directory where runtime-owned exec output files are created |
| `artifacts.exec_output_template` | string | `"{sandbox_id}/{exec_id}.log"` | Daemon config only | Relative template expanded against `artifacts.exec_output_root`; supported fields are `sandbox_id` and `exec_id` |

## Request-Time Overrides

The northbound API may override only a narrow subset of behavior:

| Surface | Allowed Request Override | Notes |
|---------|--------------------------|-------|
| Primary sandbox image | Yes | Every sandbox request must provide its own runtime image; this is not a daemon config default |
| Generic mounts | Yes | Each sandbox may bind explicit host paths to explicit container targets |
| Generic copies | Yes | Each sandbox may copy explicit host files or trees into explicit container targets |
| Built-in resources | Yes | Each sandbox may request daemon-defined resource shortcuts such as `.claude`, `.codex`, `uv`, or `ssh-agent` |
| Caller-provided `sandbox_id` | Yes | If omitted, the daemon reserves a UUID v4 before accepting the request |
| Caller-provided `exec_id` | Yes | If omitted, the daemon reserves a UUID v4 before accepting the request |
| `required_services` | Yes | Each sandbox declares the services that must become healthy before the primary is reported ready |
| `optional_services` | Yes | Each sandbox declares the services whose initial startup result is reported without blocking readiness |
| `runtime.idle_ttl` | No | Idle stop policy stays daemon-owned |
| `runtime.event_retention_ttl` | No | Event retention policy stays daemon-owned |
| Resource limits | No | V1 does not support request-scoped resource limits |

The daemon persists sandbox event history in `ids.db` and keeps deleted sandbox streams queryable until `runtime.event_retention_ttl` expires. Cleanup then removes the retained event history and its deletion metadata together.

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
