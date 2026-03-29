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
  env: dev

envs:
  APP_ENV: production
  LOG_LEVEL: info

required_services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: app_local
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      start_period: 20s
      start_interval: 1s
      retries: 12
    post_start_on_primary: ["psql -U postgres -c 'CREATE EXTENSION IF NOT EXISTS vector'"]

optional_services:
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
| `required_services` | `CreateSpec.required_services` | map of ServiceSpec | Services that must be healthy before sandbox is READY |
| `optional_services` | `CreateSpec.optional_services` | map of ServiceSpec | Services started concurrently, not blocking READY |
| `labels` | `CreateSpec.labels` | map of string | Key-value labels attached to the sandbox |
| `envs` | `CreateSpec.envs` | map of string | Environment variables set on the primary container and inherited by all execs |

### CopySpec Fields

| YAML Key | Proto Field | Description |
|---|---|---|
| `source` | `CopySpec.source` | Absolute source path on the host |
| `target` | `CopySpec.target` | Target path in the container (must be absolute) |
| `exclude_patterns` | `CopySpec.exclude_patterns` | Glob patterns to exclude from copy |

### MountSpec Fields

| YAML Key | Proto Field | Description |
|---|---|---|
| `source` | `MountSpec.source` | Absolute source path on the host |
| `target` | `MountSpec.target` | Target path in the container (must be absolute) |
| `writable` | `MountSpec.writable` | Whether the mount is writable (default: false) |

### ServiceSpec Fields

Services use a map format where the YAML key becomes `ServiceSpec.name`:

| YAML Key | Proto Field | Description |
|---|---|---|
| _(map key)_ | `ServiceSpec.name` | Service name |
| `image` | `ServiceSpec.image` | Container image for the service |
| `environment` | `ServiceSpec.environment` | Environment variables (YAML map → proto `repeated KeyValue`) |
| `healthcheck` | `ServiceSpec.healthcheck` | Healthcheck configuration |
| `post_start_on_primary` | `ServiceSpec.post_start_on_primary` | Commands to run on the primary container after service starts |

### HealthcheckConfig Fields

| YAML Key | Proto Field | Description |
|---|---|---|
| `test` | `HealthcheckConfig.test` | Healthcheck command (e.g., `["CMD-SHELL", "pg_isready"]`) |
| `interval` | `HealthcheckConfig.interval` | Check interval (e.g., `5s`) |
| `timeout` | `HealthcheckConfig.timeout` | Check timeout (e.g., `3s`) |
| `retries` | `HealthcheckConfig.retries` | Max retry count |
| `start_period` | `HealthcheckConfig.start_period` | Grace period before checks count (e.g., `20s`) |
| `start_interval` | `HealthcheckConfig.start_interval` | Check interval during start period (e.g., `1s`) |

## SDK Usage

### Python SDK

```python
yaml_config = """
image: ghcr.io/agents-sandbox/coding-runtime:latest
builtin_tools:
  - claude
"""

# YAML only
sandbox = await client.create_sandbox(config_yaml=yaml_config)

# YAML with parameter overrides
sandbox = await client.create_sandbox(
    config_yaml=yaml_config,
    image="custom:latest",
    labels={"team": "my-team"},
    envs={"APP_ENV": "staging"},
)

# No YAML (existing behavior)
sandbox = await client.create_sandbox(image="python:3.12")
```

### Go SDK

```go
configYAML := []byte(`
image: ghcr.io/agents-sandbox/coding-runtime:latest
builtin_tools:
  - claude
`)

// YAML only
sandbox, err := client.CreateSandbox(ctx, sdkclient.WithConfigYAML(configYAML))

// YAML with parameter overrides
sandbox, err := client.CreateSandbox(ctx,
    sdkclient.WithConfigYAML(configYAML),
    sdkclient.WithImage("custom:latest"),
    sdkclient.WithLabels(map[string]string{"team": "my-team"}),
    sdkclient.WithEnvs(map[string]string{"APP_ENV": "staging"}),
)

// No YAML
sandbox, err := client.CreateSandbox(ctx, sdkclient.WithImage("python:3.12"))
```

## Override Semantics

When both YAML config and explicit parameters are provided, explicit parameters override YAML values following [JSON Merge Patch (RFC 7396)](https://www.rfc-editor.org/rfc/rfc7396) semantics:

| Field Type | Override Behavior |
|---|---|
| Scalar (`image`) | Non-empty explicit value overwrites YAML value |
| Repeated (`mounts`, `copies`, `builtin_tools`, `required_services`, `optional_services`) | Non-empty explicit value replaces YAML value entirely |
| Map (`labels`, `envs`) | Key-level merge: explicit keys overwrite same-name YAML keys; YAML-only keys are preserved |

### Known Limitation

Callers cannot use explicit parameters to _clear_ a repeated field defined in YAML. For example, passing empty `required_services` will not remove services defined in the YAML file, because empty values are treated as "not set" and the YAML value is preserved.

## Environment Variable Inheritance

Sandbox-level `envs` are applied to the primary container at creation time and automatically inherited by all exec commands. When an exec specifies `env_overrides`, the values are merged on top of the sandbox-level envs — exec keys take precedence over sandbox keys with the same name.

## Architecture

YAML parsing is implemented in the daemon, not in SDKs. SDKs send raw YAML bytes to the daemon via the `config_yaml` field in `CreateSandboxRequest`. Callers may load YAML from a file, template it in memory, or generate it dynamically before the SDK call. This avoids duplicating parsing logic across Python, Go, and future SDKs.

The daemon uses strict YAML parsing that rejects unknown fields, ensuring the YAML schema stays aligned with the proto `CreateSpec` definition.

Service ordering: when converting YAML service maps to proto `repeated ServiceSpec`, services are sorted alphabetically by name. For `required_services`, this sort order determines the sequential startup order. For `optional_services`, services start concurrently regardless of sort order.
