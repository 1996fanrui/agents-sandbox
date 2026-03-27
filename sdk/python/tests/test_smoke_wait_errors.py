from __future__ import annotations

import asyncio
from collections.abc import Iterator
import time

import agents_sandbox
import pytest
from agents_sandbox import SandboxClientError
from agents_sandbox._generated import service_pb2

from tests.smoke_support import _exec_response, _new_client


@pytest.mark.parametrize(
    ("sequence", "pattern"),
    [
        (0, "Sequence must be positive: 0"),
    ],
)
def test_create_exec_wait_true_requires_valid_last_event_sequence(
    monkeypatch: pytest.MonkeyPatch,
    sequence: int,
    pattern: str,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_calls = 0

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            del request
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> object:
            exec_status = service_pb2.ExecStatus(
                exec_id=exec_id,
                sandbox_id="sandbox-1",
                state=service_pb2.EXEC_STATE_RUNNING,
                command=["echo", "hello"],
                cwd="/workspace",
            )
            return _exec_response(exec_status, last_event_sequence=sequence)

        def subscribe_sandbox_events(self, *args, **kwargs):  # noqa: ANN002, ANN003
            del args, kwargs
            _FakeRawSandboxClient.subscribe_calls += 1
            raise AssertionError("subscribe should not start when the exec snapshot sequence is invalid")

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=True)

    with pytest.raises(ValueError, match=pattern):
        asyncio.run(run_test())

    assert _FakeRawSandboxClient.subscribe_calls == 0


def test_create_exec_wait_true_surfaces_stream_end_without_polling(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, int, bool]] = []
        get_exec_calls = 0

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            del request
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> object:
            _FakeRawSandboxClient.get_exec_calls += 1
            return _exec_response(
                service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=service_pb2.EXEC_STATE_RUNNING,
                    command=["echo", "hello"],
                    cwd="/workspace",
                ),
                last_event_sequence=10,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_sequence, include_current_snapshot))
            if False:
                yield service_pb2.SandboxEvent()

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=True)

    with pytest.raises(
        SandboxClientError,
        match="event stream ended before exec exec-1 reached a terminal state",
    ):
        asyncio.run(run_test())

    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", 10, False)]
    assert _FakeRawSandboxClient.get_exec_calls == 1


def test_create_exec_wait_true_surfaces_typed_stream_errors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            del request
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
                last_event_sequence=10,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            del sandbox_id, from_sequence, include_current_snapshot
            raise agents_sandbox.SandboxSequenceExpiredError("sequence expired")
            yield service_pb2.SandboxEvent()

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=True)

    with pytest.raises(agents_sandbox.SandboxSequenceExpiredError, match="sequence expired"):
        asyncio.run(run_test())


def test_create_exec_wait_true_times_out_without_event_driven_progress(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            del request
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
                last_event_sequence=10,
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_sequence: int = 0,
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            del sandbox_id, from_sequence, include_current_snapshot
            time.sleep(0.2)
            if False:
                yield service_pb2.SandboxEvent()

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        client._operation_timeout_seconds = 0.01
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=True)

    with pytest.raises(
        TimeoutError,
        match="create_exec timed out while waiting for exec exec-1 to become terminal",
    ):
        asyncio.run(run_test())
