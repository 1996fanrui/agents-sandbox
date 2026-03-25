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
import agents_sandbox.client as client_module
from agents_sandbox import (
    AgentsSandboxClient,
    CopySpec,
    DeleteSandboxesResult,
    ExecHandle,
    ExecState,
    HealthcheckConfig,
    MountSpec,
    SandboxClientError,
    SandboxEvent,
    SandboxEventType,
    SandboxHandle,
    SandboxState,
    ServiceSpec,
)
from agents_sandbox._generated import service_pb2, service_pb2_grpc
from agents_sandbox.conversions import to_exec_handle, to_sandbox_handle
from agents_sandbox.client import _resolve_default_socket_path
from agents_sandbox.types import CallerMetadata


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


def test_package_root_exports_only_formal_client() -> None:
    exports = set(getattr(agents_sandbox, "__all__", []))

    assert "AgentsSandboxClient" in exports
    assert "DeleteSandboxesResult" in exports
    assert sorted(name for name in exports if name.endswith("SandboxClient")) == ["AgentsSandboxClient"]
    assert "CreateSandboxRequest" not in exports
    assert "CreateExecRequest" not in exports
    assert "SandboxOwner" not in exports


def test_public_models_match_protocol_contract() -> None:
    service = ServiceSpec(
        name="postgres",
        image="postgres:16",
        environment={"POSTGRES_DB": "agents"},
        healthcheck=HealthcheckConfig(
            test=("CMD-SHELL", "pg_isready -U postgres"),
            interval="5s",
            retries=3,
        ),
        post_start_on_primary=("python", "-c", "print('seeded')"),
    )
    mount = MountSpec(source="/tmp/workspace", target="/workspace", writable=True)
    copy = CopySpec(source="/tmp/source", target="/workspace/source", exclude_patterns=(".git",))

    assert service.name == "postgres"
    assert service.environment["POSTGRES_DB"] == "agents"
    assert service.healthcheck is not None
    assert service.healthcheck.test == ("CMD-SHELL", "pg_isready -U postgres")
    assert mount.target == "/workspace"
    assert copy.exclude_patterns == (".git",)
    assert "payload" not in SandboxEvent.__annotations__
    assert "service_name" in SandboxEvent.__annotations__
    assert _underscored_name("dependency", "name") not in SandboxEvent.__annotations__
    assert "created_at" not in SandboxHandle.__annotations__
    assert "updated_at" not in SandboxHandle.__annotations__
    assert "active_exec_ids" not in SandboxHandle.__annotations__
    assert "created_at" not in ExecHandle.__annotations__
    assert "updated_at" not in ExecHandle.__annotations__
    assert "stdout" in ExecHandle.__annotations__
    assert "stderr" in ExecHandle.__annotations__
    assert "last_event_cursor" in SandboxHandle.__annotations__
    assert "required_services" in SandboxHandle.__annotations__
    assert "optional_services" in SandboxHandle.__annotations__
    assert "labels" in SandboxHandle.__annotations__
    assert "dependencies" not in SandboxHandle.__annotations__
    assert "resolved_tooling_projections" not in SandboxHandle.__annotations__
    assert DeleteSandboxesResult.__annotations__ == {
        "deleted_sandbox_ids": "tuple[str, ...]",
        "deleted_count": "int",
    }


def test_caller_metadata_rejects_protocol_unsupported_extra_field() -> None:
    with pytest.raises(TypeError):
        CallerMetadata(product="p", session_id="s", run_id="r", extra={"k": "v"})


def test_sdk_exports_proto_backed_public_enums() -> None:
    assert SandboxState(service_pb2.SANDBOX_STATE_READY) is SandboxState.READY
    assert ExecState(service_pb2.EXEC_STATE_FINISHED) is ExecState.FINISHED
    assert ExecState.FINISHED.is_terminal is True
    assert ExecState.RUNNING.is_terminal is False
    assert SandboxEventType(service_pb2.EXEC_FINISHED) is SandboxEventType.EXEC_FINISHED
    assert (
        SandboxEventType(service_pb2.SANDBOX_SERVICE_READY)
        is SandboxEventType.SANDBOX_SERVICE_READY
    )


def test_public_root_exports_remove_legacy_sdk_types() -> None:
    exports = set(getattr(agents_sandbox, "__all__", []))

    for legacy_name in _legacy_sdk_type_names():
        assert legacy_name not in exports


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
    ignored_env_key = "UNRELATED_RUNTIME_SETTING"
    assert _resolve_default_socket_path(
        system="Linux",
        lookup_env=lambda key: {
            ignored_env_key: "/tmp/ignored.sock",
            "XDG_RUNTIME_DIR": "/tmp/runtime",
        }.get(key),
    ) == "/tmp/runtime/agbox/agboxd.sock"
    assert _resolve_default_socket_path(
        system="Darwin",
        lookup_env=lambda _key: None,
        home_dir="/Users/tester",
    ) == "/Users/tester/Library/Application Support/agbox/run/agboxd.sock"
    with pytest.raises(RuntimeError, match="XDG_RUNTIME_DIR"):
        _resolve_default_socket_path(
            system="Linux",
            lookup_env=lambda _key: None,
        )


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
        "delete_sandboxes",
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
        "sandbox_id",
        "mounts",
        "copies",
        "builtin_resources",
        "required_services",
        "optional_services",
        "labels",
        "wait",
    ]
    assert "request" not in create_signature.parameters
    assert "socket_path" not in inspect.signature(AgentsSandboxClient).parameters
    list_signature = inspect.signature(AgentsSandboxClient.list_sandboxes)
    assert list(list_signature.parameters) == ["self", "include_deleted", "label_selector"]
    delete_many_signature = inspect.signature(AgentsSandboxClient.delete_sandboxes)
    assert list(delete_many_signature.parameters) == ["self", "label_selector", "wait"]
    assert delete_many_signature.parameters["wait"].default is True
    exec_signature = inspect.signature(AgentsSandboxClient.create_exec)
    assert list(exec_signature.parameters) == ["self", "sandbox_id", "command", "exec_id", "cwd", "env_overrides", "wait"]
    assert exec_signature.parameters["cwd"].default == "/workspace"
    assert exec_signature.parameters["exec_id"].default is None
    assert exec_signature.parameters["env_overrides"].default is None
    run_signature = inspect.signature(AgentsSandboxClient.run)
    assert list(run_signature.parameters) == ["self", "sandbox_id", "command", "cwd", "env_overrides"]

    subscribe_signature = inspect.signature(AgentsSandboxClient.subscribe_sandbox_events)
    assert subscribe_signature.parameters["from_cursor"].default == "0"
    assert subscribe_signature.parameters["include_current_snapshot"].default is False


def test_public_docs_use_converged_python_sdk_api() -> None:
    repo_root = Path(__file__).resolve().parents[3]
    legacy_socket_token = "LEGACY_SOCKET_ENV"
    public_docs = {
        "README.md": (repo_root / "README.md").read_text(),
        "docs/sdk_async_usage.md": (repo_root / "docs" / "sdk_async_usage.md").read_text(),
        "examples/codex-cli/README.md": (repo_root / "examples" / "codex-cli" / "README.md").read_text(),
    }

    banned_tokens = (
        "CreateSandboxRequest",
        "CreateExecRequest",
        "SandboxOwner",
        *_legacy_sdk_type_names(),
        _underscored_name("SANDBOX", "DEPENDENCY", "READY"),
        'AgentsSandboxClient("/',
        "AgentsSandboxClient('/",
    )

    for relative_path, content in public_docs.items():
        for token in banned_tokens:
            assert token not in content, f"{relative_path} should not mention {token}"
        assert legacy_socket_token not in content, f"{relative_path} should not mention {legacy_socket_token}"

    assert "AgentsSandboxClient()" in public_docs["README.md"]
    assert "AgentsSandboxClient()" in public_docs["docs/sdk_async_usage.md"]
    assert "AgentsSandboxClient()" in public_docs["examples/codex-cli/README.md"]
    assert "ServiceSpec" in public_docs["docs/sdk_async_usage.md"]
    assert "HealthcheckConfig" in public_docs["docs/sdk_async_usage.md"]
    assert "required_services" in public_docs["docs/sdk_async_usage.md"]
    assert "optional_services" in public_docs["docs/sdk_async_usage.md"]
    assert "sandbox_id=" in public_docs["docs/sdk_async_usage.md"]
    assert "exec_id=" in public_docs["docs/sdk_async_usage.md"]
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
        SandboxEventType.SANDBOX_SERVICE_READY,
        SandboxEventType.EXEC_FINISHED,
    ]
    assert result["service_names"] == ["postgres", None]
    assert servicer.subscribe_requests[0].from_cursor == "0"
    assert servicer.create_requests[0].create_spec.image == "python:3.12-slim"
    assert servicer.create_requests[0].sandbox_id == "sandbox-1"
    assert dict(servicer.create_requests[0].create_spec.labels) == {"team": "sdk", "purpose": "smoke"}
    assert servicer.create_requests[0].create_spec.required_services == [
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
    ]
    assert servicer.create_requests[0].create_spec.optional_services == [
        service_pb2.ServiceSpec(
            name="redis",
            image="redis:7",
        )
    ]
    assert dict(servicer.list_requests[0].label_selector) == {"team": "sdk"}
    assert dict(servicer.delete_sandboxes_requests[0].label_selector) == {"team": "sdk"}
    assert servicer.create_exec_requests[0].cwd == "/workspace"
    assert result["deleted_count"] == 1


def test_create_sandbox_sandbox_id_serializes_explicit_and_omitted_values(monkeypatch: pytest.MonkeyPatch) -> None:
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
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor=f"{sandbox_id}:1",
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox("python:3.12-slim", sandbox_id="sandbox-explicit")
        await client.create_sandbox("python:3.12-slim")

    asyncio.run(run_test())

    assert _FakeRawSandboxClient.create_requests[0].sandbox_id == "sandbox-explicit"
    assert _FakeRawSandboxClient.create_requests[1].sandbox_id == ""


def test_create_sandbox_mounts_copies_builtin_resources_and_services_serialize_to_proto(
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
            required_services=(
                ServiceSpec(
                    name="postgres",
                    image="postgres:16",
                    environment={"POSTGRES_DB": "agents"},
                    healthcheck=HealthcheckConfig(
                        test=("CMD-SHELL", "pg_isready -U postgres"),
                        interval="5s",
                        retries=3,
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
    assert create_spec.required_services == [
        service_pb2.ServiceSpec(
            name="postgres",
            image="postgres:16",
            environment=[service_pb2.KeyValue(key="POSTGRES_DB", value="agents")],
            healthcheck=service_pb2.HealthcheckConfig(
                test=["CMD-SHELL", "pg_isready -U postgres"],
                interval="5s",
                retries=3,
            ),
            post_start_on_primary=["python", "-c", "print('seeded')"],
        )
    ]
    assert create_spec.optional_services == [
        service_pb2.ServiceSpec(
            name="redis",
            image="redis:7",
        )
    ]


def test_create_sandbox_with_labels_serializes_to_proto(monkeypatch: pytest.MonkeyPatch) -> None:
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
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_cursor="sandbox-1:1",
                    labels={"team": "sdk", "purpose": "test"},
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox(
            "python:3.12-slim",
            labels={"team": "sdk", "purpose": "test"},
        )

    asyncio.run(run_test())

    assert dict(_FakeRawSandboxClient.create_requests[0].create_spec.labels) == {"team": "sdk", "purpose": "test"}


def test_list_sandboxes_with_label_selector(monkeypatch: pytest.MonkeyPatch) -> None:
    class _FakeRawSandboxClient:
        list_requests: list[service_pb2.ListSandboxesRequest] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            return None

        def list_sandboxes(self, request: service_pb2.ListSandboxesRequest) -> service_pb2.ListSandboxesResponse:
            self.list_requests.append(request)
            return service_pb2.ListSandboxesResponse(
                sandboxes=[
                    service_pb2.SandboxHandle(
                        sandbox_id="sandbox-1",
                        state=service_pb2.SANDBOX_STATE_READY,
                        last_event_cursor="sandbox-1:1",
                        labels={"team": "sdk"},
                    )
                ]
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> list[SandboxHandle]:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.list_sandboxes(include_deleted=True, label_selector={"team": "sdk"})

    sandboxes = asyncio.run(run_test())

    assert _FakeRawSandboxClient.list_requests == [
        service_pb2.ListSandboxesRequest(include_deleted=True, label_selector={"team": "sdk"})
    ]
    assert sandboxes[0].labels == {"team": "sdk"}


def test_delete_sandboxes_waits_for_deleted_state(monkeypatch: pytest.MonkeyPatch) -> None:
    class _FakeRawSandboxClient:
        delete_requests: list[service_pb2.DeleteSandboxesRequest] = []
        subscribe_requests: list[tuple[str, str, bool]] = []

        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds
            self._sandbox_reads: dict[str, int] = {"sandbox-1": 0, "sandbox-2": 0}

        def close(self) -> None:
            return None

        def delete_sandboxes(self, request: service_pb2.DeleteSandboxesRequest) -> service_pb2.DeleteSandboxesResponse:
            self.delete_requests.append(request)
            return service_pb2.DeleteSandboxesResponse(
                deleted_sandbox_ids=["sandbox-1", "sandbox-2"],
                deleted_count=2,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            self._sandbox_reads[sandbox_id] += 1
            state = service_pb2.SANDBOX_STATE_DELETING
            cursor = f"{sandbox_id}:1"
            if self._sandbox_reads[sandbox_id] >= 2:
                state = service_pb2.SANDBOX_STATE_DELETED
                cursor = f"{sandbox_id}:2"
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
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
                sequence=2,
                cursor=f"{sandbox_id}:2",
                event_type=service_pb2.SANDBOX_DELETED,
                sandbox_state=service_pb2.SANDBOX_STATE_DELETED,
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> DeleteSandboxesResult:
        client = _new_client("/tmp/agents-sandbox.sock")
        return await client.delete_sandboxes({"team": "sdk"}, wait=True)

    result = asyncio.run(run_test())

    assert isinstance(result, DeleteSandboxesResult)
    assert result.deleted_sandbox_ids == ("sandbox-1", "sandbox-2")
    assert result.deleted_count == 2
    assert _FakeRawSandboxClient.delete_requests == [
        service_pb2.DeleteSandboxesRequest(label_selector={"team": "sdk"})
    ]
    assert _FakeRawSandboxClient.subscribe_requests == [
        ("sandbox-1", "sandbox-1:1", False),
        ("sandbox-2", "sandbox-2:1", False),
    ]


def test_sandbox_handle_labels_round_trip() -> None:
    handle = to_sandbox_handle(
        service_pb2.SandboxHandle(
            sandbox_id="sandbox-1",
            state=service_pb2.SANDBOX_STATE_READY,
            last_event_cursor="sandbox-1:1",
            labels={"team": "sdk"},
        )
    )

    assert handle.labels == {"team": "sdk"}


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
        await client.create_exec(
            "sandbox-1",
            ("echo", "hello"),
            exec_id="exec-explicit",
            cwd="/work",
            env_overrides={"HELLO": "world"},
            wait=False,
        )
        await client.create_exec("sandbox-1", ("echo", "hello"), wait=False)

    asyncio.run(run_test())

    explicit_request = _FakeRawSandboxClient.create_exec_requests[0]
    default_request = _FakeRawSandboxClient.create_exec_requests[1]
    assert explicit_request.exec_id == "exec-explicit"
    assert explicit_request.cwd == "/work"
    assert explicit_request.env_overrides == [
        service_pb2.KeyValue(key="HELLO", value="world")
    ]
    assert default_request.exec_id == ""
    assert default_request.cwd == "/workspace"
    assert list(default_request.env_overrides) == []


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
        return await client.create_sandbox("python:3.12-slim", sandbox_id="sandbox-1", wait=True)

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
        return await client.create_sandbox("python:3.12-slim", sandbox_id="sandbox-1", wait=True)

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
        ("EXEC_ID_ALREADY_EXISTS", agents_sandbox.SandboxConflictError),
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
            if reason in {"EXEC_NOT_FOUND", "EXEC_ALREADY_TERMINAL"}:
                await client.cancel_exec("exec-1")
            elif reason == "EXEC_ID_ALREADY_EXISTS":
                await client.create_exec("sandbox-1", ("echo",), exec_id="exec-1")
            elif reason == "SANDBOX_EVENT_CURSOR_EXPIRED":
                async for _event in client.subscribe_sandbox_events("sandbox-1"):
                    raise AssertionError("event stream should fail before yielding")
            else:
                await client.create_sandbox("python:3.12-slim", sandbox_id="sandbox-1")

        with pytest.raises(expected_type):
            asyncio.run(run_test())


def test_sdk_can_ping_real_agents_sandbox_over_temp_socket(tmp_path: Path) -> None:
    pytest.skip("real daemon fixed-path startup is covered by cmd package tests in stage 1")
    repo_root = Path(__file__).resolve().parents[3]
    if shutil.which("go") is None:
        pytest.skip("go is required for the real AgentsSandbox smoke test")

    socket_path = tmp_path / "runtime" / "agents-sandbox" / "agents-sandbox.sock"
    socket_path.parent.mkdir(parents=True, exist_ok=True)
    with _running_test_daemon(repo_root, socket_path):
        asyncio.run(_wait_for_ping(socket_path))


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
        "deleted_count": delete_result.deleted_count,
        "event_types": [event.event_type for event in events],
        "service_names": [event.service_name for event in events],
    }


def _new_client(socket_path: str | Path, **kwargs: object) -> AgentsSandboxClient:
    client = AgentsSandboxClient(**kwargs)
    client.close()
    client.socket_path = str(socket_path)
    client._rpc_client = client_module.SandboxGrpcClient(str(socket_path), timeout_seconds=client._timeout_seconds)
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
                last_event_cursor="sandbox-1:2",
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
            cursor="sandbox-1:2",
            event_type=service_pb2.SANDBOX_SERVICE_READY,
            replay=True,
            snapshot=True,
            service_name="postgres",
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
    cursor: str,
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
        cursor=cursor,
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
