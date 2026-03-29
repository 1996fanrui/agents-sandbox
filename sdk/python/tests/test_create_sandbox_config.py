"""Tests for YAML config support in create_sandbox."""

from __future__ import annotations

import asyncio

import pytest
from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec
from agents_sandbox._conversions import to_proto_create_sandbox_request


def test_create_sandbox_spec_image_optional() -> None:
    """image defaults to None when not provided."""
    spec = CreateSandboxSpec()
    assert spec.image is None


def test_create_sandbox_request_config_yaml() -> None:
    """config_yaml is included in the proto request."""
    yaml_content = b"image: test:latest\n"
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(),
        config_yaml=yaml_content,
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.config_yaml == yaml_content
    assert proto.create_spec.image == ""


def test_create_sandbox_request_with_image_and_config() -> None:
    """Both image and config_yaml can be provided."""
    yaml_content = b"image: yaml:latest\n"
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="override:latest"),
        config_yaml=yaml_content,
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.config_yaml == yaml_content
    assert proto.create_spec.image == "override:latest"


def test_create_sandbox_accepts_config_yaml_string(monkeypatch: pytest.MonkeyPatch) -> None:
    """Client encodes config_yaml strings and sends bytes."""
    from agents_sandbox._generated import service_pb2
    from agents_sandbox.client import AgentsSandboxClient

    yaml_content = "image: test:latest\nbuiltin_tools:\n  - claude\n"
    captured_requests: list[service_pb2.CreateSandboxRequest] = []

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            pass

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            captured_requests.append(request)
            return service_pb2.CreateSandboxResponse(
                sandbox_id="sandbox-1",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_sequence=1,
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)
    monkeypatch.setattr(
        "agents_sandbox.client._resolve_default_socket_path",
        lambda **_kwargs: "/tmp/agents-sandbox.sock",
    )

    async def run_test() -> None:
        client = AgentsSandboxClient()
        await client.create_sandbox(config_yaml=yaml_content, wait=False)

    asyncio.run(run_test())
    assert len(captured_requests) == 1
    assert captured_requests[0].config_yaml == yaml_content.encode("utf-8")
    assert captured_requests[0].create_spec.image == ""


def test_create_sandbox_accepts_config_yaml_bytes(monkeypatch: pytest.MonkeyPatch) -> None:
    """Client forwards config_yaml bytes without modification."""
    from agents_sandbox._generated import service_pb2
    from agents_sandbox.client import AgentsSandboxClient

    yaml_content = b"image: test:latest\nbuiltin_tools:\n  - claude\n"
    captured_requests: list[service_pb2.CreateSandboxRequest] = []

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            pass

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            captured_requests.append(request)
            return service_pb2.CreateSandboxResponse(
                sandbox_id="sandbox-1",
                initial_state=service_pb2.SANDBOX_STATE_PENDING,
            )

        def get_sandbox(self, sandbox_id: str) -> service_pb2.GetSandboxResponse:
            return service_pb2.GetSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id=sandbox_id,
                    state=service_pb2.SANDBOX_STATE_READY,
                    last_event_sequence=1,
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)
    monkeypatch.setattr(
        "agents_sandbox.client._resolve_default_socket_path",
        lambda **_kwargs: "/tmp/agents-sandbox.sock",
    )

    async def run_test() -> None:
        client = AgentsSandboxClient()
        await client.create_sandbox(config_yaml=yaml_content, wait=False)

    asyncio.run(run_test())
    assert len(captured_requests) == 1
    assert captured_requests[0].config_yaml == yaml_content
    assert captured_requests[0].create_spec.image == ""


def test_create_sandbox_request_with_envs() -> None:
    """envs are included in the proto request."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(
            image="test:latest",
            envs={"APP_ENV": "prod", "DB_HOST": "localhost"},
        ),
    )
    proto = to_proto_create_sandbox_request(request)
    env_map = {kv.key: kv.value for kv in proto.create_spec.envs}
    assert env_map == {"APP_ENV": "prod", "DB_HOST": "localhost"}


def test_create_sandbox_neither_config_yaml_nor_image_raises(monkeypatch: pytest.MonkeyPatch) -> None:
    """ValueError when neither config_yaml nor image is provided."""
    monkeypatch.setattr(
        "agents_sandbox.client._resolve_default_socket_path",
        lambda **_kwargs: "/tmp/agents-sandbox.sock",
    )

    from agents_sandbox.client import AgentsSandboxClient

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            pass

        def close(self) -> None:
            return None

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)

    async def run_test() -> None:
        client = AgentsSandboxClient()
        await client.create_sandbox()

    with pytest.raises(ValueError, match="at least one"):
        asyncio.run(run_test())
