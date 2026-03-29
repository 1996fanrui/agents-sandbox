from __future__ import annotations

import asyncio
from collections.abc import Iterator

import pytest
from agents_sandbox import ExecHandle, ExecState, SandboxHandle, SandboxState
from agents_sandbox._generated import service_pb2

from tests.smoke_support import _event_pb, _exec_response, _new_client

def test_create_exec_cwd_env_overrides_and_exec_id_serialize_to_proto(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        create_exec_requests: list[service_pb2.CreateExecRequest] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            self.create_exec_requests.append(request)
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> object:
            return _exec_response(
                service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=service_pb2.EXEC_STATE_RUNNING,
                    command=["echo", "hello"],
                    cwd="/workspace",
                ),
                last_event_sequence=1,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        handle = await client.create_exec(
            "sandbox-1",
            ("echo", "hello"),
            exec_id="exec-explicit",
            cwd="/work",
            env_overrides={"HELLO": "world"},
            wait=False,
        )
        assert handle.last_event_sequence == 1
        default_handle = await client.create_exec("sandbox-1", ("echo", "hello"), wait=False)
        assert default_handle.last_event_sequence == 1

    asyncio.run(run_test())

    explicit_request = _FakeRawSandboxClient.create_exec_requests[0]
    default_request = _FakeRawSandboxClient.create_exec_requests[1]
    assert explicit_request.exec_id == "exec-explicit"
    assert explicit_request.cwd == "/work"
    assert dict(explicit_request.env_overrides) == {"HELLO": "world"}
    assert default_request.exec_id == ""
    assert default_request.cwd == "/workspace"
    assert dict(default_request.env_overrides) == {}


def test_run_waits_and_returns_exec_handle(monkeypatch: pytest.MonkeyPatch) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, int, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._get_exec_calls = 0

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            assert request.cwd == "/workspace"
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> object:
            self._get_exec_calls += 1
            state = service_pb2.EXEC_STATE_RUNNING
            last_event_sequence = 10
            if self._get_exec_calls > 1:
                state = service_pb2.EXEC_STATE_FINISHED
                last_event_sequence = 11
            return _exec_response(
                service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    exit_code=0,
                ),
                last_event_sequence=last_event_sequence,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=11,
                event_type=service_pb2.EXEC_FINISHED,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> ExecHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.run("sandbox-1", ("echo", "hello"))

    exec_handle = asyncio.run(run_test())

    assert exec_handle.state is ExecState.FINISHED
    assert exec_handle.last_event_sequence == 11
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", 10, False)]


def test_agents_sandbox_client_wait_true_ignores_replayed_old_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, int, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._get_sandbox_calls = 0

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            assert request.create_spec.image == "python:3.12-slim"
            # CreateSandboxResponse now carries the full SandboxHandle.
            return service_pb2.CreateSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_PENDING,
                    last_event_sequence=5,
                )
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            self._get_sandbox_calls += 1
            state = service_pb2.SANDBOX_STATE_READY
            sequence = 6
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    state=state,
                    last_event_sequence=sequence,
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=4,
                event_type=service_pb2.SANDBOX_READY,
                replay=True,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=5,
                event_type=service_pb2.SANDBOX_READY,
                replay=True,
                snapshot=True,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=6,
                event_type=service_pb2.SANDBOX_READY,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> SandboxHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.create_sandbox(image="python:3.12-slim", sandbox_id="sandbox-1", wait=True)

    sandbox = asyncio.run(run_test())

    assert sandbox.state is SandboxState.READY
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", 5, False)]


def test_agents_sandbox_client_wait_true_short_circuits_when_baseline_is_terminal(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_calls = 0

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            del request
            # Return READY state directly so wait=True short-circuits without subscribing.
            return service_pb2.CreateSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_sequence=9,
                )
            )

        def subscribe_sandbox_events(self, *args, **kwargs):  # noqa: ANN002, ANN003
            del args, kwargs
            _FakeRawSandboxClient.subscribe_calls += 1
            raise AssertionError("subscribe should not run when the baseline is already terminal")

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> SandboxHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.create_sandbox(image="python:3.12-slim", sandbox_id="sandbox-1", wait=True)

    sandbox = asyncio.run(run_test())

    assert sandbox.state is SandboxState.READY
    assert _FakeRawSandboxClient.subscribe_calls == 0


def test_sandbox_lifecycle_wait_paths_cover_wait_false_and_wait_true(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, int, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._operation = ""
            self._resume_calls = 0
            self._stop_calls = 0
            self._delete_calls = 0
            self._sandbox_responses = {
                "resume-false": [
                    (service_pb2.SANDBOX_STATE_PENDING, 1),
                ],
                "resume-true": [
                    (service_pb2.SANDBOX_STATE_PENDING, 2),
                    (service_pb2.SANDBOX_STATE_READY, 3),
                ],
                "stop-false": [
                    (service_pb2.SANDBOX_STATE_READY, 4),
                ],
                "stop-true": [
                    (service_pb2.SANDBOX_STATE_READY, 5),
                    (service_pb2.SANDBOX_STATE_STOPPED, 6),
                ],
                "delete-false": [
                    (service_pb2.SANDBOX_STATE_DELETING, 7),
                ],
                "delete-true": [
                    (service_pb2.SANDBOX_STATE_DELETING, 8),
                    (service_pb2.SANDBOX_STATE_DELETED, 9),
                ],
            }

        def close(self) -> None:
            return None

        def resume_sandbox(self, sandbox_id: str) -> service_pb2.AcceptedResponse:
            del sandbox_id
            self._operation = "resume-false" if self._resume_calls == 0 else "resume-true"
            self._resume_calls += 1
            return service_pb2.AcceptedResponse(accepted=True)

        def stop_sandbox(self, sandbox_id: str) -> service_pb2.AcceptedResponse:
            del sandbox_id
            self._operation = "stop-false" if self._stop_calls == 0 else "stop-true"
            self._stop_calls += 1
            return service_pb2.AcceptedResponse(accepted=True)

        def delete_sandbox(self, sandbox_id: str) -> service_pb2.AcceptedResponse:
            del sandbox_id
            self._operation = "delete-false" if self._delete_calls == 0 else "delete-true"
            self._delete_calls += 1
            return service_pb2.AcceptedResponse(accepted=True)

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            state, sequence = self._sandbox_responses[self._operation].pop(0)
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    state=state,
                    last_event_sequence=sequence,
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            if self._operation == "resume-true":
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=2,
                    event_type=service_pb2.SANDBOX_READY,
                    replay=True,
                    sandbox_state=service_pb2.SANDBOX_STATE_READY,
                )
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=3,
                    event_type=service_pb2.SANDBOX_READY,
                    sandbox_state=service_pb2.SANDBOX_STATE_READY,
                )
                return
            if self._operation == "stop-true":
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=5,
                    event_type=service_pb2.SANDBOX_STOPPED,
                    replay=True,
                    sandbox_state=service_pb2.SANDBOX_STATE_STOPPED,
                )
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=6,
                    event_type=service_pb2.SANDBOX_STOPPED,
                    sandbox_state=service_pb2.SANDBOX_STATE_STOPPED,
                )
                return
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=8,
                event_type=service_pb2.SANDBOX_DELETED,
                replay=True,
                sandbox_state=service_pb2.SANDBOX_STATE_DELETED,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=9,
                event_type=service_pb2.SANDBOX_DELETED,
                sandbox_state=service_pb2.SANDBOX_STATE_DELETED,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> tuple[SandboxHandle, SandboxHandle, SandboxHandle, SandboxHandle, SandboxHandle, SandboxHandle]:
        client = _new_client("/tmp/agents-sandbox.sock")
        resume_pending = await client.resume_sandbox("sandbox-1", wait=False)
        resume_ready = await client.resume_sandbox("sandbox-1", wait=True)
        stop_ready = await client.stop_sandbox("sandbox-1", wait=False)
        stop_stopped = await client.stop_sandbox("sandbox-1", wait=True)
        delete_deleting = await client.delete_sandbox("sandbox-1", wait=False)
        delete_deleted = await client.delete_sandbox("sandbox-1", wait=True)
        return (
            resume_pending,
            resume_ready,
            stop_ready,
            stop_stopped,
            delete_deleting,
            delete_deleted,
        )

    (
        resume_pending,
        resume_ready,
        stop_ready,
        stop_stopped,
        delete_deleting,
        delete_deleted,
    ) = asyncio.run(run_test())

    assert resume_pending.state is SandboxState.PENDING
    assert resume_ready.state is SandboxState.READY
    assert stop_ready.state is SandboxState.READY
    assert stop_stopped.state is SandboxState.STOPPED
    assert delete_deleting.state is SandboxState.DELETING
    assert delete_deleted.state is SandboxState.DELETED
    assert _FakeRawSandboxClient.subscribe_requests == [
        ("sandbox-1", 2, False),
        ("sandbox-1", 5, False),
        ("sandbox-1", 8, False),
    ]


def test_agents_sandbox_client_waits_for_exec_terminal_with_replay_dedupe(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, str, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._get_exec_calls = 0

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            assert request.command == ["echo", "hello"]
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> object:
            self._get_exec_calls += 1
            state = service_pb2.EXEC_STATE_RUNNING
            exit_code = 0
            if self._get_exec_calls >= 2:
                state = service_pb2.EXEC_STATE_FINISHED
            return _exec_response(
                service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    exit_code=exit_code,
                ),
                last_event_sequence=10 if self._get_exec_calls == 1 else 11,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=10,
                event_type=service_pb2.EXEC_FINISHED,
                replay=True,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=11,
                event_type=service_pb2.EXEC_FINISHED,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> ExecHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.create_exec("sandbox-1", ("echo", "hello"), wait=True)

    exec_handle = asyncio.run(run_test())

    assert exec_handle.state is ExecState.FINISHED
    assert exec_handle.last_event_sequence == 11
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", 10, False)]


def test_cancel_exec_wait_paths_follow_post_baseline_exec_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, int, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._cancel_calls = 0
            self._mode = "cancel-false"
            self._true_exec_calls = 0

        def close(self) -> None:
            return None

        def cancel_exec(self, exec_id: str) -> service_pb2.AcceptedResponse:
            del exec_id
            self._mode = "cancel-false" if self._cancel_calls == 0 else "cancel-true"
            self._cancel_calls += 1
            return service_pb2.AcceptedResponse(accepted=True)

        def get_exec(self, exec_id: str) -> object:
            state = service_pb2.EXEC_STATE_RUNNING
            if self._mode == "cancel-true":
                self._true_exec_calls += 1
                if self._true_exec_calls >= 2:
                    state = service_pb2.EXEC_STATE_FINISHED
            return _exec_response(
                service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    exit_code=0,
                ),
                last_event_sequence=12 if self._mode != "cancel-true" or self._true_exec_calls < 2 else 13,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=12,
                event_type=service_pb2.EXEC_FINISHED,
                replay=True,
                exec_id="exec-2",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=13,
                event_type=service_pb2.EXEC_FINISHED,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=14,
                event_type=service_pb2.EXEC_FINISHED,
                replay=True,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> tuple[ExecHandle, ExecHandle]:
        client = _new_client("/tmp/agents-sandbox.sock")
        running = await client.cancel_exec("exec-1", wait=False)
        finished = await client.cancel_exec("exec-1", wait=True)
        return running, finished

    running, finished = asyncio.run(run_test())

    assert running.state is ExecState.RUNNING
    assert finished.state is ExecState.FINISHED
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", 12, False)]
