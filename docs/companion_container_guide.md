# Companion Container Guide

When the agent needs to debug or develop against external dependencies (databases, caches, message queues, etc.), declare them as companion containers.

They run as sibling containers on the same dedicated network, start in parallel with the primary container, and never block sandbox readiness. A healthy companion emits `COMPANION_CONTAINER_READY`; a failed companion emits `COMPANION_CONTAINER_FAILED` — the sandbox continues normally either way.

## CompanionContainerSpec Fields

The YAML key (e.g. `postgres`) becomes the companion container `name` and the DNS network alias — the primary container reaches companions by name (e.g. `postgres:5432`).

| Field | Type | Description |
|-------|------|-------------|
| `image` | string | Container image for the companion container (required) |
| `envs` | map | Environment variables passed to the companion container |
| `healthcheck` | HealthcheckConfig | How the daemon determines the companion is healthy (see below) |
| `post_start_on_primary` | list of string | Commands executed on the **primary** container after this companion becomes healthy. Requires `healthcheck` to be defined. |

### HealthcheckConfig Fields

Duration fields accept duration strings (e.g. `"5s"`, `"1m30s"`).

| Field | Description |
|-------|-------------|
| `test` | Healthcheck command (e.g. `["CMD-SHELL", "pg_isready -U postgres"]`) |
| `interval` | Time between checks |
| `timeout` | Single check timeout |
| `retries` | Max consecutive failures before unhealthy |
| `start_period` | Grace period — failures during this window do not count toward retries |
| `start_interval` | Check interval during `start_period` (can be shorter than `interval`) |

## Example

```yaml
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
    # Seed test data after DB is healthy
    post_start_on_primary:
      - "psql -h postgres -U postgres -c 'CREATE TABLE users (id serial, name text)'"

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD-SHELL", "redis-cli ping"]
      interval: 5s
      start_period: 10s
      start_interval: 1s
      retries: 6
```

For YAML schema overview and SDK usage, see [Declarative YAML Config](declarative_yaml_config.md). For startup flow internals, see [Container Dependency Strategy](container_dependency_strategy.md).
