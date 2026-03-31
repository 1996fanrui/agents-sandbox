"""Tests for SandboxHandle conversion with error and state_changed_at fields."""

from google.protobuf.timestamp_pb2 import Timestamp

from agents_sandbox._conversions import to_sandbox_handle
from agents_sandbox._generated import service_pb2


def test_to_sandbox_handle_error_fields() -> None:
    ts = Timestamp()
    ts.GetCurrentTime()

    # FAILED sandbox with error fields populated
    handle = to_sandbox_handle(
        service_pb2.SandboxHandle(
            sandbox_id="sb-1",
            state=service_pb2.SANDBOX_STATE_FAILED,
            error_code="CONTAINER_NOT_RUNNING",
            error_message="primary container not running",
            state_changed_at=ts,
        )
    )
    assert handle.error_code == "CONTAINER_NOT_RUNNING"
    assert handle.error_message == "primary container not running"
    assert handle.state_changed_at is not None

    # READY sandbox without error fields
    handle2 = to_sandbox_handle(
        service_pb2.SandboxHandle(
            sandbox_id="sb-2",
            state=service_pb2.SANDBOX_STATE_READY,
        )
    )
    assert handle2.error_code is None
    assert handle2.error_message is None
    assert handle2.state_changed_at is None
