from __future__ import annotations

import asyncio
from collections.abc import Iterator
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
import os
from pathlib import Path
import inspect
import signal
import shutil
import subprocess
import time

import grpc
import pytest
from google.protobuf.any_pb2 import Any
from google.protobuf.timestamp_pb2 import Timestamp
from google.rpc import code_pb2, error_details_pb2, status_pb2
from grpc_status import rpc_status

import agents_sandbox
from agents_sandbox import (
    AgentsSandboxClient,
    CopySpec,
    DependencySpec,
    ExecHandle,
    ExecState,
    MountSpec,
    ProjectionMountMode,
    ResolvedProjectionHandle,
    SandboxClientError,
    SandboxEvent,
    SandboxEventType,
    SandboxHandle,
    SandboxState,
)
from agents_sandbox._generated import service_pb2, service_pb2_grpc
from agents_sandbox.conversions import to_exec_handle
from agents_sandbox.client import _resolve_default_socket_path
from agents_sandbox.types import CallerMetadata


def test_package_root_exports_only_formal_client() -> None:
    exports = set(getattr(agents_sandbox, "__all__", []))

    assert "AgentsSandboxClient" in exports
    assert sorted(name for name in exports if name.endswith("SandboxClient")) == ["AgentsSandboxClient"]
    assert "CreateSandboxRequest" not in exports
    assert "CreateExecRequest" not in exports
    assert "SandboxOwner" not in exports


def test_public_models_match_protocol_contract() -> None:
    dependency = DependencySpec(
        name="postgres",
        image="postgres:16",
        network_alias="db",
    )
    mount = MountSpec(source="/tmp/workspace", target="/workspace", writable=True)
    copy = CopySpec(source="/tmp/source", target="/workspace/source", exclude_patterns=(".git",))

    assert dependency.name == "postgres"
    assert mount.target == "/workspace"
    assert copy.exclude_patterns == (".git",)
    assert "payload" not in SandboxEvent.__annotations__
    assert "created_at" not in SandboxHandle.__annotations__
    assert "updated_at" not in SandboxHandle.__annotations__
    assert "active_exec_ids" not in SandboxHandle.__annotations__
    assert "created_at" not in ExecHandle.__annotations__
    assert "updated_at" not in ExecHandle.__annotations__
    assert "stdout" in ExecHandle.__annotations__
    assert "stderr" in ExecHandle.__annotations__
    assert "aliases" not in DependencySpec.__annotations__
    assert "network_alias" in DependencySpec.__annotations__
    assert "last_event_cursor" in SandboxHandle.__annotations__
    assert "resolved_tooling_projections" in SandboxHandle.__annotations__


def test_caller_metadata_rejects_protocol_unsupported_extra_field() -> None:
    with pytest.raises(TypeError):
        CallerMetadata(product="p", session_id="s", run_id="r", extra={"k": "v"})


def test_sdk_exports_proto_backed_public_enums() -> None:
    assert SandboxState(service_pb2.SANDBOX_STATE_READY) is SandboxState.READY
    assert ExecState(service_pb2.EXEC_STATE_FINISHED) is ExecState.FINISHED
    assert ExecState.FINISHED.is_terminal is True
    assert ExecState.RUNNING.is_terminal is False
    assert SandboxEventType(service_pb2.EXEC_FINISHED) is SandboxEventType.EXEC_FINISHED
    assert ProjectionMountMode(service_pb2.PROJECTION_MOUNT_MODE_BIND) is ProjectionMountMode.BIND


def test_to_exec_handle_preserves_stdout_and_stderr() -> None:
    running = to_exec_handle(
        service_pb2.ExecStatus(
            exec_id="exec-running",
            sandbox_id="sandbox-1",
            state=service_pb2.EXEC_STATE_RUNNING,
            command=["echo", "hello"],
            cwd="/workspace",
            stdout="partial",
            stderr="warn",
            exit_code=0,
        )
    )
    finished = to_exec_handle(
        service_pb2.ExecStatus(
            exec_id="exec-finished",
            sandbox_id="sandbox-1",
            state=service_pb2.EXEC_STATE_FINISHED,
            command=["echo", "hello"],
            cwd="/workspace",
            stdout="done",
            stderr="",
            exit_code=7,
        )
    )

    assert running.stdout == "partial"
    assert running.stderr == "warn"
    assert running.exit_code is None
    assert finished.stdout == "done"
    assert finished.stderr is None
    assert finished.exit_code == 7


def test_default_socket_path_resolution_matches_daemon_rules(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("AGBOX_SOCKET", raising=False)
    assert _resolve_default_socket_path(
        system="Linux",
        lookup_env=lambda key: { "XDG_RUNTIME_DIR": "/tmp/runtime" }.get(key),
    ) == "/tmp/runtime/agbox/agboxd.sock"
    assert _resolve_default_socket_path(
        system="Darwin",
        lookup_env=lambda _key: None,
        home_dir="/Users/tester",
    ) == "/Users/tester/Library/Application Support/agbox/run/agboxd.sock"
    assert _resolve_default_socket_path(
        system="Linux",
        lookup_env=lambda key: { "AGBOX_SOCKET": "/tmp/from-env.sock" }.get(key),
    ) == "/tmp/from-env.sock"


def test_agents_sandbox_client_signatures_match_public_contract() -> None:
    expected = {
        "ping",
        "create_sandbox",
        "get_sandbox",
        "list_sandboxes",
        "subscribe_sandbox_events",
        "resume_sandbox",
        "stop_sandbox",
        "delete_sandbox",
        "create_exec",
        "run",
        "cancel_exec",
        "get_exec",
        "list_active_execs",
    }
    actual = {
        name
        for name, value in inspect.getmembers(AgentsSandboxClient)
        if callable(value) and not name.startswith("_")
    }

    assert expected - actual == set()
    assert {name for name in actual - expected if name != "close"} == set()

    defaults = {
        "create_sandbox": True,
        "resume_sandbox": True,
        "stop_sandbox": True,
        "delete_sandbox": True,
        "create_exec": False,
        "cancel_exec": True,
    }
    for method_name, expected_wait in defaults.items():
        signature = inspect.signature(getattr(AgentsSandboxClient, method_name))
        assert signature.parameters["wait"].default is expected_wait

    create_signature = inspect.signature(AgentsSandboxClient.create_sandbox)
    assert list(create_signature.parameters) == [
        "self",
        "image",
        "sandbox_owner",
        "mounts",
        "copies",
        "builtin_resources",
        "dependencies",
        "wait",
    ]
    assert "request" not in create_signature.parameters
    assert "socket_path" not in inspect.signature(AgentsSandboxClient).parameters
    list_signature = inspect.signature(AgentsSandboxClient.list_sandboxes)
    assert list(list_signature.parameters) == ["self", "include_deleted"]
    exec_signature = inspect.signature(AgentsSandboxClient.create_exec)
    assert list(exec_signature.parameters) == ["self", "sandbox_id", "command", "cwd", "env_overrides", "wait"]
    assert exec_signature.parameters["cwd"].default == "/workspace"
    assert exec_signature.parameters["env_overrides"].default is None
    run_signature = inspect.signature(AgentsSandboxClient.run)
    assert list(run_signature.parameters) == ["self", "sandbox_id", "command", "cwd", "env_overrides"]

    subscribe_signature = inspect.signature(AgentsSandboxClient.subscribe_sandbox_events)
    assert subscribe_signature.parameters["from_cursor"].default == "0"
    assert subscribe_signature.parameters["include_current_snapshot"].default is False


def test_public_docs_use_converged_python_sdk_api() -> None:
    repo_root = Path(__file__).resolve().parents[3]
    public_docs = {
        "README.md": (repo_root / "README.md").read_text(),
        "docs/sdk_async_usage.md": (repo_root / "docs" / "sdk_async_usage.md").read_text(),
        "examples/codex-cli/README.md": (repo_root / "examples" / "codex-cli" / "README.md").read_text(),
    }

    banned_tokens = (
        "CreateSandboxRequest",
        "CreateExecRequest",
        "SandboxOwner",
        'AgentsSandboxClient("/',
        "AgentsSandboxClient('/",
    )

    for relative_path, content in public_docs.items():
        for token in banned_tokens:
            assert token not in content, f"{relative_path} should not mention {token}"

    assert "AgentsSandboxClient()" in public_docs["README.md"]
    assert "AgentsSandboxClient()" in public_docs["docs/sdk_async_usage.md"]
    assert "AgentsSandboxClient()" in public_docs["examples/codex-cli/README.md"]
    assert "from_cursor=\"0\"" in public_docs["docs/sdk_async_usage.md"]
    assert "cursor" in public_docs["docs/sdk_async_usage.md"]
    assert "sequence" in public_docs["docs/sdk_async_usage.md"]


def test_public_async_client_round_trips_over_unix_socket(tmp_path: Path) -> None:
    servicer = _RecordingSandboxService()

    with _running_server(tmp_path / "sandbox.sock", servicer):
        result = asyncio.run(_exercise_public_client(tmp_path / "sandbox.sock"))

    assert result["ping"].daemon == "agboxd"
    assert result["sandbox_state"] is SandboxState.READY
    assert result["exec_state"] is ExecState.FINISHED
    assert result["event_types"] == [
        SandboxEventType.SANDBOX_READY,
        SandboxEventType.EXEC_FINISHED,
    ]
    assert servicer.subscribe_requests[0].from_cursor == "0"
    assert servicer.create_requests[0].create_spec.image == "python:3.12-slim"
    assert servicer.create_requests[0].sandbox_owner == "owner-1"
    assert servicer.create_exec_requests[0].cwd == "/workspace"


def test_create_sandbox_sandbox_owner_serializes_explicit_and_generated_owner(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        create_requests: list[service_pb2.CreateSandboxRequest] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            self.create_requests.append(request)
            return service_pb2.CreateSandboxResponse(
                sandbox_id=f"sandbox-{len(self.create_requests)}",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor=f"{sandbox_id}:1",
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)
    monkeypatch.setattr("agents_sandbox.client.uuid.uuid4", lambda: "generated-owner")

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox("python:3.12-slim", sandbox_owner="owner-1")
        await client.create_sandbox("python:3.12-slim")

    asyncio.run(run_test())

    assert _FakeRawSandboxClient.create_requests[0].sandbox_owner == "owner-1"
    assert _FakeRawSandboxClient.create_requests[1].sandbox_owner == "generated-owner"


def test_create_sandbox_mounts_copies_and_builtin_resources_serialize_to_proto(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        create_requests: list[service_pb2.CreateSandboxRequest] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            self.create_requests.append(request)
            return service_pb2.CreateSandboxResponse(
                sandbox_id="sandbox-1",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor=f"{sandbox_id}:1",
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox(
            "python:3.12-slim",
            mounts=(
                MountSpec(source="/host/workspace", target="/workspace", writable=True),
            ),
            copies=(
                CopySpec(
                    source="/host/template",
                    target="/workspace/template",
                    exclude_patterns=(".git", "__pycache__"),
                ),
            ),
            builtin_resources=(".claude", "uv"),
        )

    asyncio.run(run_test())

    create_spec = _FakeRawSandboxClient.create_requests[0].create_spec
    assert list(create_spec.builtin_resources) == [".claude", "uv"]
    assert create_spec.mounts == [
        service_pb2.MountSpec(source="/host/workspace", target="/workspace", writable=True)
    ]
    assert create_spec.copies == [
        service_pb2.CopySpec(
            source="/host/template",
            target="/workspace/template",
            exclude_patterns=[".git", "__pycache__"],
        )
    ]


def test_create_exec_cwd_and_env_overrides_default_to_workspace_and_empty_map(
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

        def get_exec(self, exec_id: str) -> service_pb2.GetExecResponse:
            return service_pb2.GetExecResponse(
                exec=service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=service_pb2.EXEC_STATE_RUNNING,
                    command=["echo", "hello"],
                    cwd="/workspace",
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=False)

    asyncio.run(run_test())

    request = _FakeRawSandboxClient.create_exec_requests[0]
    assert request.cwd == "/workspace"
    assert list(request.env_overrides) == []


def test_run_waits_and_returns_stdout(monkeypatch: pytest.MonkeyPatch) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, str, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._get_exec_calls = 0

        def close(self) -> None:
            return None

        def create_exec(self, request: service_pb2.CreateExecRequest) -> service_pb2.CreateExecResponse:
            assert request.cwd == "/workspace"
            return service_pb2.CreateExecResponse(exec_id="exec-1")

        def get_exec(self, exec_id: str) -> service_pb2.GetExecResponse:
            self._get_exec_calls += 1
            state = service_pb2.EXEC_STATE_RUNNING
            stdout = ""
            if self._get_exec_calls > 1:
                state = service_pb2.EXEC_STATE_FINISHED
                stdout = "hello"
            return service_pb2.GetExecResponse(
                exec=service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    stdout=stdout,
                    exit_code=0,
                )
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:10",
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_cursor: str = "",
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_cursor, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=11,
                cursor="sandbox-1:11",
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
    assert exec_handle.stdout == "hello"
    assert _FakeRawSandboxClient.subscribe_requests == []


def test_agents_sandbox_client_wait_true_ignores_replayed_old_events(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, str, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._get_sandbox_calls = 0

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            assert request.create_spec.image == "python:3.12-slim"
            return service_pb2.CreateSandboxResponse(
                sandbox_id="sandbox-1",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            self._get_sandbox_calls += 1
            state = service_pb2.SANDBOX_STATE_PENDING
            cursor = "sandbox-1:5"
            if self._get_sandbox_calls > 1:
                state = service_pb2.SANDBOX_STATE_READY
                cursor = "sandbox-1:6"
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=state,
                    last_event_cursor=cursor,
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_cursor: str = "",
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_cursor, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=4,
                cursor="sandbox-1:4",
                event_type=service_pb2.SANDBOX_READY,
                replay=True,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=5,
                cursor="sandbox-1:5",
                event_type=service_pb2.SANDBOX_READY,
                replay=True,
                snapshot=True,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=6,
                cursor="sandbox-1:6",
                event_type=service_pb2.SANDBOX_READY,
                sandbox_state=service_pb2.SANDBOX_STATE_READY,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> SandboxHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.create_sandbox("python:3.12-slim", sandbox_owner="owner-1", wait=True)

    sandbox = asyncio.run(run_test())

    assert sandbox.state is SandboxState.READY
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", "sandbox-1:5", False)]


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
            return service_pb2.CreateSandboxResponse(
                sandbox_id="sandbox-1",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:9",
                )
            )

        def subscribe_sandbox_events(self, *args, **kwargs):  # noqa: ANN002, ANN003
            del args, kwargs
            _FakeRawSandboxClient.subscribe_calls += 1
            raise AssertionError("subscribe should not run when the baseline is already terminal")

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> SandboxHandle:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.create_sandbox("python:3.12-slim", sandbox_owner="owner-1", wait=True)

    sandbox = asyncio.run(run_test())

    assert sandbox.state is SandboxState.READY
    assert _FakeRawSandboxClient.subscribe_calls == 0


def test_sandbox_lifecycle_wait_paths_cover_wait_false_and_wait_true(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, str, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._operation = ""
            self._resume_calls = 0
            self._stop_calls = 0
            self._delete_calls = 0
            self._sandbox_responses = {
                "resume-false": [
                    (service_pb2.SANDBOX_STATE_PENDING, "sandbox-1:1"),
                ],
                "resume-true": [
                    (service_pb2.SANDBOX_STATE_PENDING, "sandbox-1:2"),
                    (service_pb2.SANDBOX_STATE_READY, "sandbox-1:3"),
                ],
                "stop-false": [
                    (service_pb2.SANDBOX_STATE_READY, "sandbox-1:4"),
                ],
                "stop-true": [
                    (service_pb2.SANDBOX_STATE_READY, "sandbox-1:5"),
                    (service_pb2.SANDBOX_STATE_STOPPED, "sandbox-1:6"),
                ],
                "delete-false": [
                    (service_pb2.SANDBOX_STATE_DELETING, "sandbox-1:7"),
                ],
                "delete-true": [
                    (service_pb2.SANDBOX_STATE_DELETING, "sandbox-1:8"),
                    (service_pb2.SANDBOX_STATE_DELETED, "sandbox-1:9"),
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
            state, cursor = self._sandbox_responses[self._operation].pop(0)
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=state,
                    last_event_cursor=cursor,
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_cursor: str = "",
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_cursor, include_current_snapshot))
            if self._operation == "resume-true":
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=2,
                    cursor="sandbox-1:2",
                    event_type=service_pb2.SANDBOX_READY,
                    replay=True,
                    sandbox_state=service_pb2.SANDBOX_STATE_READY,
                )
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=3,
                    cursor="sandbox-1:3",
                    event_type=service_pb2.SANDBOX_READY,
                    sandbox_state=service_pb2.SANDBOX_STATE_READY,
                )
                return
            if self._operation == "stop-true":
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=5,
                    cursor="sandbox-1:5",
                    event_type=service_pb2.SANDBOX_STOPPED,
                    replay=True,
                    sandbox_state=service_pb2.SANDBOX_STATE_STOPPED,
                )
                yield _event_pb(
                    sandbox_id=sandbox_id,
                    sequence=6,
                    cursor="sandbox-1:6",
                    event_type=service_pb2.SANDBOX_STOPPED,
                    sandbox_state=service_pb2.SANDBOX_STATE_STOPPED,
                )
                return
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=8,
                cursor="sandbox-1:8",
                event_type=service_pb2.SANDBOX_DELETED,
                replay=True,
                sandbox_state=service_pb2.SANDBOX_STATE_DELETED,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=9,
                cursor="sandbox-1:9",
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
        ("sandbox-1", "sandbox-1:2", False),
        ("sandbox-1", "sandbox-1:5", False),
        ("sandbox-1", "sandbox-1:8", False),
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

        def get_exec(self, exec_id: str) -> service_pb2.GetExecResponse:
            self._get_exec_calls += 1
            state = service_pb2.EXEC_STATE_RUNNING
            exit_code = 0
            if self._get_exec_calls >= 3:
                state = service_pb2.EXEC_STATE_FINISHED
            return service_pb2.GetExecResponse(
                exec=service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    exit_code=exit_code,
                )
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:10",
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_cursor: str = "",
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_cursor, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=10,
                cursor="sandbox-1:10",
                event_type=service_pb2.EXEC_FINISHED,
                replay=True,
                exec_id="exec-1",
                exec_state=service_pb2.EXEC_STATE_FINISHED,
                exit_code=0,
            )
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=11,
                cursor="sandbox-1:11",
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
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", "sandbox-1:10", False)]


def test_cancel_exec_wait_paths_compensate_for_terminal_event_before_baseline(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _FakeRawSandboxClient:
        subscribe_requests: list[tuple[str, str, bool]] = []

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

        def get_exec(self, exec_id: str) -> service_pb2.GetExecResponse:
            state = service_pb2.EXEC_STATE_RUNNING
            if self._mode == "cancel-true":
                self._true_exec_calls += 1
                if self._true_exec_calls >= 3:
                    state = service_pb2.EXEC_STATE_FINISHED
            return service_pb2.GetExecResponse(
                exec=service_pb2.ExecStatus(
                    exec_id=exec_id,
                    sandbox_id="sandbox-1",
                    state=state,
                    command=["echo", "hello"],
                    cwd="/workspace",
                    exit_code=0,
                )
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:12",
                )
            )

        def subscribe_sandbox_events(
            self,
            sandbox_id: str,
            *,
            from_cursor: str = "",
            include_current_snapshot: bool = False,
        ) -> Iterator[service_pb2.SandboxEvent]:
            self.subscribe_requests.append((sandbox_id, from_cursor, include_current_snapshot))
            yield _event_pb(
                sandbox_id=sandbox_id,
                sequence=12,
                cursor="sandbox-1:12",
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
    assert _FakeRawSandboxClient.subscribe_requests == [("sandbox-1", "sandbox-1:12", False)]


@pytest.mark.parametrize(
    ("reason", "expected_type"),
    [
        ("SANDBOX_CONFLICT", agents_sandbox.SandboxConflictError),
        ("SANDBOX_NOT_FOUND", agents_sandbox.SandboxNotFoundError),
        ("SANDBOX_NOT_READY", agents_sandbox.SandboxNotReadyError),
        ("SANDBOX_INVALID_STATE", agents_sandbox.SandboxInvalidStateError),
        ("EXEC_NOT_FOUND", agents_sandbox.ExecNotFoundError),
        ("EXEC_ALREADY_TERMINAL", agents_sandbox.ExecAlreadyTerminalError),
        ("SANDBOX_EVENT_CURSOR_EXPIRED", agents_sandbox.SandboxCursorExpiredError),
    ],
)
def test_sdk_maps_error_info_reasons_to_public_exceptions(
    tmp_path: Path,
    reason: str,
    expected_type: type[Exception],
) -> None:
    servicer = _ErrorSandboxService(reason=reason)

    with _running_server(tmp_path / f"{reason.lower()}.sock", servicer):
        async def run_test() -> None:
            client = _new_client(tmp_path / f"{reason.lower()}.sock")
            if reason.startswith("EXEC_"):
                await client.cancel_exec("exec-1")
            elif reason == "SANDBOX_EVENT_CURSOR_EXPIRED":
                async for _event in client.subscribe_sandbox_events("sandbox-1"):
                    raise AssertionError("event stream should fail before yielding")
            else:
                await client.create_sandbox("python:3.12-slim", sandbox_owner="owner-1")

        with pytest.raises(expected_type):
            asyncio.run(run_test())


def test_sdk_can_ping_real_agents_sandbox_over_temp_socket(tmp_path: Path) -> None:
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real AgentsSandbox smoke test")

    socket_path = tmp_path / "agboxd.sock"
    with _running_test_daemon(repo_root, socket_path):
        asyncio.run(_wait_for_ping(socket_path))


async def _exercise_public_client(socket_path: Path) -> dict[str, object]:
    client = _new_client(socket_path, timeout_seconds=5.0, stream_timeout_seconds=5.0)
    ping = await client.ping()
    sandbox = await client.create_sandbox(
        "python:3.12-slim",
        sandbox_owner="owner-1",
        dependencies=(
            DependencySpec(
                name="postgres",
                image="postgres:16",
                network_alias="db",
            ),
        ),
        mounts=(
            MountSpec(source="/workspace", target="/workspace", writable=True),
        ),
        builtin_resources=(".claude",),
        wait=False,
    )
    sandboxes = await client.list_sandboxes()
    exec_handle = await client.create_exec(sandbox.sandbox_id, ("echo", "hello"), wait=False)
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
        "event_types": [event.event_type for event in events],
    }


@contextmanager
def _socket_env(socket_path: str | Path) -> Iterator[None]:
    previous = os.environ.get("AGBOX_SOCKET")
    os.environ["AGBOX_SOCKET"] = str(socket_path)
    try:
        yield
    finally:
        if previous is None:
            os.environ.pop("AGBOX_SOCKET", None)
        else:
            os.environ["AGBOX_SOCKET"] = previous


def _new_client(socket_path: str | Path, **kwargs: object) -> AgentsSandboxClient:
    with _socket_env(socket_path):
        return AgentsSandboxClient(**kwargs)


@contextmanager
def _running_test_daemon(
    repo_root: Path,
    socket_path: Path,
    *,
    env: dict[str, str] | None = None,
) -> Iterator[subprocess.Popen[str]]:
    process = subprocess.Popen(
        ["go", "run", "./cmd/agboxd", "--socket", str(socket_path)],
        cwd=repo_root,
        env=env,
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
                sandbox_owner="owner-1",
                state=service_pb2.SANDBOX_STATE_READY,
                resolved_tooling_projections=[
                    service_pb2.ResolvedProjectionHandle(
                        capability_id=".claude",
                        source_path="/tmp/source",
                        target_path="/home/agbox/.claude",
                        mount_mode=service_pb2.PROJECTION_MOUNT_MODE_BIND,
                        writable=True,
                        write_back=False,
                    )
                ],
                dependencies=[
                    service_pb2.DependencySpec(
                        dependency_name="postgres",
                        image="postgres:16",
                        network_alias="db",
                    )
                ],
                last_event_cursor="sandbox-1:2",
            )
        )

    def ListSandboxes(self, request, context):  # noqa: N802
        del context
        return service_pb2.ListSandboxesResponse(
            sandboxes=[
                service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    sandbox_owner="owner-1",
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:2",
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

    def SubscribeSandboxEvents(self, request, context):  # noqa: N802
        del context
        self.subscribe_requests.append(request)
        yield _event_pb(
            sandbox_id=request.sandbox_id,
            sequence=2,
            cursor="sandbox-1:2",
            event_type=service_pb2.SANDBOX_READY,
            replay=True,
            snapshot=True,
            sandbox_state=service_pb2.SANDBOX_STATE_READY,
        )
        yield _event_pb(
            sandbox_id=request.sandbox_id,
            sequence=3,
            cursor="sandbox-1:3",
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
    cursor: str,
    event_type: int,
    replay: bool = False,
    snapshot: bool = False,
    sandbox_state: int = service_pb2.SANDBOX_STATE_UNSPECIFIED,
    exec_state: int = service_pb2.EXEC_STATE_UNSPECIFIED,
    exec_id: str = "",
    exit_code: int = 0,
) -> service_pb2.SandboxEvent:
    return service_pb2.SandboxEvent(
        event_id=f"event-{sequence}",
        sequence=sequence,
        cursor=cursor,
        sandbox_id=sandbox_id,
        event_type=event_type,
        occurred_at=_timestamp(),
        replay=replay,
        snapshot=snapshot,
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
