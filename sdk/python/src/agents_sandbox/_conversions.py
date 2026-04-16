"""Helpers that convert between protobuf payloads and public SDK types."""

from __future__ import annotations

from datetime import UTC, timedelta

from google.protobuf.duration_pb2 import Duration

from ._generated import service_pb2
from .models import ExecState, SandboxEventType, SandboxState
from ._request_types import CreateExecRequest, CreateSandboxRequest
from .types import (
    CompanionContainerEventDetails,
    CompanionContainerSpec,
    CopySpec,
    ExecEventDetails,
    ExecHandle,
    HealthcheckConfig,
    MountSpec,
    PingInfo,
    PortMapping,
    SandboxEvent,
    SandboxHandle,
    SandboxPhaseDetails,
)


def to_ping_info(response: service_pb2.PingResponse) -> PingInfo:
    return PingInfo(version=response.version, daemon=response.daemon)


def _duration_to_timedelta(d: Duration | None) -> timedelta | None:
    if d is None or (d.seconds == 0 and d.nanos == 0):
        return None
    return timedelta(seconds=d.seconds, microseconds=d.nanos // 1000)


def _timedelta_to_duration(td: timedelta | None) -> Duration | None:
    if td is None:
        return None
    total_seconds = int(td.total_seconds())
    nanos = int((td.total_seconds() - total_seconds) * 1_000_000_000)
    return Duration(seconds=total_seconds, nanos=nanos)


def to_proto_healthcheck(config: HealthcheckConfig) -> service_pb2.HealthcheckConfig:
    hc = service_pb2.HealthcheckConfig(
        test=list(config.test),
        retries=0 if config.retries is None else config.retries,
    )
    if config.interval is not None:
        d = _timedelta_to_duration(config.interval)
        if d is not None:
            hc.interval.CopyFrom(d)
    if config.timeout is not None:
        d = _timedelta_to_duration(config.timeout)
        if d is not None:
            hc.timeout.CopyFrom(d)
    if config.start_period is not None:
        d = _timedelta_to_duration(config.start_period)
        if d is not None:
            hc.start_period.CopyFrom(d)
    if config.start_interval is not None:
        d = _timedelta_to_duration(config.start_interval)
        if d is not None:
            hc.start_interval.CopyFrom(d)
    return hc


def to_proto_companion_container(spec: CompanionContainerSpec) -> service_pb2.CompanionContainerSpec:
    return service_pb2.CompanionContainerSpec(
        name=spec.name,
        image=spec.image,
        envs=dict(spec.envs),
        healthcheck=(
            None
            if spec.healthcheck is None
            else to_proto_healthcheck(spec.healthcheck)
        ),
        command=[] if spec.command is None else list(spec.command),
        post_start_on_primary=list(spec.post_start_on_primary),
    )


def to_proto_create_sandbox_request(request: CreateSandboxRequest) -> service_pb2.CreateSandboxRequest:
    create_spec = service_pb2.CreateSpec(
        image="" if request.create_spec.image is None else request.create_spec.image,
        mounts=[to_proto_mount(item) for item in request.create_spec.mounts],
        copies=[to_proto_copy(item) for item in request.create_spec.copies],
        ports=[to_proto_port_mapping(item) for item in request.create_spec.ports],
        builtin_tools=list(request.create_spec.builtin_tools),
        command=list(request.create_spec.command),
        companion_containers=[to_proto_companion_container(item) for item in request.create_spec.companion_containers],
        labels=dict(request.create_spec.labels),
        envs=dict(request.create_spec.envs),
    )
    if request.create_spec.idle_ttl is not None:
        create_spec.idle_ttl.CopyFrom(_timedelta_to_duration(request.create_spec.idle_ttl))
    return service_pb2.CreateSandboxRequest(
        sandbox_id="" if request.sandbox_id is None else request.sandbox_id,
        create_spec=create_spec,
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


_PORT_PROTOCOL_MAP = {
    "tcp": service_pb2.PORT_PROTOCOL_TCP,
    "udp": service_pb2.PORT_PROTOCOL_UDP,
    "sctp": service_pb2.PORT_PROTOCOL_SCTP,
}


def to_proto_port_mapping(spec: PortMapping) -> service_pb2.PortMapping:
    normalized = spec.protocol.lower()
    if normalized not in _PORT_PROTOCOL_MAP:
        raise ValueError(
            f"unsupported port protocol {spec.protocol!r}; must be tcp, udp, or sctp"
        )
    return service_pb2.PortMapping(
        container_port=spec.container_port,
        host_port=spec.host_port,
        protocol=_PORT_PROTOCOL_MAP[normalized],
    )


def to_proto_create_exec_request(request: CreateExecRequest) -> service_pb2.CreateExecRequest:
    return service_pb2.CreateExecRequest(
        sandbox_id=request.sandbox_id,
        command=list(request.command),
        exec_id="" if request.exec_id is None else request.exec_id,
        cwd="" if request.cwd is None else request.cwd,
        env_overrides=dict(request.env_overrides),
    )


def to_healthcheck(config: service_pb2.HealthcheckConfig | None) -> HealthcheckConfig | None:
    if config is None:
        return None
    return HealthcheckConfig(
        test=tuple(config.test),
        interval=_duration_to_timedelta(config.interval if config.HasField("interval") else None),
        timeout=_duration_to_timedelta(config.timeout if config.HasField("timeout") else None),
        retries=config.retries if config.retries != 0 else None,
        start_period=_duration_to_timedelta(config.start_period if config.HasField("start_period") else None),
        start_interval=_duration_to_timedelta(config.start_interval if config.HasField("start_interval") else None),
    )


def to_companion_container(spec: service_pb2.CompanionContainerSpec) -> CompanionContainerSpec:
    return CompanionContainerSpec(
        name=spec.name,
        image=spec.image,
        envs=dict(spec.envs),
        healthcheck=to_healthcheck(spec.healthcheck if spec.HasField("healthcheck") else None),
        command=tuple(spec.command) if len(spec.command) > 0 else None,
        post_start_on_primary=tuple(spec.post_start_on_primary),
    )


def to_sandbox_handle(handle: service_pb2.SandboxHandle) -> SandboxHandle:
    if handle.last_event_sequence < 0:
        raise ValueError(f"Sequence must be non-negative: {handle.last_event_sequence}")
    created_at = None
    if handle.HasField("created_at"):
        created_at = handle.created_at.ToDatetime(tzinfo=UTC)
    return SandboxHandle(
        sandbox_id=handle.sandbox_id,
        state=map_sandbox_state(handle.state),
        last_event_sequence=int(handle.last_event_sequence),
        companion_containers=tuple(to_companion_container(item) for item in handle.companion_containers),
        labels=dict(handle.labels),
        created_at=created_at,
        image=handle.image,
        error_code=handle.error_code or None,
        error_message=handle.error_message or None,
        state_changed_at=handle.state_changed_at.ToDatetime(tzinfo=UTC) if handle.HasField("state_changed_at") else None,
    )


def to_exec_handle(exec_status: service_pb2.ExecStatus) -> ExecHandle:
    state = map_exec_state(exec_status.state)
    exit_code = exec_status.exit_code if state.is_terminal else None
    return ExecHandle(
        exec_id=exec_status.exec_id,
        sandbox_id=exec_status.sandbox_id,
        state=state,
        command=tuple(exec_status.command),
        cwd=exec_status.cwd,
        env_overrides=dict(exec_status.env_overrides),
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
    sandbox_state = (
        None
        if event.sandbox_state == service_pb2.SANDBOX_STATE_UNSPECIFIED
        else map_sandbox_state(event.sandbox_state)
    )

    sandbox_phase = None
    exec_details = None
    companion_container_details = None

    details_field = event.WhichOneof("details")
    if details_field == "sandbox_phase":
        p = event.sandbox_phase
        sandbox_phase = SandboxPhaseDetails(
            phase=p.phase or None,
            error_code=p.error_code or None,
            error_message=p.error_message or None,
            reason=p.reason or None,
        )
    elif details_field == "exec":
        e = event.exec
        exit_code: int | None = None
        if event.event_type == service_pb2.EXEC_FINISHED or e.exit_code != 0:
            exit_code = e.exit_code
        exec_state = (
            None if e.exec_state == service_pb2.EXEC_STATE_UNSPECIFIED
            else map_exec_state(e.exec_state)
        )
        exec_details = ExecEventDetails(
            exec_id=e.exec_id,
            exit_code=exit_code,
            exec_state=exec_state,
            error_code=e.error_code or None,
            error_message=e.error_message or None,
        )
    elif details_field == "companion_container":
        cc = event.companion_container
        companion_container_details = CompanionContainerEventDetails(
            name=cc.name,
            error_code=cc.error_code or None,
            error_message=cc.error_message or None,
        )

    return SandboxEvent(
        event_id=event.event_id,
        sequence=int(event.sequence),
        sandbox_id=event.sandbox_id,
        event_type=SandboxEventType(event.event_type),
        occurred_at=event.occurred_at.ToDatetime(tzinfo=UTC),
        replay=event.replay,
        snapshot=event.snapshot,
        sandbox_state=sandbox_state,
        sandbox_phase=sandbox_phase,
        exec=exec_details,
        companion_container=companion_container_details,
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
