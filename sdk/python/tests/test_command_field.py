"""Tests for the `command` field on primary and companion containers."""

from __future__ import annotations

import asyncio

import pytest
from agents_sandbox._conversions import (
    to_companion_container,
    to_proto_companion_container,
    to_proto_create_sandbox_request,
)
from agents_sandbox._generated import service_pb2
from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec
from agents_sandbox.types import CompanionContainerSpec


def _install_fake_client(monkeypatch: pytest.MonkeyPatch, captured: list[service_pb2.CreateSandboxRequest]) -> None:
    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            pass

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            captured.append(request)
            return service_pb2.CreateSandboxResponse(
                sandbox=service_pb2.SandboxHandle(
                    sandbox_id="sandbox-1",
                    state=service_pb2.SANDBOX_STATE_PENDING,
                    last_event_sequence=1,
                )
            )

    monkeypatch.setattr("agents_sandbox.client.SandboxGrpcClient", _FakeRawSandboxClient)
    monkeypatch.setattr(
        "agents_sandbox.client._resolve_default_socket_path",
        lambda **_kwargs: "/tmp/agents-sandbox.sock",
    )


def test_python_sdk_rejects_empty_command_array(monkeypatch: pytest.MonkeyPatch) -> None:
    from agents_sandbox.client import AgentsSandboxClient

    _install_fake_client(monkeypatch, [])

    async def run_test() -> None:
        client = AgentsSandboxClient()
        with pytest.raises(ValueError, match=r"command.*empty array"):
            await client.create_sandbox(image="example:latest", command=[], wait=False)

    asyncio.run(run_test())


def test_python_sdk_rejects_empty_string_in_command(monkeypatch: pytest.MonkeyPatch) -> None:
    from agents_sandbox.client import AgentsSandboxClient

    _install_fake_client(monkeypatch, [])

    async def run_test() -> None:
        client = AgentsSandboxClient()
        with pytest.raises(ValueError, match=r"command\[1\]"):
            await client.create_sandbox(
                image="example:latest",
                command=["foo", "", "bar"],
                wait=False,
            )

    asyncio.run(run_test())


def test_python_sdk_command_populates_create_spec(monkeypatch: pytest.MonkeyPatch) -> None:
    from agents_sandbox.client import AgentsSandboxClient

    captured: list[service_pb2.CreateSandboxRequest] = []
    _install_fake_client(monkeypatch, captured)

    async def run_test() -> None:
        client = AgentsSandboxClient()
        await client.create_sandbox(
            image="example:latest",
            command=["myworker", "serve"],
            wait=False,
        )

    asyncio.run(run_test())

    assert len(captured) == 1
    assert list(captured[0].create_spec.command) == ["myworker", "serve"]


def test_companion_container_command_round_trip() -> None:
    original = CompanionContainerSpec(
        name="redis",
        image="redis:7",
        command=("redis-server", "--appendonly", "yes"),
    )
    proto_spec = to_proto_companion_container(original)
    assert list(proto_spec.command) == ["redis-server", "--appendonly", "yes"]
    round_tripped = to_companion_container(proto_spec)
    assert round_tripped.command == original.command
    assert round_tripped.name == "redis"


def test_companion_container_rejects_empty_string_in_command() -> None:
    with pytest.raises(ValueError, match=r"'redis'.*command\[1\]"):
        CompanionContainerSpec(
            name="redis",
            image="redis:7",
            command=("redis-server", "", "--appendonly"),
        )


def test_companion_container_rejects_empty_command_array() -> None:
    with pytest.raises(ValueError, match=r"'redis'.*empty array"):
        CompanionContainerSpec(
            name="redis",
            image="redis:7",
            command=[],
        )


def test_companion_container_rejects_empty_string_list_form() -> None:
    with pytest.raises(ValueError, match=r"'redis'.*command\[1\]"):
        CompanionContainerSpec(
            name="redis",
            image="redis:7",
            command=["foo", "", "bar"],
        )


def test_companion_container_valid_command_round_trip() -> None:
    spec = CompanionContainerSpec(
        name="redis",
        image="redis:7",
        command=["redis-server"],
    )
    assert spec.command == ("redis-server",)
    proto = to_proto_companion_container(spec)
    assert list(proto.command) == ["redis-server"]
    back = to_companion_container(proto)
    assert back.command == ("redis-server",)


def test_companion_container_default_command_omitted_is_none() -> None:
    spec = CompanionContainerSpec(name="redis", image="redis:7")
    assert spec.command is None
    proto = to_proto_companion_container(spec)
    assert list(proto.command) == []
    # Conversion back from proto's empty repeated field yields None to preserve
    # the "unset / inherit image CMD" semantics.
    back = to_companion_container(proto)
    assert back.command is None


def test_create_sandbox_spec_command_serializes_to_proto() -> None:
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(
            image="example:latest",
            command=("python", "worker.py"),
        ),
    )
    proto = to_proto_create_sandbox_request(request)
    assert list(proto.create_spec.command) == ["python", "worker.py"]
