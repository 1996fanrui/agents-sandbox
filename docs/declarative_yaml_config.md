# Declarative YAML Configuration

AgentsSandbox supports declarative YAML configuration for sandbox creation. Instead of assembling all parameters in code, define the sandbox environment as YAML content and send it through the SDK.

## YAML Schema

The YAML schema is a 1:1 mapping of the proto `CreateSpec` message. Every field in `CreateSpec` has a corresponding YAML key; no extra fields are allowed.

```yaml
image: coding-runtime:latest

copies:
  - source: /absolute/path/to/project
    target: /workspace
    exclude_patterns: [".venv", "__pycache__", "node_modules"]

mounts:
  - source: /host/data
    target: /data
    writable: true

builtin_tools: ["claude", "git", "uv", "npm"]

labels:
  team: my-team

ports:
  - container_port: 8080
    host_port: 8080

envs:
  APP_ENV: production

companion_containers:
  postgres:
    image: postgres:16-alpine
    envs:
      POSTGRES_DB: app_local
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      start_period: 20s
      start_interval: 1s
      retries: 12
    post_start_on_primary: ["psql -U postgres -c 'CREATE EXTENSION IF NOT EXISTS vector'"]

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD-SHELL", "redis-cli ping"]
      interval: 5s
      start_period: 10s
      start_interval: 1s
      retries: 6
```

### With Resource Limits

```yaml
image: coding-runtime:latest

cpu_limit: "2"
memory_limit: "4g"
disk_limit: "10g"

companion_containers:
  postgres:
    image: postgres:16-alpine
    cpu_limit: "1"
    memory_limit: "512m"
    disk_limit: "5g"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      start_period: 20s
      start_interval: 1s
      retries: 12
```

All three limits (`cpu_limit`, `memory_limit`, `disk_limit`) are per-container and use Docker-native HostConfig keys directly: `HostConfig.NanoCPUs`, `HostConfig.Memory`, and `HostConfig.StorageOpt["size"]`. The top-level values constrain the primary container only; each companion carries its own independent set under `companion_containers.<name>`. There is no shared sandbox-level cgroup. See [Configuration Reference: Resource Limits Prerequisites](configuration_reference.md#resource-limits-prerequisites) for host requirements.

## Field Reference

| YAML Key | Proto Field | Type | Description |
|---|---|---|---|
| `image` | `CreateSpec.image` | string | Container image for the primary sandbox |
| `copies` | `CreateSpec.copies` | list of CopySpec | Files to copy into the container |
| `mounts` | `CreateSpec.mounts` | list of MountSpec | Bind mounts from host to container |
| `builtin_tools` | `CreateSpec.builtin_tools` | list of string | Built-in resources to provision |
| `companion_containers` | `CreateSpec.companion_containers` | map of CompanionContainerSpec | Companion containers started concurrently with the primary container |
| `labels` | `CreateSpec.labels` | map of string | Labels attached to the sandbox |
| `envs` | `CreateSpec.envs` | map of string | Env vars on primary container, inherited by all execs |
| `ports` | `CreateSpec.ports` | list of PortMapping | Port mappings to publish container ports to the host (localhost only) |
| `idle_ttl` | `CreateSpec.idle_ttl` | duration string | Per-sandbox idle TTL override. Omit to use the global daemon default. Set to `"0"` to disable idle stop for this sandbox. |
| `command` | `CreateSpec.command` | list of string | Optional override of the primary container's Docker `Cmd`. Omit to keep the daemon's sleep-loop default. Must be a long-lived process; `command: []` and empty-string entries are rejected. See [Configuration Reference: Primary container command](configuration_reference.md#primary-container-command). |
| `companion_containers.<name>.command` | `CompanionContainerSpec.command` | list of string | Optional override of a companion container's Docker `Cmd`. Omit to keep the image's built-in `CMD`. Same long-lived constraint and validation as the primary `command`. |
| `cpu_limit` | `CreateSpec.cpu_limit` | string | Docker `--cpus` style, e.g. `"2"`, `"0.5"`. `""` (omitted) = unlimited. Applies to the **primary container** only via `HostConfig.NanoCPUs`. |
| `memory_limit` | `CreateSpec.memory_limit` | string | Docker `--memory` style, e.g. `"4g"`, `"512m"`. `""` (omitted) = unlimited. Applies to the **primary container** only via `HostConfig.Memory`. |
| `disk_limit` | `CreateSpec.disk_limit` | string | Docker `--storage-opt size=` style, e.g. `"10g"`. `""` (omitted) = unlimited. Applies to the **primary container** only via `HostConfig.StorageOpt["size"]`. Requires overlay2 on XFS with `prjquota` + `ftype=1`; see [Configuration Reference: Resource Limits Prerequisites](configuration_reference.md#resource-limits-prerequisites). |
| `companion_containers.<name>.cpu_limit` | `CompanionContainerSpec.cpu_limit` | string | Per-companion CPU limit, same syntax as top-level `cpu_limit`. `""` (omitted) = unlimited. Wired to that companion's `HostConfig.NanoCPUs`. |
| `companion_containers.<name>.memory_limit` | `CompanionContainerSpec.memory_limit` | string | Per-companion memory limit, same syntax as top-level `memory_limit`. `""` (omitted) = unlimited. Wired to that companion's `HostConfig.Memory`. |
| `companion_containers.<name>.disk_limit` | `CompanionContainerSpec.disk_limit` | string | Per-companion disk limit, same syntax as top-level `disk_limit`. `""` (omitted) = unlimited. Wired to that companion's `HostConfig.StorageOpt["size"]`. Same prerequisites as the primary `disk_limit`; see [Configuration Reference: Resource Limits Prerequisites](configuration_reference.md#resource-limits-prerequisites). |

Quick self-check commands for the `disk_limit` prerequisites:

```bash
docker info --format '{{.Driver}} {{.DockerRootDir}}'
findmnt -T "$(docker info --format '{{.DockerRootDir}}')" -n -o OPTIONS
```

### CopySpec Fields

`source` (absolute host path), `target` (absolute container path), `exclude_patterns` (glob patterns to exclude). For detailed copy semantics and symlink handling, see [Container Dependency Strategy](container_dependency_strategy.md).

### MountSpec Fields

`source` (absolute host path), `target` (absolute container path), `writable` (default: false). For detailed mount semantics, see [Container Dependency Strategy](container_dependency_strategy.md).

### PortMapping Fields

`container_port` (uint32, required, 1-65535), `host_port` (uint32, required, 1-65535), `protocol` (string, default: `"tcp"`, one of `tcp`, `udp`, `sctp`). Ports bind to `127.0.0.1` (localhost only).

### CompanionContainerSpec and HealthcheckConfig Fields

Companion containers use a map format where the YAML key becomes the container name and network alias. For field definitions, usage scenarios, and healthcheck configuration, see [Companion Container Guide](companion_container_guide.md).

## SDK Usage

### Python SDK

```python
sandbox = await client.create_sandbox(config_yaml=yaml_config)

# YAML with parameter overrides
sandbox = await client.create_sandbox(
    config_yaml=yaml_config,
    image="custom:latest",
    labels={"team": "my-team"},
)

# Resource limits via explicit parameters
sandbox = await client.create_sandbox(
    image="coding-runtime:latest",
    cpu_limit="2",
    memory_limit="4g",
    disk_limit="10g",
)
```

### Go SDK

```go
sandbox, err := client.CreateSandbox(ctx, sdkclient.WithConfigYAML(configYAML))

// YAML with parameter overrides
sandbox, err := client.CreateSandbox(ctx,
    sdkclient.WithConfigYAML(configYAML),
    sdkclient.WithImage("custom:latest"),
)

// Resource limits via explicit options
sandbox, err := client.CreateSandbox(ctx,
    sdkclient.WithImage("coding-runtime:latest"),
    sdkclient.WithCPULimit("2"),
    sdkclient.WithMemoryLimit("4g"),
    sdkclient.WithPrimaryDiskLimit("10g"),
)
```

## Override Semantics

When both YAML and explicit parameters are provided, explicit parameters override YAML values following [JSON Merge Patch (RFC 7396)](https://www.rfc-editor.org/rfc/rfc7396) semantics:

| Field Type | Override Behavior |
|---|---|
| Scalar (`image`) | Non-empty explicit value overwrites YAML |
| Repeated (`mounts`, `copies`, etc.) | Non-empty explicit value replaces YAML entirely |
| Map (`labels`, `envs`) | Key-level merge: explicit keys overwrite; YAML-only keys preserved |

**Known limitation**: callers cannot use explicit parameters to clear a repeated field defined in YAML. Empty values are treated as "not set."

## Environment Variable Inheritance

Sandbox-level `envs` are applied to the primary container at creation time and inherited by all exec commands. Exec `env_overrides` merge on top with exec keys taking precedence.

## Architecture

YAML parsing is implemented in the daemon, not in SDKs. SDKs send raw YAML bytes via `config_yaml` in `CreateSandboxRequest`, avoiding duplicating parsing logic. The daemon uses strict parsing that rejects unknown fields.

Companion container ordering: when converting YAML companion container maps to proto `repeated CompanionContainerSpec`, entries are sorted alphabetically by name. All companion containers start concurrently.
