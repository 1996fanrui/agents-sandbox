# Python SDK Usage

`AgentsSandboxClient` is the single public async client for the Python SDK with platform default daemon socket resolution.

```python
from agents_sandbox import (
    AgentsSandboxClient,
    HealthcheckConfig,
    MountSpec,
    ServiceSpec,
)

async with AgentsSandboxClient() as client:
    ...

# Or manually:
client = AgentsSandboxClient()
try:
    ...
finally:
    await client.aclose()
```

## Key Types

```python
@dataclass
class SandboxHandle:
    sandbox_id: str
    state: SandboxState
    last_event_sequence: int
    required_services: tuple[ServiceSpec, ...]
    optional_services: tuple[ServiceSpec, ...]
    labels: Mapping[str, str]
    created_at: datetime | None
    image: str
    error_code: str | None
    error_message: str | None
    state_changed_at: datetime | None
```

`ServiceSpec` uses `envs` (not `environment`); `HealthcheckConfig` uses `timedelta` for duration fields:

```python
@dataclass
class ServiceSpec:
    name: str
    image: str
    envs: Mapping[str, str]
    healthcheck: HealthcheckConfig | None
    post_start_on_primary: tuple[str, ...]

@dataclass
class HealthcheckConfig:
    test: tuple[str, ...]
    interval: timedelta | None
    timeout: timedelta | None
    retries: int | None
    start_period: timedelta | None
    start_interval: timedelta | None
```

## Query APIs

Query methods return the latest authoritative snapshot and do not accept `wait`: `ping`, `get_sandbox`, `list_sandboxes`, `get_exec`, `list_active_execs`.

`list_active_execs` accepts an optional keyword-only `sandbox_id` argument to filter results.

## Slow Operations

Slow operations use an explicit `wait` parameter. For the full accepted-vs-completed contract and wait semantics, see [Protocol Design Principles](protocol_design_principles.md).

- `create_sandbox`, `resume_sandbox`, `stop_sandbox`, `delete_sandbox` default to `wait=True`
- `create_exec` defaults to `wait=False`
- `cancel_exec` defaults to `wait=True`

`wait=False`: the SDK sends the command, fetches one authoritative snapshot; the returned handle may still be in progress.

```python
sandbox = await client.create_sandbox(
    image="ghcr.io/agents-sandbox/coding-runtime:latest",
    sandbox_id="demo-sandbox",
    mounts=(MountSpec(source="/path/to/workspace", target="/workspace", writable=True),),
    required_services=(
        ServiceSpec(
            name="postgres",
            image="postgres:16",
            healthcheck=HealthcheckConfig(
                test=("CMD-SHELL", "pg_isready -U postgres"),
                interval="5s",
                retries=5,
            ),
            post_start_on_primary=("python -c \"print('seeded')\"",),
        ),
    ),
    optional_services=(ServiceSpec(name="redis", image="redis:7"),),
    wait=False,
)
```

`wait=True`: the SDK subscribes to the event stream and waits using protocol ordering. For exec waits, the `ExecHandle.last_event_sequence` is the only supported handoff to `subscribe_sandbox_events`.

```python
sandbox = await client.create_sandbox(
    image="ghcr.io/agents-sandbox/coding-runtime:latest",
    sandbox_id="demo-sandbox",
)

exec_handle = await client.create_exec(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
    exec_id="my-exec",
    wait=True,
)

result = await client.run(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
)
print(result.stdout_log_path)
```

## Subscription

`subscribe_sandbox_events` is a public advanced API returning an async iterator of events with `event_type`, `sequence`, and typed sub-structs (`sandbox_phase`, `exec`, `service`):

```python
async for event in client.subscribe_sandbox_events(sandbox_id, from_sequence=0):
    print(event.event_type.name, event.sequence)
```

## Error Handling

Typed errors importable from `agents_sandbox`:

| Error | Description |
|-------|-------------|
| `SandboxClientError` | Base class for all SDK errors |
| `SandboxNotFoundError` | Sandbox does not exist; carries `.sandbox_id` |
| `SandboxNotReadyError` | Sandbox not in READY state |
| `SandboxConflictError` | Caller-provided ID already exists |
| `ExecNotFoundError` | Exec does not exist |
| `ExecNotRunningError` | Cancel called on non-running exec |
| `ExecAlreadyTerminalError` | Exec already in terminal state |
| `SandboxSequenceExpiredError` | Sequence garbage-collected; carries `.from_sequence` and `.oldest_sequence` |

Errors are translated from gRPC structured error metadata (`domain`, `reason`, `metadata`) into typed Python exceptions.

## Recommended Usage

- Normal lifecycle: `wait=True` or `run(...)` for completed results
- Advanced orchestration: `wait=False` + `subscribe_sandbox_events`
- Always pass the runtime image explicitly on every `create_sandbox(...)` call
- Declarative config: pass `config_yaml=...` (see [Declarative YAML Config](declarative_yaml_config.md))
