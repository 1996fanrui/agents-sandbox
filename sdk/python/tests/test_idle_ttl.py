"""Tests for idle_ttl field in CreateSandboxSpec and proto conversion."""

from __future__ import annotations

import datetime

from agents_sandbox._request_types import CreateSandboxRequest, CreateSandboxSpec
from agents_sandbox._conversions import to_proto_create_sandbox_request


def test_idle_ttl_zero_maps_to_duration() -> None:
    """timedelta(0) maps to Duration(seconds=0, nanos=0) on the proto."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="test:latest", idle_ttl=datetime.timedelta(0)),
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.create_spec.HasField("idle_ttl")
    assert proto.create_spec.idle_ttl.seconds == 0
    assert proto.create_spec.idle_ttl.nanos == 0


def test_idle_ttl_positive_maps_to_duration() -> None:
    """timedelta(minutes=5) maps to Duration(seconds=300)."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="test:latest", idle_ttl=datetime.timedelta(minutes=5)),
    )
    proto = to_proto_create_sandbox_request(request)
    assert proto.create_spec.HasField("idle_ttl")
    assert proto.create_spec.idle_ttl.seconds == 300
    assert proto.create_spec.idle_ttl.nanos == 0


def test_idle_ttl_none_not_set() -> None:
    """When idle_ttl is None (default), the proto field is not set."""
    request = CreateSandboxRequest(
        create_spec=CreateSandboxSpec(image="test:latest"),
    )
    proto = to_proto_create_sandbox_request(request)
    assert not proto.create_spec.HasField("idle_ttl")
