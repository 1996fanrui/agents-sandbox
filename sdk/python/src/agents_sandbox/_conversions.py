"""Helpers that convert between protobuf payloads and public SDK types."""

from __future__ import annotations

from datetime import UTC

from ._generated import service_pb2
from .models import ExecState, SandboxEventType, SandboxState
from ._request_types import CreateExecRequest, CreateSandboxRequest
from .types import (
    CopySpec,
    ExecHandle,
    HealthcheckConfig,
    MountSpec,
    PingInfo,
    SandboxEvent,
    SandboxHandle,
    ServiceSpec,
)


def to_ping_info(response: service_pb2.PingResponse) -> PingInfo:
    return PingInfo(version=response.version, daemon=response.daemon)


def to_proto_healthcheck(config: HealthcheckConfig) -> service_pb2.HealthcheckConfig:
    return service_pb2.HealthcheckConfig(
        test=list(config.test),
        interval="" if config.interval is None else config.interval,
        timeout="" if config.timeout is None else config.timeout,
        retries=0 if config.retries is None else config.retries,
        start_period="" if config.start_period is None else config.start_period,
        start_interval="" if config.start_interval is None else config.start_interval,
    )


def to_proto_service(spec: ServiceSpec) -> service_pb2.ServiceSpec:
    return service_pb2.ServiceSpec(
        name=spec.name,
        image=spec.image,
        environment=[
            service_pb2.KeyValue(key=key, value=value)
            for key, value in spec.environment.items()
        ],
        healthcheck=(
            None
            if spec.healthcheck is None
            else to_proto_healthcheck(spec.healthcheck)
        ),
        post_start_on_primary=list(spec.post_start_on_primary),
    )


def to_proto_create_sandbox_request(request: CreateSandboxRequest) -> service_pb2.CreateSandboxRequest:
    return service_pb2.CreateSandboxRequest(
        sandbox_id="" if request.sandbox_id is None else request.sandbox_id,
        create_spec=service_pb2.CreateSpec(
            image="" if request.create_spec.image is None else request.create_spec.image,
            mounts=[to_proto_mount(item) for item in request.create_spec.mounts],
            copies=[to_proto_copy(item) for item in request.create_spec.copies],
            builtin_resources=list(request.create_spec.builtin_resources),
            required_services=[to_proto_service(item) for item in request.create_spec.required_services],
            optional_services=[to_proto_service(item) for item in request.create_spec.optional_services],
            labels=dict(request.create_spec.labels),
        ),
        config_yaml=request.config_yaml,
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
        exec_id="" if request.exec_id is None else request.exec_id,
        cwd="" if request.cwd is None else request.cwd,
        env_overrides=[
            service_pb2.KeyValue(key=key, value=value)
            for key, value in request.env_overrides.items()
        ],
    )


def to_healthcheck(config: service_pb2.HealthcheckConfig | None) -> HealthcheckConfig | None:
    if config is None:
        return None
    return HealthcheckConfig(
        test=tuple(config.test),
        interval=config.interval or None,
        timeout=config.timeout or None,
        retries=config.retries if config.retries != 0 else None,
        start_period=config.start_period or None,
        start_interval=config.start_interval or None,
    )


def to_service(spec: service_pb2.ServiceSpec) -> ServiceSpec:
    return ServiceSpec(
        name=spec.name,
        image=spec.image,
        environment={item.key: item.value for item in spec.environment},
        healthcheck=to_healthcheck(spec.healthcheck if spec.HasField("healthcheck") else None),
        post_start_on_primary=tuple(spec.post_start_on_primary),
    )


def to_sandbox_handle(handle: service_pb2.SandboxHandle) -> SandboxHandle:
    if handle.last_event_sequence < 0:
        raise ValueError(f"Sequence must be non-negative: {handle.last_event_sequence}")
    return SandboxHandle(
        sandbox_id=handle.sandbox_id,
        state=map_sandbox_state(handle.state),
        last_event_sequence=int(handle.last_event_sequence),
        required_services=tuple(to_service(item) for item in handle.required_services),
        optional_services=tuple(to_service(item) for item in handle.optional_services),
        labels=dict(handle.labels),
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
        last_event_sequence=int(exec_status.last_event_sequence),
    )


def to_exec_snapshot(response: object) -> tuple[ExecHandle, int]:
    exec_status = getattr(response, "exec", None)
    if exec_status is None:
        raise ValueError("GetExecResponse is missing exec")
    handle = to_exec_handle(exec_status)
    sequence = handle.last_event_sequence
    if sequence <= 0:
        raise ValueError(f"Sequence must be positive: {sequence}")
    return handle, sequence


def to_sandbox_event(event: service_pb2.SandboxEvent) -> SandboxEvent:
    if not event.HasField("occurred_at"):
        raise ValueError(f"Sandbox event {event.event_id or '<unknown>'} is missing occurred_at")
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
        sandbox_id=event.sandbox_id,
        event_type=SandboxEventType(event.event_type),
        occurred_at=event.occurred_at.ToDatetime(tzinfo=UTC),
        replay=event.replay,
        snapshot=event.snapshot,
        phase=event.phase or None,
        service_name=event.service_name or None,
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


def parse_event_sequence(sequence: int) -> int:
    sequence = int(sequence)
    if sequence < 0:
        raise ValueError(f"Sequence must be non-negative: {sequence}")
    return sequence


def normalize_from_sequence(sequence: int) -> int:
    return parse_event_sequence(sequence)


__all__ = [
    "map_exec_state",
    "map_sandbox_state",
    "normalize_from_sequence",
    "parse_event_sequence",
    "to_exec_handle",
    "to_exec_snapshot",
    "to_ping_info",
    "to_proto_create_exec_request",
    "to_proto_create_sandbox_request",
    "to_sandbox_event",
    "to_sandbox_handle",
]
