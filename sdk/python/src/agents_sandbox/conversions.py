"""Helpers that convert between protobuf payloads and public SDK types."""

from __future__ import annotations

from datetime import UTC

from ._generated import service_pb2
from .models import (
    ExecState,
    ProjectionMountMode,
    SandboxEventType,
    SandboxState,
    WorkspaceMaterializationMode,
)
from .types import (
    CallerMetadata,
    CopySpec,
    CreateExecRequest,
    CreateSandboxRequest,
    DependencySpec,
    ExecHandle,
    MountSpec,
    PingInfo,
    ResolvedProjectionHandle,
    SandboxEvent,
    SandboxHandle,
    SandboxOwner,
    WorkspaceMaterializationSpec,
)


def to_ping_info(response: service_pb2.PingResponse) -> PingInfo:
    return PingInfo(version=response.version, daemon=response.daemon)


def to_proto_sandbox_owner(owner: SandboxOwner) -> service_pb2.SandboxOwner:
    return service_pb2.SandboxOwner(
        product=owner.product,
        owner_type=owner.owner_type,
        owner_id=owner.owner_id,
    )


def to_proto_caller_metadata(metadata: CallerMetadata) -> service_pb2.CallerMetadata:
    return service_pb2.CallerMetadata(
        product=metadata.product,
        session_id=metadata.session_id,
        run_id=metadata.run_id,
    )


def to_proto_workspace_spec(workspace: WorkspaceMaterializationSpec) -> service_pb2.WorkspaceSpec:
    resolved_mode = _WORKSPACE_MODE_TO_PROTO[workspace.mode]
    return service_pb2.WorkspaceSpec(
        path="" if workspace.source_root is None else workspace.source_root,
        mode=resolved_mode,
    )


def to_proto_create_sandbox_request(request: CreateSandboxRequest) -> service_pb2.CreateSandboxRequest:
    return service_pb2.CreateSandboxRequest(
        sandbox_owner=to_proto_sandbox_owner(request.sandbox_owner),
        create_spec=service_pb2.CreateSpec(
            image=request.create_spec.image,
            workspace=(
                None
                if request.create_spec.workspace is None
                else to_proto_workspace_spec(request.create_spec.workspace)
            ),
            cache_projections=[
                service_pb2.CacheProjectionRequest(
                    cache_id=item.capability_id,
                    enabled=item.enabled,
                )
                for item in request.create_spec.cache_projections
            ],
            tooling_projections=[
                service_pb2.ToolingProjectionRequest(
                    capability_id=item.capability_id,
                    writable=item.writable,
                    source_path="" if item.source_root is None else item.source_root,
                    target_path="" if item.target_path is None else item.target_path,
                )
                for item in request.create_spec.tooling_projections
            ],
            dependencies=[to_proto_dependency(item) for item in request.create_spec.dependencies],
            mounts=[to_proto_mount(item) for item in request.create_spec.mounts],
            copies=[to_proto_copy(item) for item in request.create_spec.copies],
            builtin_resources=list(request.create_spec.builtin_resources),
        ),
        caller_metadata=(
            None if request.caller_metadata is None else to_proto_caller_metadata(request.caller_metadata)
        ),
    )


def to_proto_dependency(spec: DependencySpec) -> service_pb2.DependencySpec:
    return service_pb2.DependencySpec(
        dependency_name=spec.name,
        image=spec.image,
        network_alias="" if spec.network_alias is None else spec.network_alias,
        environment=[
            service_pb2.KeyValue(key=key, value=value)
            for key, value in spec.environment.items()
        ],
    )


def to_proto_mount(spec: MountSpec) -> service_pb2.MountSpec:
    return service_pb2.MountSpec(
        source=spec.source,
        target=spec.target,
        writable=spec.writable,
    )


def to_proto_copy(spec: CopySpec) -> service_pb2.CopySpec:
    return service_pb2.CopySpec(
        source=spec.source,
        target=spec.target,
        exclude_patterns=list(spec.exclude_patterns),
    )


def to_proto_create_exec_request(request: CreateExecRequest) -> service_pb2.CreateExecRequest:
    return service_pb2.CreateExecRequest(
        sandbox_id=request.sandbox_id,
        command=list(request.command),
        cwd="" if request.cwd is None else request.cwd,
        env_overrides=[
            service_pb2.KeyValue(key=key, value=value)
            for key, value in request.env_overrides.items()
        ],
        caller_metadata=(
            None if request.caller_metadata is None else to_proto_caller_metadata(request.caller_metadata)
        ),
    )


def to_sandbox_owner(owner: service_pb2.SandboxOwner) -> SandboxOwner:
    return SandboxOwner(
        product=owner.product,
        owner_type=owner.owner_type,
        owner_id=owner.owner_id,
    )


def to_dependency(spec: service_pb2.DependencySpec) -> DependencySpec:
    return DependencySpec(
        name=spec.dependency_name,
        image=spec.image,
        network_alias=spec.network_alias or None,
        environment={item.key: item.value for item in spec.environment},
    )


def to_resolved_projection_handle(handle: service_pb2.ResolvedProjectionHandle) -> ResolvedProjectionHandle:
    return ResolvedProjectionHandle(
        capability_id=handle.capability_id,
        source_path=handle.source_path or None,
        target_path=handle.target_path or None,
        mount_mode=map_projection_mount_mode(handle.mount_mode),
        writable=handle.writable,
        write_back=handle.write_back,
    )


def to_sandbox_handle(handle: service_pb2.SandboxHandle) -> SandboxHandle:
    if handle.last_event_cursor:
        parse_cursor_sequence(handle.sandbox_id, handle.last_event_cursor)
    return SandboxHandle(
        sandbox_id=handle.sandbox_id,
        sandbox_owner=to_sandbox_owner(handle.owner),
        state=map_sandbox_state(handle.state),
        resolved_tooling_projections=tuple(
            to_resolved_projection_handle(item) for item in handle.resolved_tooling_projections
        ),
        dependencies=tuple(to_dependency(item) for item in handle.dependencies),
        last_event_cursor=handle.last_event_cursor,
    )


def to_exec_handle(exec_status: service_pb2.ExecStatus) -> ExecHandle:
    state = map_exec_state(exec_status.state)
    exit_code = exec_status.exit_code if state.is_terminal else None
    return ExecHandle(
        exec_id=exec_status.exec_id,
        sandbox_id=exec_status.sandbox_id,
        state=state,
        command=tuple(exec_status.command),
        cwd=exec_status.cwd or None,
        env_overrides={item.key: item.value for item in exec_status.env_overrides},
        exit_code=exit_code,
        error=exec_status.error or None,
        stdout=exec_status.stdout or None,
        stderr=exec_status.stderr or None,
    )


def to_sandbox_event(event: service_pb2.SandboxEvent) -> SandboxEvent:
    if not event.HasField("occurred_at"):
        raise ValueError(f"Sandbox event {event.event_id or '<unknown>'} is missing occurred_at")
    parse_cursor_sequence(event.sandbox_id, event.cursor)
    exit_code: int | None = None
    if event.event_type == service_pb2.EXEC_FINISHED or event.exit_code != 0:
        exit_code = event.exit_code
    sandbox_state = (
        None
        if event.sandbox_state == service_pb2.SANDBOX_STATE_UNSPECIFIED
        else map_sandbox_state(event.sandbox_state)
    )
    exec_state = (
        None if event.exec_state == service_pb2.EXEC_STATE_UNSPECIFIED else map_exec_state(event.exec_state)
    )
    return SandboxEvent(
        event_id=event.event_id,
        sequence=int(event.sequence),
        cursor=event.cursor,
        sandbox_id=event.sandbox_id,
        event_type=SandboxEventType(event.event_type),
        occurred_at=event.occurred_at.ToDatetime(tzinfo=UTC),
        replay=event.replay,
        snapshot=event.snapshot,
        phase=event.phase or None,
        dependency_name=event.dependency_name or None,
        error_code=event.error_code or None,
        error_message=event.error_message or None,
        reason=event.reason or None,
        exec_id=event.exec_id or None,
        exit_code=exit_code,
        sandbox_state=sandbox_state,
        exec_state=exec_state,
    )


def map_sandbox_state(state: int) -> SandboxState:
    return SandboxState(state)


def map_exec_state(state: int) -> ExecState:
    return ExecState(state)


def map_projection_mount_mode(mode: int) -> ProjectionMountMode:
    return ProjectionMountMode(mode)


def parse_cursor_sequence(sandbox_id: str, cursor: str) -> int:
    if cursor == "" or cursor == "0":
        return 0
    prefix, separator, raw_sequence = cursor.partition(":")
    if separator == "" or prefix != sandbox_id:
        raise ValueError(f"Cursor does not belong to sandbox {sandbox_id}: {cursor}")
    sequence = int(raw_sequence)
    if sequence < 0:
        raise ValueError(f"Cursor sequence must be non-negative: {cursor}")
    return sequence


def normalize_from_cursor(sandbox_id: str, cursor: str) -> str:
    if cursor == "0":
        return cursor
    if cursor == "":
        return cursor
    parse_cursor_sequence(sandbox_id, cursor)
    return cursor


_WORKSPACE_MODE_TO_PROTO = {
    WorkspaceMaterializationMode.UNSPECIFIED: service_pb2.WORKSPACE_MATERIALIZATION_MODE_UNSPECIFIED,
    WorkspaceMaterializationMode.DURABLE_COPY: service_pb2.WORKSPACE_MATERIALIZATION_MODE_DURABLE_COPY,
    WorkspaceMaterializationMode.BIND: service_pb2.WORKSPACE_MATERIALIZATION_MODE_BIND,
}


__all__ = [
    "map_exec_state",
    "map_projection_mount_mode",
    "map_sandbox_state",
    "normalize_from_cursor",
    "parse_cursor_sequence",
    "to_exec_handle",
    "to_ping_info",
    "to_proto_create_exec_request",
    "to_proto_create_sandbox_request",
    "to_proto_sandbox_owner",
    "to_sandbox_event",
    "to_sandbox_handle",
    "to_sandbox_owner",
]
