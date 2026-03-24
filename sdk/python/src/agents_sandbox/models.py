"""Public SDK enum models backed by protobuf values."""

from __future__ import annotations

from enum import IntEnum

from ._generated import service_pb2


class SandboxState(IntEnum):
    """Public sandbox lifecycle states."""

    UNSPECIFIED = service_pb2.SANDBOX_STATE_UNSPECIFIED
    PENDING = service_pb2.SANDBOX_STATE_PENDING
    READY = service_pb2.SANDBOX_STATE_READY
    FAILED = service_pb2.SANDBOX_STATE_FAILED
    STOPPED = service_pb2.SANDBOX_STATE_STOPPED
    DELETING = service_pb2.SANDBOX_STATE_DELETING
    DELETED = service_pb2.SANDBOX_STATE_DELETED


class ExecState(IntEnum):
    """Public exec lifecycle states."""

    UNSPECIFIED = service_pb2.EXEC_STATE_UNSPECIFIED
    RUNNING = service_pb2.EXEC_STATE_RUNNING
    FINISHED = service_pb2.EXEC_STATE_FINISHED
    FAILED = service_pb2.EXEC_STATE_FAILED
    CANCELLED = service_pb2.EXEC_STATE_CANCELLED

    @property
    def is_terminal(self) -> bool:
        return self in {ExecState.FINISHED, ExecState.FAILED, ExecState.CANCELLED}


class SandboxEventType(IntEnum):
    """Public sandbox event types."""

    UNSPECIFIED = service_pb2.EVENT_TYPE_UNSPECIFIED
    SANDBOX_ACCEPTED = service_pb2.SANDBOX_ACCEPTED
    SANDBOX_PREPARING = service_pb2.SANDBOX_PREPARING
    SANDBOX_DEPENDENCY_READY = service_pb2.SANDBOX_DEPENDENCY_READY
    SANDBOX_READY = service_pb2.SANDBOX_READY
    SANDBOX_FAILED = service_pb2.SANDBOX_FAILED
    SANDBOX_STOP_REQUESTED = service_pb2.SANDBOX_STOP_REQUESTED
    SANDBOX_STOPPED = service_pb2.SANDBOX_STOPPED
    SANDBOX_DELETE_REQUESTED = service_pb2.SANDBOX_DELETE_REQUESTED
    SANDBOX_DELETED = service_pb2.SANDBOX_DELETED
    EXEC_STARTED = service_pb2.EXEC_STARTED
    EXEC_FINISHED = service_pb2.EXEC_FINISHED
    EXEC_FAILED = service_pb2.EXEC_FAILED
    EXEC_CANCELLED = service_pb2.EXEC_CANCELLED


class ProjectionMountMode(IntEnum):
    """Public projection mount modes."""

    UNSPECIFIED = service_pb2.PROJECTION_MOUNT_MODE_UNSPECIFIED
    BIND = service_pb2.PROJECTION_MOUNT_MODE_BIND
    SHADOW_COPY = service_pb2.PROJECTION_MOUNT_MODE_SHADOW_COPY


class WorkspaceMaterializationMode(IntEnum):
    """Public workspace materialization modes."""

    UNSPECIFIED = service_pb2.WORKSPACE_MATERIALIZATION_MODE_UNSPECIFIED
    DURABLE_COPY = service_pb2.WORKSPACE_MATERIALIZATION_MODE_DURABLE_COPY
    BIND = service_pb2.WORKSPACE_MATERIALIZATION_MODE_BIND


__all__ = [
    "ExecState",
    "ProjectionMountMode",
    "SandboxEventType",
    "SandboxState",
    "WorkspaceMaterializationMode",
]
