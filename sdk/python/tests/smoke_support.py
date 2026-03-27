from __future__ import annotations

import asyncio
from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import os
from pathlib import Path
import signal
import subprocess
import time

import grpc
from google.protobuf.any_pb2 import Any
from google.protobuf.timestamp_pb2 import Timestamp
from google.rpc import code_pb2, error_details_pb2, status_pb2
from grpc_status import rpc_status

import agents_sandbox.client as client_module
from agents_sandbox import (
    AgentsSandboxClient,
    HealthcheckConfig,
    MountSpec,
    SandboxEvent,
    ServiceSpec,
)
from agents_sandbox._generated import service_pb2, service_pb2_grpc


def _joined_name(*parts: str) -> str:
    return "".join(parts)


def _underscored_name(*parts: str) -> str:
    return "_".join(parts)


def _legacy_sdk_type_names() -> tuple[str, ...]:
    return (
        _joined_name("Dependency", "Spec"),
        _joined_name("Workspace", "Materialization", "Spec"),
        _joined_name("Cache", "Projection", "Spec"),
        _joined_name("Tooling", "Projection", "Spec"),
        _joined_name("Resolved", "Projection", "Handle"),
        _joined_name("Projection", "Mount", "Mode"),
        _joined_name("Workspace", "Materialization", "Mode"),
    )


def _exec_response(
    exec_status: service_pb2.ExecStatus,
    *,
    last_event_sequence: int,
) -> service_pb2.GetExecResponse:
    exec_status.last_event_sequence = last_event_sequence
    return service_pb2.GetExecResponse(exec=exec_status)


async def _exercise_public_client(socket_path: Path) -> dict[str, object]:
    client = _new_client(socket_path, timeout_seconds=5.0, stream_timeout_seconds=5.0)
    ping = await client.ping()
    sandbox = await client.create_sandbox(
        "python:3.12-slim",
        sandbox_id="sandbox-1",
        required_services=(
            ServiceSpec(
                name="postgres",
                image="postgres:16",
                environment={"POSTGRES_DB": "agents"},
                healthcheck=HealthcheckConfig(
                    test=("CMD-SHELL", "pg_isready -U postgres"),
                    interval="5s",
                    retries=5,
                ),
                post_start_on_primary=("python", "-c", "print('seeded')"),
            ),
        ),
        optional_services=(
            ServiceSpec(
                name="redis",
                image="redis:7",
            ),
        ),
        mounts=(
            MountSpec(source="/workspace", target="/workspace", writable=True),
        ),
        builtin_resources=(".claude",),
        labels={"team": "sdk", "purpose": "smoke"},
        wait=False,
    )
    sandboxes = await client.list_sandboxes(label_selector={"team": "sdk"})
    delete_result = await client.delete_sandboxes({"team": "sdk"}, wait=False)
    exec_handle = await client.create_exec(
        sandbox.sandbox_id,
        ("echo", "hello"),
        exec_id="exec-1",
        wait=False,
    )
    events: list[SandboxEvent] = []
    async for event in client.subscribe_sandbox_events(sandbox.sandbox_id):
        events.append(event)
        if len(events) == 2:
            break
    assert len(sandboxes) == 1
    return {
        "ping": ping,
        "sandbox_state": sandbox.state,
        "exec_state": exec_handle.state,
        "exec_last_event_sequence": exec_handle.last_event_sequence,
        "deleted_count": delete_result.deleted_count,
        "event_types": [event.event_type for event in events],
        "service_names": [event.service_name for event in events],
    }


def _new_client(socket_path: str | Path, **kwargs: object) -> AgentsSandboxClient:
    client = AgentsSandboxClient(**kwargs)
    client.close()
    client.socket_path = str(socket_path)
    client._rpc_client = client_module.SandboxGrpcClient(
        str(socket_path),
        timeout_seconds=client._timeout_seconds,
    )
    return client


@contextmanager
def _running_test_daemon(
    repo_root: Path,
    socket_path: Path,
    *,
    env: dict[str, str] | None = None,
) -> Iterator[subprocess.Popen[str]]:
    runtime_dir = socket_path.parent.parent
    merged_env = os.environ.copy()
    merged_env["XDG_RUNTIME_DIR"] = str(runtime_dir)
    merged_env["HOME"] = str(runtime_dir.parent)
    if env is not None:
        merged_env.update(env)
    process = subprocess.Popen(
        ["go", "run", "./cmd/agboxd"],
        cwd=repo_root,
        env=merged_env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        text=True,
        start_new_session=True,
    )
    try:
        yield process
    finally:
        _terminate_process_group(process)


def _terminate_process_group(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return

    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return

    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            return
        process.wait(timeout=5)


async def _wait_for_ping(socket_path: Path) -> None:
    deadline = time.monotonic() + 5.0
    while time.monotonic() < deadline:
        if socket_path.exists():
            client = _new_client(socket_path)
            try:
                ping = await client.ping()
            except Exception:  # noqa: BLE001
                client.close()
                await asyncio.sleep(0.05)
                continue
            client.close()
            assert ping.daemon == "agboxd"
            return
        await asyncio.sleep(0.05)
    raise AssertionError("AgentsSandbox daemon did not become ready")


@contextmanager
def _running_server(
    socket_path: Path,
    servicer: service_pb2_grpc.SandboxServiceServicer,
) -> Iterator[None]:
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
        self.list_requests: list[service_pb2.ListSandboxesRequest] = []
        self.delete_sandboxes_requests: list[service_pb2.DeleteSandboxesRequest] = []
        self.subscribe_requests: list[service_pb2.SubscribeSandboxEventsRequest] = []
        self.create_exec_requests: list[service_pb2.CreateExecRequest] = []

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
                state=service_pb2.SANDBOX_STATE_READY,
                required_services=[
                    service_pb2.ServiceSpec(
                        name="postgres",
                        image="postgres:16",
                        environment=[service_pb2.KeyValue(key="POSTGRES_DB", value="agents")],
                        healthcheck=service_pb2.HealthcheckConfig(
                            test=["CMD-SHELL", "pg_isready -U postgres"],
                            interval="5s",
                            retries=5,
                        ),
                        post_start_on_primary=["python", "-c", "print('seeded')"],
                    )
                ],
                optional_services=[
                    service_pb2.ServiceSpec(
                        name="redis",
                        image="redis:7",
                    )
                ],
                labels={"team": "sdk", "purpose": "smoke"},
                last_event_sequence=2,
            )
        )

    def ListSandboxes(self, request, context):  # noqa: N802
        del context
        self.list_requests.append(request)
        return service_pb2.ListSandboxesResponse(
            sandboxes=[
                service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    labels={"team": "sdk", "purpose": "smoke"},
                    last_event_sequence=2,
                )
            ]
        )

    def ResumeSandbox(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.AcceptedResponse(accepted=True)

    def StopSandbox(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.AcceptedResponse(accepted=True)

    def DeleteSandbox(self, request, context):  # noqa: N802
        del request, context
        return service_pb2.AcceptedResponse(accepted=True)

    def DeleteSandboxes(self, request, context):  # noqa: N802
        del context
        self.delete_sandboxes_requests.append(request)
        return service_pb2.DeleteSandboxesResponse(
            deleted_sandbox_ids=["sandbox-1"],
            deleted_count=1,
        )

    def SubscribeSandboxEvents(self, request, context):  # noqa: N802
        del context
        self.subscribe_requests.append(request)
        yield _event_pb(
            sandbox_id=request.sandbox_id,
            sequence=2,
            event_type=service_pb2.SANDBOX_SERVICE_READY,
            replay=True,
            snapshot=True,
            service_name="postgres",
        )
        yield _event_pb(
            sandbox_id=request.sandbox_id,
            sequence=3,
            event_type=service_pb2.EXEC_FINISHED,
            exec_id="exec-1",
            exec_state=service_pb2.EXEC_STATE_FINISHED,
            exit_code=0,
        )

    def CreateExec(self, request, context):  # noqa: N802
        del context
        self.create_exec_requests.append(request)
        return service_pb2.CreateExecResponse(exec_id="exec-1")

    def CancelExec(self, request, context):  # noqa: N802
        del request, context
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
                last_event_sequence=3,
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

    def CreateExec(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))

    def CancelExec(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))

    def SubscribeSandboxEvents(self, request, context):  # noqa: N802
        del request
        context.abort_with_status(_rich_status(self._reason))
        yield service_pb2.SandboxEvent()


def _event_pb(
    *,
    sandbox_id: str,
    sequence: int,
    event_type: int,
    replay: bool = False,
    snapshot: bool = False,
    sandbox_state: int = service_pb2.SANDBOX_STATE_UNSPECIFIED,
    exec_state: int = service_pb2.EXEC_STATE_UNSPECIFIED,
    service_name: str = "",
    exec_id: str = "",
    exit_code: int = 0,
) -> service_pb2.SandboxEvent:
    return service_pb2.SandboxEvent(
        event_id=f"event-{sequence}",
        sequence=sequence,
        sandbox_id=sandbox_id,
        event_type=event_type,
        occurred_at=_timestamp(),
        replay=replay,
        snapshot=snapshot,
        service_name=service_name,
        exec_id=exec_id,
        exit_code=exit_code,
        sandbox_state=sandbox_state,
        exec_state=exec_state,
    )


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
