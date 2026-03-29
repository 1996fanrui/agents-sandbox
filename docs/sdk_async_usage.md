# Async SDK Usage

`AgentsSandboxClient` is the single public async client for the Python SDK.

Create the client with the platform default daemon socket resolution:

```python
from agents_sandbox import (
    AgentsSandboxClient,
    HealthcheckConfig,
    MountSpec,
    ServiceSpec,
)

client = AgentsSandboxClient()
```

Use `async with` or call `aclose()` explicitly to release resources:

```python
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

`SandboxHandle` includes `created_at` and `image`:

```python
@dataclass
class SandboxHandle:
    sandbox_id: str
    state: SandboxState
    last_event_sequence: int
    required_services: tuple[ServiceSpec, ...]
    optional_services: tuple[ServiceSpec, ...]
    labels: Mapping[str, str]
    created_at: datetime | None   # None if not set by daemon
    image: str
```

`ServiceSpec` uses `envs` (not `environment`):

```python
@dataclass
class ServiceSpec:
    name: str
    image: str
    envs: Mapping[str, str]
    healthcheck: HealthcheckConfig | None
    post_start_on_primary: tuple[str, ...]
```

`HealthcheckConfig` uses `timedelta` for duration fields:

```python
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

Query methods always return the latest authoritative snapshot and do not accept `wait`:

- `ping`
- `get_sandbox`
- `list_sandboxes`
- `get_exec`
- `list_active_execs`

`list_active_execs` accepts an optional keyword-only `sandbox_id` argument to filter results:

```python
# All active execs
execs = await client.list_active_execs()

# Active execs for one sandbox
execs = await client.list_active_execs(sandbox_id="sandbox-abc")
```

## Slow Operations

Slow operations use an explicit `wait` parameter:

- `create_sandbox`, `resume_sandbox`, `stop_sandbox`, `delete_sandbox` default to `wait=True`
- `create_exec` defaults to `wait=False`
- `cancel_exec` defaults to `wait=True`

`wait=False` keeps the accepted async contract visible to the caller:

- the SDK sends the command
- the SDK fetches one authoritative snapshot
- the returned handle may still be in progress

Example:

```python
sandbox = await client.create_sandbox(
    image="ghcr.io/agents-sandbox/coding-runtime:latest",
    sandbox_id="demo-sandbox",
    mounts=(
        MountSpec(
            source="/path/to/workspace",
            target="/workspace",
            writable=True,
        ),
    ),
    required_services=(
        ServiceSpec(
            name="postgres",
            image="postgres:16",
            healthcheck=HealthcheckConfig(
                test=("CMD-SHELL", "pg_isready -U postgres"),
                interval="5s",
                retries=5,
            ),
            post_start_on_primary=(
                "python -c \"print('seeded')\"",
            ),
        ),
    ),
    optional_services=(
        ServiceSpec(
            name="redis",
            image="redis:7",
        ),
    ),
    wait=False,
)

if sandbox.state.name == "READY":
    ...
elif sandbox.state.name == "FAILED":
    ...
else:
    ...
```

`wait=True` adds SDK-side waiting on top of the protocol contract:

- build a baseline snapshot with the authoritative read for the resource being waited on
- subscribe to the sandbox event stream
- use numeric event sequences to ignore replayed or stale events
- fetch authoritative state again before returning

For exec waits specifically:

- the public `ExecHandle.last_event_sequence` returned by `get_exec`, `create_exec`, or `run` is the only supported handoff to `subscribe_sandbox_events`
- the SDK must not borrow the sequence anchor from `GetSandbox`
- the SDK must not fall back to timeout-driven `GetExec` polling

Examples:

```python
sandbox = await client.create_sandbox(
    image="ghcr.io/agents-sandbox/coding-runtime:latest",
    sandbox_id="demo-sandbox",
)

exec_handle = await client.create_exec(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
    exec_id="exec-demo",
    wait=False,
)

result = await client.run(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
)
print(result.stdout_log_path)
```

## Subscription

`subscribe_sandbox_events` is a public advanced API:

```python
async for event in client.subscribe_sandbox_events(
    sandbox_id,
    from_sequence=0,
):
    # event.event_type is a SandboxEventType enum value
    # detail sub-structs hold event-specific data
    print(event.event_type.name, event.sequence)
    if event.sandbox_phase is not None:
        print("phase:", event.sandbox_phase.phase)
    if event.exec is not None:
        print("exec:", event.exec.exec_id, event.exec.exec_state)
    if event.service is not None:
        print("service:", event.service.service_name, event.service.error_code)
```

Important rules:

- `from_sequence=0` replays the full ordered event history for one sandbox
- other sequence anchors must be daemon-issued event sequences from the same sandbox stream
- callers must treat event sequences as the ordering source of truth
- event detail is carried in typed sub-structs: `sandbox_phase`, `exec`, `service`

## Error Handling

Typed errors are importable directly from `agents_sandbox`:

```python
from agents_sandbox import (
    SandboxClientError,
    SandboxNotFoundError,
    SandboxNotReadyError,
    SandboxConflictError,
    ExecNotFoundError,
    ExecNotRunningError,
    ExecAlreadyTerminalError,
    SandboxSequenceExpiredError,
)
```

Common patterns:

- `SandboxNotFoundError` — sandbox does not exist; carries `.sandbox_id`
- `SandboxConflictError` — caller-provided ID already exists; carries `.sandbox_id`
- `ExecNotRunningError` — `cancel_exec` called on an exec that is no longer running; carries `.exec_id`
- `SandboxSequenceExpiredError` — `subscribe_sandbox_events` from a sequence that has been garbage-collected; carries `.from_sequence` and `.oldest_sequence`
- `SandboxClientError` — base class for all of the above; catch for generic SDK errors

Errors are translated from gRPC structured error metadata (`domain`, `reason`, `metadata`) into typed Python exceptions. The translation is performed by the Python SDK's gRPC error handler.

## Recommended Usage

- normal lifecycle flow: use `wait=True` or call `run(...)` when you want a completed exec result
- advanced orchestration: use `wait=False` and call `subscribe_sandbox_events` directly
- example/demo flow: pass the runtime image explicitly on every `create_sandbox(...)` call
- declarative config: pass `config_yaml=...` with YAML content generated in memory or loaded by your application (see `docs/declarative_yaml_config.md`)

```python
# Create from YAML content
sandbox = await client.create_sandbox(
    config_yaml="image: ghcr.io/agents-sandbox/coding-runtime:latest\n"
)

# YAML config with parameter overrides
sandbox = await client.create_sandbox(
    config_yaml="builtin_tools:\n  - claude\n",
    image="custom:latest",
    labels={"team": "my-team"},
)
```
