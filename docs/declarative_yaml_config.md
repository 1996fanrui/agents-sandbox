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
| `idle_ttl` | `CreateSpec.idle_ttl` | duration string | Per-sandbox idle TTL override. Omit to use the global daemon default. Set to `"0"` to disable idle stop for this sandbox. |

### CopySpec Fields

`source` (absolute host path), `target` (absolute container path), `exclude_patterns` (glob patterns to exclude). For detailed copy semantics and symlink handling, see [Container Dependency Strategy](container_dependency_strategy.md).

### MountSpec Fields

`source` (absolute host path), `target` (absolute container path), `writable` (default: false). For detailed mount semantics, see [Container Dependency Strategy](container_dependency_strategy.md).

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
```

### Go SDK

```go
sandbox, err := client.CreateSandbox(ctx, sdkclient.WithConfigYAML(configYAML))

// YAML with parameter overrides
sandbox, err := client.CreateSandbox(ctx,
    sdkclient.WithConfigYAML(configYAML),
    sdkclient.WithImage("custom:latest"),
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
