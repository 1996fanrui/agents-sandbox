"""Tests for port mapping support in SDK types and conversions."""

from __future__ import annotations

import asyncio

import pytest
from agents_sandbox.types import PortMapping
from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec
from agents_sandbox._conversions import to_proto_create_sandbox_request, to_proto_port_mapping
from agents_sandbox._generated import service_pb2


def test_port_mapping_defaults() -> None:
    """PortMapping protocol defaults to tcp."""
    pm = PortMapping(container_port=8080, host_port=9090)
    assert pm.protocol == "tcp"


def test_port_mapping_frozen() -> None:
    """PortMapping is immutable."""
    pm = PortMapping(container_port=8080, host_port=9090)
    with pytest.raises(AttributeError):
        pm.container_port = 3000  # type: ignore[misc]


def test_to_proto_port_mapping_tcp() -> None:
    """TCP port mapping converts correctly."""
    pm = PortMapping(container_port=8080, host_port=9090, protocol="tcp")
    proto = to_proto_port_mapping(pm)
    assert proto.container_port == 8080
    assert proto.host_port == 9090
    assert proto.protocol == service_pb2.PORT_PROTOCOL_TCP


def test_to_proto_port_mapping_udp() -> None:
    """UDP port mapping converts correctly."""
    pm = PortMapping(container_port=53, host_port=5353, protocol="udp")
    proto = to_proto_port_mapping(pm)
    assert proto.container_port == 53
    assert proto.host_port == 5353
    assert proto.protocol == service_pb2.PORT_PROTOCOL_UDP


def test_to_proto_port_mapping_sctp() -> None:
    """SCTP port mapping converts correctly."""
    pm = PortMapping(container_port=3000, host_port=3000, protocol="sctp")
    proto = to_proto_port_mapping(pm)
    assert proto.protocol == service_pb2.PORT_PROTOCOL_SCTP


def test_to_proto_port_mapping_case_insensitive() -> None:
    """Protocol string is case-insensitive."""
    pm = PortMapping(container_port=80, host_port=80, protocol="UDP")
    proto = to_proto_port_mapping(pm)
    assert proto.protocol == service_pb2.PORT_PROTOCOL_UDP


def test_to_proto_port_mapping_unknown_raises_error() -> None:
    """Unknown protocol raises ValueError."""
    pm = PortMapping(container_port=80, host_port=80, protocol="unknown")
    with pytest.raises(ValueError, match="unsupported port protocol"):
        to_proto_port_mapping(pm)


def test_create_sandbox_request_with_ports() -> None:
    """Ports are included in the proto CreateSpec."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(
            image="test:latest",
            ports=(
                PortMapping(container_port=8080, host_port=9090, protocol="tcp"),
                PortMapping(container_port=53, host_port=5353, protocol="udp"),
            ),
        ),
    )
    proto = to_proto_create_sandbox_request(request)
    assert len(proto.create_spec.ports) == 2
    assert proto.create_spec.ports[0].container_port == 8080
    assert proto.create_spec.ports[0].host_port == 9090
    assert proto.create_spec.ports[0].protocol == service_pb2.PORT_PROTOCOL_TCP
    assert proto.create_spec.ports[1].container_port == 53
    assert proto.create_spec.ports[1].protocol == service_pb2.PORT_PROTOCOL_UDP


def test_create_sandbox_request_no_ports() -> None:
    """Empty ports results in empty list in proto."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="test:latest"),
    )
    proto = to_proto_create_sandbox_request(request)
    assert len(proto.create_spec.ports) == 0


def test_create_sandbox_client_passes_ports(monkeypatch: pytest.MonkeyPatch) -> None:
    """Client forwards ports to proto request."""
    from agents_sandbox.client import AgentsSandboxClient

    captured_requests: list[service_pb2.CreateSandboxRequest] = []

    class _FakeRawSandboxClient:
        def __init__(self, socket_path: str, *, timeout_seconds: float) -> None:
            pass

        def close(self) -> None:
            return None

        def create_sandbox(self, request: service_pb2.CreateSandboxRequest) -> service_pb2.CreateSandboxResponse:
            captured_requests.append(request)
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

    async def run_test() -> None:
        client = AgentsSandboxClient()
        await client.create_sandbox(
            image="test:latest",
            ports=(
                PortMapping(container_port=8080, host_port=9090),
                PortMapping(container_port=53, host_port=5353, protocol="udp"),
            ),
            wait=False,
        )

    asyncio.run(run_test())
    assert len(captured_requests) == 1
    ports = captured_requests[0].create_spec.ports
    assert len(ports) == 2
    assert ports[0].container_port == 8080
    assert ports[0].host_port == 9090
    assert ports[0].protocol == service_pb2.PORT_PROTOCOL_TCP
    assert ports[1].protocol == service_pb2.PORT_PROTOCOL_UDP


def test_port_mapping_in_public_api() -> None:
    """PortMapping is importable from the top-level package."""
    from agents_sandbox import PortMapping as PM
    pm = PM(container_port=80, host_port=80)
    assert pm.container_port == 80
