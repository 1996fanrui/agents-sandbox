from __future__ import annotations

from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from pathlib import Path
import shutil
import subprocess
import time

import grpc
import pytest
from google.protobuf.any_pb2 import Any
from google.protobuf.timestamp_pb2 import Timestamp
from google.rpc import code_pb2, error_details_pb2, status_pb2
from grpc_status import rpc_status

from agents_sandbox import (
    ExecAlreadyTerminalError,
    ExecNotFoundError,
    SandboxClient,
    SandboxConflictError,
    SandboxCursorExpiredError,
    SandboxInvalidStateError,
    SandboxNotFoundError,
    SandboxNotReadyError,
)
from agents_sandbox._generated import service_pb2, service_pb2_grpc


def test_sdk_client_round_trips_over_unix_socket(tmp_path: Path) -> None:
    servicer = _RecordingSandboxService()

    with _running_server(tmp_path / "sandbox.sock", servicer):
        with SandboxClient(str(tmp_path / "sandbox.sock")) as client:
            ping = client.ping()
            assert ping.version == "0.1.0"
            assert ping.daemon == "agboxd"

            create_response = client.create_sandbox(
                service_pb2.CreateSandboxRequest(
                    sandbox_owner=service_pb2.SandboxOwner(
                        product="consumer",
                        owner_type="workspace",
                        owner_id="owner-1",
                    ),
                    create_spec=service_pb2.CreateSpec(
                        dependencies=[
                            service_pb2.DependencySpec(
                                dependency_name="postgres",
                                image="postgres:16",
                            )
                        ]
                    ),
                )
            )
            assert create_response.sandbox_id == "sandbox-1"
            assert create_response.initial_state == service_pb2.SANDBOX_STATE_PENDING
            assert servicer.create_requests[0].sandbox_owner.product == "consumer"

            sandbox = client.get_sandbox("sandbox-1")
            assert sandbox.sandbox.sandbox_id == "sandbox-1"
            assert sandbox.sandbox.state == service_pb2.SANDBOX_STATE_READY

            sandboxes = client.list_sandboxes(
                service_pb2.ListSandboxesRequest(
                    owner=service_pb2.SandboxOwner(
                        product="consumer",
                        owner_type="workspace",
                        owner_id="owner-1",
                    )
                )
            )
            assert [item.sandbox_id for item in sandboxes.sandboxes] == ["sandbox-1"]

            resumed = client.resume_sandbox("sandbox-1")
            stopped = client.stop_sandbox(
                "sandbox-1",
                action_reason=service_pb2.ACTION_REASON_CLEANUP_IDLE_SESSION,
                action_strategy=service_pb2.ACTION_STRATEGY_IDLE_SESSION_STOP,
            )
            deleted = client.delete_sandbox(
                "sandbox-1",
                action_reason=service_pb2.ACTION_REASON_CLEANUP_LEAKED_SESSION_RESOURCES,
                action_strategy=service_pb2.ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME,
            )
            assert resumed.accepted is True
            assert stopped.accepted is True
            assert deleted.accepted is True
            assert servicer.stop_requests[0].action_reason == service_pb2.ACTION_REASON_CLEANUP_IDLE_SESSION
            assert servicer.delete_requests[0].action_strategy == service_pb2.ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME

            events = list(
                client.subscribe_sandbox_events(
                    "sandbox-1",
                    from_cursor="sandbox-1:1",
                    include_current_snapshot=True,
                )
            )
            assert [event.event_type for event in events] == [
                service_pb2.SANDBOX_READY,
                service_pb2.EXEC_FINISHED,
            ]
            assert events[0].snapshot is True
            assert events[1].exec_id == "exec-1"
            assert servicer.subscribe_requests[0].from_cursor == "sandbox-1:1"
            assert servicer.subscribe_requests[0].include_current_snapshot is True

            create_exec = client.create_exec(
                service_pb2.CreateExecRequest(
                    sandbox_id="sandbox-1",
                    command=["echo", "hello"],
                    cwd="/workspace",
                )
            )
            started = client.start_exec(
                create_exec.exec_id,
                action_reason=service_pb2.ACTION_REASON_EXECUTE_RUN,
                action_strategy=service_pb2.ACTION_STRATEGY_START_RUN_EXEC,
            )
            cancelled = client.cancel_exec(
                create_exec.exec_id,
                action_reason=service_pb2.ACTION_REASON_EXECUTE_RUN,
                action_strategy=service_pb2.ACTION_STRATEGY_CANCEL_RUN_EXEC,
            )
            assert create_exec.exec_id == "exec-1"
            assert started.accepted is True
            assert cancelled.accepted is True
            assert servicer.start_requests[0].action_strategy == service_pb2.ACTION_STRATEGY_START_RUN_EXEC
            assert servicer.cancel_requests[0].action_strategy == service_pb2.ACTION_STRATEGY_CANCEL_RUN_EXEC

            exec_status = client.get_exec("exec-1")
            assert exec_status.exec.exec_id == "exec-1"
            assert exec_status.exec.state == service_pb2.EXEC_STATE_FINISHED

            active_execs = client.list_active_execs("sandbox-1")
            assert [item.exec_id for item in active_execs.execs] == ["exec-1"]


@pytest.mark.parametrize(
    ("reason", "expected_type"),
    [
        ("SANDBOX_CONFLICT", SandboxConflictError),
        ("SANDBOX_NOT_FOUND", SandboxNotFoundError),
        ("SANDBOX_NOT_READY", SandboxNotReadyError),
        ("SANDBOX_INVALID_STATE", SandboxInvalidStateError),
        ("EXEC_NOT_FOUND", ExecNotFoundError),
        ("EXEC_ALREADY_TERMINAL", ExecAlreadyTerminalError),
        ("SANDBOX_EVENT_CURSOR_EXPIRED", SandboxCursorExpiredError),
    ],
)
def test_sdk_maps_error_info_reasons_to_public_exceptions(
    tmp_path: Path,
    reason: str,
    expected_type: type[Exception],
) -> None:
    servicer = _ErrorSandboxService(reason=reason)

    with _running_server(tmp_path / f"{reason.lower()}.sock", servicer):
        with SandboxClient(str(tmp_path / f"{reason.lower()}.sock")) as client:
            with pytest.raises(expected_type):
                if reason.startswith("EXEC_"):
                    client.start_exec("exec-1")
                elif reason == "SANDBOX_EVENT_CURSOR_EXPIRED":
                    next(client.subscribe_sandbox_events("sandbox-1"))
                else:
                    client.create_sandbox(service_pb2.CreateSandboxRequest())


def test_sdk_exports_public_symbols_and_generated_proto() -> None:
    request = service_pb2.CreateSandboxRequest(
        sandbox_owner=service_pb2.SandboxOwner(
            product="consumer",
            owner_type="workspace",
            owner_id="owner-1",
        )
    )
    client = SandboxClient("/tmp/nonexistent.sock")
    client.close()

    assert request.sandbox_owner.product == "consumer"


def test_sdk_can_ping_real_agboxd_over_temp_socket(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real agboxd smoke test")

    socket_path = tmp_path / "agboxd.sock"
    process = subprocess.Popen(
        ["go", "run", "./cmd/agboxd", "--socket", str(socket_path)],
        cwd=repo_root,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    try:
        deadline = time.monotonic() + 5.0
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise AssertionError("real agboxd exited before becoming ready")
            if socket_path.exists():
                with SandboxClient(str(socket_path)) as client:
                    ping = client.ping()
                assert ping.daemon == "agboxd"
                return
            time.sleep(0.05)
        raise AssertionError("real agboxd did not become ready")
    finally:
        process.terminate()
        process.wait(timeout=5)


@contextmanager
def _running_server(socket_path: Path, servicer: service_pb2_grpc.SandboxServiceServicer) -> Iterator[None]:
    server = grpc.server(ThreadPoolExecutor(max_workers=4))
    service_pb2_grpc.add_SandboxServiceServicer_to_server(servicer, server)
    bound = server.add_insecure_port(f"unix://{socket_path}")
    if bound == 0:
        raise AssertionError(f"failed to bind unix socket: {socket_path}")
    server.start()
    try:
        yield
    finally:
        server.stop(grace=0).wait()


class _RecordingSandboxService(service_pb2_grpc.SandboxServiceServicer):
    def __init__(self) -> None:
        self.create_requests: list[service_pb2.CreateSandboxRequest] = []
        self.subscribe_requests: list[service_pb2.SubscribeSandboxEventsRequest] = []
        self.stop_requests: list[service_pb2.StopSandboxRequest] = []
        self.delete_requests: list[service_pb2.DeleteSandboxRequest] = []
        self.start_requests: list[service_pb2.StartExecRequest] = []
        self.cancel_requests: list[service_pb2.CancelExecRequest] = []

    def Ping(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.PingResponse(version="0.1.0", daemon="agboxd")

    def CreateSandbox(self, request, context):  # noqa: N802
        del context
        self.create_requests.append(request)
        return service_pb2.CreateSandboxResponse(
            sandbox_id="sandbox-1",
            initial_state=service_pb2.SANDBOX_STATE_PENDING,
        )

    def GetSandbox(self, request, context):  # noqa: N802
        del context
        return service_pb2.GetSandboxResponse(
            sandbox=service_pb2.SandboxHandle(
                sandbox_id=request.sandbox_id,
                owner=service_pb2.SandboxOwner(
                    product="consumer",
                    owner_type="workspace",
                    owner_id="owner-1",
                ),
                state=service_pb2.SANDBOX_STATE_READY,
                last_event_cursor="sandbox-1:2",
            )
        )

    def ListSandboxes(self, request, context):  # noqa: N802
        del context
        return service_pb2.ListSandboxesResponse(
            sandboxes=[
                service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    owner=request.owner,
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:2",
                )
            ]
        )

    def ResumeSandbox(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.AcceptedResponse(accepted=True)

    def StopSandbox(self, request, context):  # noqa: N802
        del context
        self.stop_requests.append(request)
        return service_pb2.AcceptedResponse(accepted=True)

    def DeleteSandbox(self, request, context):  # noqa: N802
        del context
        self.delete_requests.append(request)
        return service_pb2.AcceptedResponse(accepted=True)

    def SubscribeSandboxEvents(self, request, context):  # noqa: N802
        del context
        self.subscribe_requests.append(request)
        yield service_pb2.SandboxEvent(
            event_id="event-1",
            sequence=2,
            cursor="sandbox-1:2",
            sandbox_id=request.sandbox_id,
            event_type=service_pb2.SANDBOX_READY,
            occurred_at=_timestamp(),
            replay=True,
            snapshot=True,
            sandbox_state=service_pb2.SANDBOX_STATE_READY,
        )
        yield service_pb2.SandboxEvent(
            event_id="event-2",
            sequence=3,
            cursor="sandbox-1:3",
            sandbox_id=request.sandbox_id,
            event_type=service_pb2.EXEC_FINISHED,
            occurred_at=_timestamp(),
            exec_id="exec-1",
            exit_code=0,
            exec_state=service_pb2.EXEC_STATE_FINISHED,
        )

    def CreateExec(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.CreateExecResponse(exec_id="exec-1")

    def StartExec(self, request, context):  # noqa: N802
        del context
        self.start_requests.append(request)
        return service_pb2.AcceptedResponse(accepted=True)

    def CancelExec(self, request, context):  # noqa: N802
        del context
        self.cancel_requests.append(request)
        return service_pb2.AcceptedResponse(accepted=True)

    def GetExec(self, request, context):  # noqa: N802
        del context
        return service_pb2.GetExecResponse(
            exec=service_pb2.ExecStatus(
                exec_id=request.exec_id,
                sandbox_id="sandbox-1",
                state=service_pb2.EXEC_STATE_FINISHED,
                command=["echo", "hello"],
                cwd="/workspace",
                exit_code=0,
            )
        )

    def ListActiveExecs(self, request, context):  # noqa: N802
        del context
        return service_pb2.ListActiveExecsResponse(
            execs=[
                service_pb2.ExecStatus(
                    exec_id="exec-1",
                    sandbox_id=request.sandbox_id or "sandbox-1",
                    state=service_pb2.EXEC_STATE_RUNNING,
                    command=["echo", "hello"],
                    cwd="/workspace",
                )
            ]
        )


class _ErrorSandboxService(service_pb2_grpc.SandboxServiceServicer):
    def __init__(self, *, reason: str) -> None:
        self._reason = reason

    def CreateSandbox(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))

    def StartExec(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))

    def SubscribeSandboxEvents(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))
        yield service_pb2.SandboxEvent()


def _rich_status(reason: str) -> grpc.Status:
    error_info = error_details_pb2.ErrorInfo(reason=reason)
    status = status_pb2.Status(code=code_pb2.INVALID_ARGUMENT, message=reason)
    detail = Any()
    detail.Pack(error_info)
    status.details.append(detail)
    return rpc_status.to_status(status)


def _timestamp() -> Timestamp:
    timestamp = Timestamp()
    timestamp.GetCurrentTime()
    return timestamp
