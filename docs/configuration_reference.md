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
| `command` (primary) | Yes | Optional override of primary container CMD; defaults to daemon sleep-loop when omitted. Must be a long-lived process — see [Primary container command](#primary-container-command). |
| `companion_containers.<name>.command` | Yes | Optional override of a companion container CMD; defaults to the image's built-in `CMD` when omitted. Must be a long-lived process with the same exit semantics as the primary `command`. |
| `ports` | Yes | Each sandbox may expose container ports to the host via Docker port publishing (`-p`). Each entry specifies `container_port`, `host_port`, and `protocol` (tcp/udp/sctp). |
| `runtime.idle_ttl` | Yes | `CreateSpec.idle_ttl` overrides the global threshold per sandbox. `nil` (unset) uses the daemon global default; `0` disables idle stop for that sandbox. |
| `runtime.cleanup_ttl` | No | Cleanup policy stays daemon-owned |
| `CreateSpec.cpu_limit` | Yes | Docker `--cpus` style, e.g. `"2"`, `"0.5"`. `""` = unlimited. Sandbox-scoped (primary and all companions share the cgroup). See [Resource Limits Prerequisites](#resource-limits-prerequisites). |
| `CreateSpec.memory_limit` | Yes | Docker `--memory` style, e.g. `"4g"`, `"512m"`. `""` = unlimited. Sandbox-scoped (primary and all companions share the cgroup). See [Resource Limits Prerequisites](#resource-limits-prerequisites). |
| `CreateSpec.disk_limit` / `CompanionContainerSpec.disk_limit` | Yes | Docker `--storage-opt size=` style, e.g. `"10g"`. Per-container (primary and each companion carry their own limit). `""` = unlimited. See [Resource Limits Prerequisites](#resource-limits-prerequisites). |

The daemon persists sandbox event history in `ids.db`. For STOPPED sandboxes, once `runtime.cleanup_ttl` elapses since the sandbox entered STOPPED state, the daemon automatically removes Docker resources (containers, network) and deletes the sandbox record from the database. For DELETED sandboxes, once `runtime.cleanup_ttl` elapses since deletion, the daemon purges the retained event history and deletion metadata.

## Resource Limits Prerequisites

Resource limits (`cpu_limit`, `memory_limit`, `disk_limit`) require specific host runtime capabilities. The daemon does not probe these at startup; missing prerequisites surface as `FailedPrecondition` either at validation time (CPU/memory) or at `ContainerCreate` time (disk).

- **CPU / memory limits** require cgroup v2 and Docker's `CgroupDriver=systemd`. When a sandbox sets a non-empty `cpu_limit` or `memory_limit` but either condition is not met, the daemon rejects the request with `FailedPrecondition`. The daemon creates a per-sandbox transient systemd slice (`agbox-<sandbox-id>.slice` under parent `agbox.slice`) and attaches the primary and all companion containers to it via Docker `--cgroup-parent`.
- **Disk limits** require the overlay2 storage driver on an XFS filesystem mounted with `prjquota` and formatted with `ftype=1`. When the backing storage does not satisfy these constraints, `docker create` fails and the daemon wraps the error as `FailedPrecondition`, preserving the Docker message for diagnosis.

Self-check on the host:

```
docker info --format '{{.CgroupDriver}} {{.CgroupVersion}}'
DOCKER_ROOT=$(docker info --format '{{.DockerRootDir}}')
findmnt -T "$DOCKER_ROOT" -n -o OPTIONS
```

Expect `systemd 2` for CPU/memory support, and the `findmnt` output to contain `prjquota` for disk support. For XFS, `ftype=1` can be confirmed with `xfs_info "$DOCKER_ROOT" | grep ftype`.

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

## Primary container command

The optional top-level `command` field on a sandbox request overrides the primary container's Docker `Cmd`. It maps 1:1 onto `CreateSpec.command` in the RPC contract and onto Docker's `container.Config.Cmd` on the runtime side.

```yaml
image: my-image:latest
command: ["myworker", "serve", "--foreground"]
```

**Default when omitted.** If `command` is not set, the daemon falls back to its built-in sleep-loop (`sh -lc "trap 'exit 0' TERM INT; while sleep 3600; do :; done"`). Existing sandboxes that never set `command` keep this sleep-loop behavior, so omitting the field is the zero-variance path.

**Long-lived constraint.** A user-supplied `command` must be a long-lived / long-running foreground process. Docker treats the main process as the container lifetime anchor: process exit → container exit → sandbox unusable until restart. Short-lived commands (e.g. `echo hi`, one-shot scripts) cause the primary container to exit almost immediately, which in turn makes the sandbox unusable for further `CreateExec` calls. Users who want the legacy always-ready behavior should simply omit `command`.

**Validation.** `command: []` (explicit empty array) and any empty string entry (e.g. `command: ["foo", ""]`) are rejected at the YAML parse layer and at the SDK entrypoints. proto3 cannot distinguish an omitted `repeated string` from an explicitly empty one, so the daemon's `validateCreateSpec` enforces the empty-string-element check only; the empty-array check is the responsibility of the YAML layer and the SDKs.

**Interaction with `entrypoint.sh`.** The image `ENTRYPOINT` is intentionally not user-configurable. The image's `entrypoint.sh` remains the container entrypoint and is still responsible for UID/GID setup plus the final `exec gosu "$HOST_UID:$HOST_GID" "$@"` drop-privilege step. `command` supplies the argv that `entrypoint.sh` execs into; the first token must be an executable available inside the image. `entrypoint` itself is not exposed on the RPC or YAML surface — overriding it would bypass the UID/GID + gosu contract that the runtime image relies on.

### Companion container command

The same field exists as `companion_containers.<name>.command` and carries identical semantics for companion containers:

```yaml
companion_containers:
  worker:
    image: my-worker:latest
    command: ["my-worker", "--foreground"]
```

- When `command` is omitted on a companion, the daemon does not send `Cmd` to Docker and the image's built-in `CMD` applies — this matches the prior default behavior.
- When set, it must be a long-lived process for the same reason as the primary container: the companion's main process is still the container lifetime anchor.
- Validation (empty array / empty string entries) follows the same layering; error messages include the companion name for locatability.
- The companion image's own `ENTRYPOINT` is preserved; as with the primary container, `entrypoint` is intentionally not exposed.
