"""Tests for resource limit fields wired through the Python SDK."""

from __future__ import annotations

from agents_sandbox._conversions import to_proto_create_sandbox_request
from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec
from agents_sandbox.types import CompanionContainerSpec


def test_resource_limits_round_trip_into_proto() -> None:
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(
            image="example:latest",
            cpu_limit="2",
            memory_limit="4g",
            disk_limit="10g",
            companion_containers=(
                CompanionContainerSpec(
                    name="db",
                    image="postgres:16",
                    cpu_limit="1",
                    memory_limit="512m",
                    disk_limit="5g",
                ),
            ),
        ),
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.create_spec.cpu_limit == "2"
    assert proto.create_spec.memory_limit == "4g"
    assert proto.create_spec.disk_limit == "10g"
    assert len(proto.create_spec.companion_containers) == 1
    companion = proto.create_spec.companion_containers[0]
    assert companion.cpu_limit == "1"
    assert companion.memory_limit == "512m"
    assert companion.disk_limit == "5g"


def test_resource_limits_default_to_empty_string_when_unset() -> None:
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(
            image="example:latest",
            companion_containers=(
                CompanionContainerSpec(name="db", image="postgres:16"),
            ),
        ),
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.create_spec.cpu_limit == ""
    assert proto.create_spec.memory_limit == ""
    assert proto.create_spec.disk_limit == ""
    companion = proto.create_spec.companion_containers[0]
    assert companion.cpu_limit == ""
    assert companion.memory_limit == ""
    assert companion.disk_limit == ""
