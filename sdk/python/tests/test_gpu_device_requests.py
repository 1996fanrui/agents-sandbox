from __future__ import annotations

import asyncio

import pytest
from agents_sandbox._conversions import to_proto_create_sandbox_request
from agents_sandbox._generated import service_pb2
from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec

from tests.smoke_support import _new_client


def test_gpu_device_request_round_trip_into_proto(monkeypatch: pytest.MonkeyPatch) -> None:
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
            gpus="all",
            wait=False,
        )

    asyncio.run(run_test())

    assert _FakeRawSandboxClient.create_requests[0].create_spec.gpus == "all"


def test_gpu_device_request_defaults_to_empty_string() -> None:
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="python:3.12-slim"),
    )

    proto = to_proto_create_sandbox_request(request)

    assert proto.create_spec.gpus == ""
