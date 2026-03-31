from __future__ import annotations

import asyncio
from collections.abc import Iterator
from datetime import timedelta
import inspect
from pathlib import Path
from types import SimpleNamespace

import agents_sandbox
import pytest
from agents_sandbox import (
    AgentsSandboxClient,
    CopySpec,
    DeleteSandboxesResult,
    ExecHandle,
    ExecState,
    HealthcheckConfig,
    MountSpec,
    SandboxEvent,
    SandboxEventType,
    SandboxHandle,
    SandboxState,
    ServiceSpec,
)
from agents_sandbox._generated import service_pb2
from agents_sandbox.client import _resolve_default_socket_path
from agents_sandbox._conversions import to_exec_handle, to_exec_snapshot, to_sandbox_handle

from tests.smoke_support import (
    _RecordingSandboxService,
    _event_pb,
    _exercise_public_client,
    _exec_response,
    _legacy_sdk_type_names,
    _new_client,
    _running_server,
    _underscored_name,
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
        envs={"POSTGRES_DB": "agents"},
        healthcheck=HealthcheckConfig(
            test=("CMD-SHELL", "pg_isready -U postgres"),
            interval=timedelta(seconds=5),
            retries=3,
        ),
        post_start_on_primary=("python", "-c", "print('seeded')"),
    )
    mount = MountSpec(source="/tmp/workspace", target="/workspace", writable=True)
    copy = CopySpec(source="/tmp/source", target="/workspace/source", exclude_patterns=(".git",))

    assert service.name == "postgres"
    assert service.envs["POSTGRES_DB"] == "agents"
    assert service.healthcheck is not None
    assert service.healthcheck.test == ("CMD-SHELL", "pg_isready -U postgres")
    assert mount.target == "/workspace"
    assert copy.exclude_patterns == (".git",)
    assert set(SandboxEvent.__annotations__) == {
        "event_id",
        "sequence",
        "sandbox_id",
        "event_type",
        "occurred_at",
        "replay",
        "snapshot",
        "sandbox_state",
        "sandbox_phase",
        "exec",
        "service",
    }
    assert set(SandboxHandle.__annotations__) == {
        "sandbox_id",
        "state",
        "last_event_sequence",
        "required_services",
        "optional_services",
        "labels",
        "created_at",
        "image",
        "error_code",
        "error_message",
        "state_changed_at",
    }
    assert SandboxHandle.__annotations__["last_event_sequence"] == "int"
    assert set(ExecHandle.__annotations__) == {
        "exec_id",
        "sandbox_id",
        "state",
        "command",
        "cwd",
        "env_overrides",
        "exit_code",
        "error",
        "stdout_log_path",
        "stderr_log_path",
        "last_event_sequence",
    }
    assert ExecHandle.__annotations__["last_event_sequence"] == "int"
    assert DeleteSandboxesResult.__annotations__ == {
        "deleted_sandbox_ids": "tuple[str, ...]",
        "deleted_count": "int",
    }



def test_sdk_exports_proto_backed_public_enums() -> None:
    assert SandboxState(service_pb2.SANDBOX_STATE_READY) is SandboxState.READY
    assert ExecState(service_pb2.EXEC_STATE_FINISHED) is ExecState.FINISHED
    assert ExecState.FINISHED.is_terminal is True
    assert ExecState.RUNNING.is_terminal is False
    assert {
        service_pb2.SANDBOX_READY: "sandbox_ready",
        service_pb2.SANDBOX_FAILED: "sandbox_failed",
        service_pb2.EXEC_STARTED: "exec_started",
        service_pb2.EXEC_FINISHED: "exec_finished",
        service_pb2.EXEC_FAILED: "exec_failed",
        service_pb2.EXEC_CANCELLED: "exec_cancelled",
        service_pb2.SANDBOX_SERVICE_READY: "sandbox_service_ready",
        service_pb2.SANDBOX_SERVICE_FAILED: "sandbox_service_failed",
    } == {
        3: "sandbox_ready",
        4: "sandbox_failed",
        9: "exec_started",
        10: "exec_finished",
        11: "exec_failed",
        12: "exec_cancelled",
        13: "sandbox_service_ready",
        14: "sandbox_service_failed",
    }
    assert SandboxEventType(service_pb2.EXEC_FINISHED) is SandboxEventType.EXEC_FINISHED
    assert (
        SandboxEventType(service_pb2.SANDBOX_SERVICE_READY)
        is SandboxEventType.SANDBOX_SERVICE_READY
    )


def test_public_root_exports_remove_legacy_sdk_types() -> None:
    exports = set(getattr(agents_sandbox, "__all__", []))

    for legacy_name in _legacy_sdk_type_names():
        assert legacy_name not in exports


def test_to_exec_handle_maps_exit_code_and_sequence() -> None:
    running = to_exec_handle(
        service_pb2.ExecStatus(
            exec_id="exec-running",
            sandbox_id="sandbox-1",
            state=service_pb2.EXEC_STATE_RUNNING,
            command=["echo", "hello"],
            cwd="/workspace",
            exit_code=0,
            last_event_sequence=3,
        )
    )
    finished = to_exec_handle(
        service_pb2.ExecStatus(
            exec_id="exec-finished",
            sandbox_id="sandbox-1",
            state=service_pb2.EXEC_STATE_FINISHED,
            command=["echo", "hello"],
            cwd="/workspace",
            exit_code=7,
            last_event_sequence=7,
        )
    )

    assert running.exit_code is None  # not terminal, exit_code suppressed
    assert running.last_event_sequence == 3
    assert finished.exit_code == 7
    assert finished.last_event_sequence == 7


def test_to_exec_snapshot_requires_daemon_issued_sequence() -> None:
    valid_exec_status = service_pb2.ExecStatus(
        exec_id="exec-1",
        sandbox_id="sandbox-1",
        state=service_pb2.EXEC_STATE_RUNNING,
        command=["echo", "hello"],
        cwd="/workspace",
    )

    handle, sequence = to_exec_snapshot(_exec_response(valid_exec_status, last_event_sequence=7))

    assert handle.exec_id == "exec-1"
    assert sequence == 7

    zero_sequence_exec_status = service_pb2.ExecStatus(
        exec_id="exec-1",
        sandbox_id="sandbox-1",
        state=service_pb2.EXEC_STATE_RUNNING,
        command=["echo", "hello"],
        cwd="/workspace",
    )

    with pytest.raises(ValueError, match="Sequence must be positive: 0"):
        to_exec_snapshot(SimpleNamespace(exec=zero_sequence_exec_status))

    with pytest.raises(ValueError, match="Sequence must be positive: 0"):
        to_exec_snapshot(_exec_response(zero_sequence_exec_status, last_event_sequence=0))


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
    ) == "/Users/tester/Library/Application Support/agbox/agboxd.sock"
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
    assert {name for name in actual - expected if name not in {"close", "aclose"}} == set()

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
        "config_yaml",
        "image",
        "sandbox_id",
        "mounts",
        "copies",
        "builtin_tools",
        "required_services",
        "optional_services",
        "labels",
        "envs",
        "idle_ttl",
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
    assert subscribe_signature.parameters["from_sequence"].default == 0
    assert subscribe_signature.parameters["include_current_snapshot"].default is False


def test_public_docs_use_converged_python_sdk_api() -> None:
    repo_root = Path(__file__).resolve().parents[3]
    legacy_socket_token = "LEGACY_SOCKET_ENV"
    public_docs = {
        "README.md": (repo_root / "README.md").read_text(),
        "docs/sdk_python_usage.md": (repo_root / "docs" / "sdk_python_usage.md").read_text(),
        "examples/codex/README.md": (repo_root / "examples" / "codex" / "README.md").read_text(),
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
    assert "AgentsSandboxClient()" in public_docs["docs/sdk_python_usage.md"]
    assert "ServiceSpec" in public_docs["docs/sdk_python_usage.md"]
    assert "HealthcheckConfig" in public_docs["docs/sdk_python_usage.md"]
    assert "required_services" in public_docs["docs/sdk_python_usage.md"]
    assert "optional_services" in public_docs["docs/sdk_python_usage.md"]
    assert "sandbox_id=" in public_docs["docs/sdk_python_usage.md"]
    assert "exec_id=" in public_docs["docs/sdk_python_usage.md"]
    assert "from_sequence=0" in public_docs["docs/sdk_python_usage.md"]
    assert "last_event_sequence" in public_docs["docs/sdk_python_usage.md"]
    assert "sequence" in public_docs["docs/sdk_python_usage.md"]


def test_public_async_client_round_trips_over_unix_socket(tmp_path: Path) -> None:
    servicer = _RecordingSandboxService()

    with _running_server("sandbox.sock", servicer) as socket_path:
        result = asyncio.run(_exercise_public_client(socket_path))

    assert result["ping"].daemon == "agboxd"
    # create_sandbox with wait=False returns state from CreateSandboxResponse (PENDING).
    assert result["sandbox_state"] is SandboxState.PENDING
    assert result["exec_state"] is ExecState.FINISHED
    assert result["event_types"] == [
        SandboxEventType.SANDBOX_SERVICE_READY,
        SandboxEventType.EXEC_FINISHED,
    ]
    assert result["service_names"] == ["postgres", None]
    assert result["exec_last_event_sequence"] == 3
    assert servicer.subscribe_requests[0].from_sequence == 0
    assert servicer.create_requests[0].create_spec.image == "python:3.12-slim"
    assert servicer.create_requests[0].sandbox_id == "sandbox-1"
    assert dict(servicer.create_requests[0].create_spec.labels) == {"team": "sdk", "purpose": "smoke"}
    # envs are serialized as a map in the proto.
    assert dict(servicer.create_requests[0].create_spec.required_services[0].envs) == {"POSTGRES_DB": "agents"}
    assert servicer.create_requests[0].create_spec.required_services[0].name == "postgres"
    assert servicer.create_requests[0].create_spec.required_services[0].image == "postgres:16"
    assert servicer.create_requests[0].create_spec.optional_services[0].name == "redis"
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
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=f"sandbox-{len(self.create_requests)}",
                    state=service_pb2.SANDBOX_STATE_PENDING,
                    last_event_sequence=1,
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox(image="python:3.12-slim", sandbox_id="sandbox-explicit", wait=False)
        await client.create_sandbox(image="python:3.12-slim", wait=False)

    asyncio.run(run_test())

    assert _FakeRawSandboxClient.create_requests[0].sandbox_id == "sandbox-explicit"
    assert _FakeRawSandboxClient.create_requests[1].sandbox_id == ""


def test_create_sandbox_mounts_copies_builtin_tools_and_services_serialize_to_proto(
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
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_PENDING,
                    last_event_sequence=1,
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox(
            image="python:3.12-slim",
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
            builtin_tools=("claude", "uv"),
            required_services=(
                ServiceSpec(
                    name="postgres",
                    image="postgres:16",
                    envs={"POSTGRES_DB": "agents"},
                    healthcheck=HealthcheckConfig(
                        test=("CMD-SHELL", "pg_isready -U postgres"),
                        interval=timedelta(seconds=5),
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
            wait=False,
        )

    asyncio.run(run_test())

    create_spec = _FakeRawSandboxClient.create_requests[0].create_spec
    assert list(create_spec.builtin_tools) == ["claude", "uv"]
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
    # Verify service fields individually (proto equality sensitive to field ordering).
    assert len(create_spec.required_services) == 1
    req_svc = create_spec.required_services[0]
    assert req_svc.name == "postgres"
    assert req_svc.image == "postgres:16"
    assert dict(req_svc.envs) == {"POSTGRES_DB": "agents"}
    assert req_svc.healthcheck.interval.seconds == 5
    assert req_svc.healthcheck.retries == 3
    assert list(req_svc.post_start_on_primary) == ["python", "-c", "print('seeded')"]
    assert len(create_spec.optional_services) == 1
    assert create_spec.optional_services[0].name == "redis"


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
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_PENDING,
                    last_event_sequence=1,
                    labels={"team": "sdk", "purpose": "test"},
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.create_sandbox(
            image="python:3.12-slim",
            labels={"team": "sdk", "purpose": "test"},
            wait=False,
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
                        last_event_sequence=1,
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
        subscribe_requests: list[tuple[str, int, bool]] = []

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
            sequence = 1
            if self._sandbox_reads[sandbox_id] >= 2:
                state = service_pb2.SANDBOX_STATE_DELETED
                sequence = 2
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
                sequence=2,
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
        ("sandbox-1", 1, False),
        ("sandbox-2", 1, False),
    ]


def test_aclose_delegates_to_rpc_client_close(monkeypatch: pytest.MonkeyPatch) -> None:
    close_called = False

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            nonlocal close_called
            close_called = True

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = _new_client("/tmp/agents-sandbox.sock")
        await client.aclose()

    asyncio.run(run_test())
    assert close_called


def test_async_context_manager_calls_aclose(monkeypatch: pytest.MonkeyPatch) -> None:
    close_called = False

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            self.socket_path = socket_path
            self.timeout_seconds = timeout_seconds

        def close(self) -> None:
            nonlocal close_called
            close_called = True

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        async with _new_client("/tmp/agents-sandbox.sock"):
            pass

    asyncio.run(run_test())
    assert close_called


def test_error_classes_accept_structured_fields() -> None:
    from agents_sandbox.errors import (
        ExecAlreadyTerminalError,
        ExecNotFoundError,
        ExecNotRunningError,
        SandboxConflictError,
        SandboxNotFoundError,
        SandboxNotReadyError,
        SandboxSequenceExpiredError,
    )

    conflict = SandboxConflictError("sandbox-1")
    assert conflict.sandbox_id == "sandbox-1"
    assert "sandbox-1" in str(conflict)

    not_found = SandboxNotFoundError("sandbox-1")
    assert not_found.sandbox_id == "sandbox-1"
    assert "sandbox-1" in str(not_found)

    not_ready = SandboxNotReadyError("sandbox-1")
    assert not_ready.sandbox_id == "sandbox-1"

    exec_not_found = ExecNotFoundError("exec-1")
    assert exec_not_found.exec_id == "exec-1"

    exec_terminal = ExecAlreadyTerminalError("exec-1")
    assert exec_terminal.exec_id == "exec-1"

    exec_not_running = ExecNotRunningError("exec-1")
    assert exec_not_running.exec_id == "exec-1"

    seq_expired = SandboxSequenceExpiredError("sandbox-1", 5, 3)
    assert seq_expired.sandbox_id == "sandbox-1"
    assert seq_expired.from_sequence == 5
    assert seq_expired.oldest_sequence == 3
    assert "5" in str(seq_expired)
    assert "3" in str(seq_expired)


def test_error_classes_accept_none_fields() -> None:
    from agents_sandbox.errors import (
        ExecNotFoundError,
        SandboxConflictError,
        SandboxNotFoundError,
        SandboxSequenceExpiredError,
    )

    assert SandboxConflictError().sandbox_id is None
    assert SandboxNotFoundError().sandbox_id is None
    assert ExecNotFoundError().exec_id is None

    seq = SandboxSequenceExpiredError()
    assert seq.sandbox_id is None
    assert seq.from_sequence is None
    assert "expired" in str(seq).lower()


def test_space_heuristic_removed_sandbox_id_with_spaces_preserved() -> None:
    """Regression: old code used space heuristic to decide if arg was an ID or message."""
    from agents_sandbox.errors import SandboxNotFoundError

    # An ID that contains spaces should be stored as sandbox_id, not treated as a message.
    err = SandboxNotFoundError("id with spaces")
    assert err.sandbox_id == "id with spaces"


def test_sandbox_handle_labels_round_trip() -> None:
    handle = to_sandbox_handle(
        service_pb2.SandboxHandle(
            sandbox_id="sandbox-1",
            state=service_pb2.SANDBOX_STATE_READY,
            last_event_sequence=1,
            labels={"team": "sdk"},
        )
    )

    assert handle.labels == {"team": "sdk"}
