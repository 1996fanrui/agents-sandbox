# Async SDK Usage

`AgentsSandboxClient` is the single public async client for the Python SDK.

Create the client with the platform default daemon socket resolution:

```python
from agents_sandbox import AgentsSandboxClient, MountSpec

client = AgentsSandboxClient()
```

## Query APIs

Query methods always return the latest authoritative snapshot and do not accept `wait`:

- `ping`
- `get_sandbox`
- `list_sandboxes`
- `get_exec`
- `list_active_execs`

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
    mounts=(
        MountSpec(
            source="/path/to/workspace",
            target="/workspace",
            writable=True,
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

- build a baseline with `GetSandbox` or `GetExec`
- subscribe to the sandbox event stream
- use `cursor` and `sequence` to ignore replayed or stale events
- fetch authoritative state again before returning

Examples:

```python
sandbox = await client.create_sandbox(
    image="ghcr.io/agents-sandbox/coding-runtime:latest",
)

exec_handle = await client.create_exec(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
    wait=False,
)

result = await client.run(
    sandbox.sandbox_id,
    ("python", "-c", "print('hello')"),
)
print(result.stdout)
```

## Subscription

`subscribe_sandbox_events` is a public advanced API:

```python
async for event in client.subscribe_sandbox_events(
    sandbox_id,
    from_cursor="0",
):
    print(event.event_type.name, event.sequence)
```

Important rules:

- `from_cursor="0"` replays the full ordered event history for one sandbox
- other cursors must be daemon-issued values from the same sandbox stream
- callers must treat `cursor` and `sequence` as the ordering source of truth

## Recommended Usage

- normal lifecycle flow: use `wait=True` or call `run(...)` when you want a completed exec result
- advanced orchestration: use `wait=False` and call `subscribe_sandbox_events` directly
- example/demo flow: pass the runtime image explicitly on every `create_sandbox(...)` call
