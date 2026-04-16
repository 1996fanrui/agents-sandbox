"""Public SDK request, response, and handle types."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from datetime import datetime, timedelta

from .models import ExecState, SandboxEventType, SandboxState


@dataclass(frozen=True, slots=True)
class PingInfo:
    version: str
    daemon: str


@dataclass(frozen=True, slots=True)
class HealthcheckConfig:
    test: tuple[str, ...]
    interval: timedelta | None = None
    timeout: timedelta | None = None
    retries: int | None = None
    start_period: timedelta | None = None
    start_interval: timedelta | None = None


@dataclass(frozen=True, slots=True)
class CompanionContainerSpec:
    name: str
    image: str
    envs: Mapping[str, str] = field(default_factory=dict)
    healthcheck: HealthcheckConfig | None = None
    # ``None`` means "omit the field and inherit the image's built-in CMD".
    # An explicit empty sequence is rejected; proto3 cannot distinguish "unset"
    # from "explicitly empty" on repeated fields, so SDK entry layers own that
    # check (matching the primary ``command`` validation pattern).
    command: Sequence[str] | None = None
    post_start_on_primary: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if self.command is not None:
            command_tuple = tuple(self.command)
            if len(command_tuple) == 0:
                raise ValueError(
                    f"companion_containers[{self.name!r}].command: "
                    "empty array is not allowed, omit the field to use the "
                    "default image CMD"
                )
            for index, element in enumerate(command_tuple):
                if element == "":
                    raise ValueError(
                        f"companion_containers[{self.name!r}].command[{index}]: "
                        "empty string entry is not allowed"
                    )
            object.__setattr__(self, "command", command_tuple)


@dataclass(frozen=True, slots=True)
class MountSpec:
    source: str
    target: str
    writable: bool = False


@dataclass(frozen=True, slots=True)
class CopySpec:
    source: str
    target: str
    exclude_patterns: tuple[str, ...] = ()


@dataclass(frozen=True, slots=True)
class PortMapping:
    container_port: int
    host_port: int
    protocol: str = "tcp"


@dataclass(frozen=True, slots=True)
class SandboxPhaseDetails:
    phase: str | None = None
    error_code: str | None = None
    error_message: str | None = None
    reason: str | None = None


@dataclass(frozen=True, slots=True)
class ExecEventDetails:
    exec_id: str = ""
    exit_code: int | None = None
    exec_state: ExecState | None = None
    error_code: str | None = None
    error_message: str | None = None


@dataclass(frozen=True, slots=True)
class CompanionContainerEventDetails:
    name: str = ""
    error_code: str | None = None
    error_message: str | None = None


@dataclass(frozen=True, slots=True)
class SandboxEvent:
    event_id: str
    sequence: int
    sandbox_id: str
    event_type: SandboxEventType
    occurred_at: datetime
    replay: bool = False
    snapshot: bool = False
    sandbox_state: SandboxState | None = None
    sandbox_phase: SandboxPhaseDetails | None = None
    exec: ExecEventDetails | None = None
    companion_container: CompanionContainerEventDetails | None = None


@dataclass(frozen=True, slots=True)
class SandboxHandle:
    sandbox_id: str
    state: SandboxState
    last_event_sequence: int = 0
    companion_containers: tuple[CompanionContainerSpec, ...] = ()
    labels: Mapping[str, str] = field(default_factory=dict)
    created_at: datetime | None = None
    image: str = ""
    error_code: str | None = None
    error_message: str | None = None
    state_changed_at: datetime | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "labels", dict(self.labels))


@dataclass(frozen=True, slots=True)
class DeleteSandboxesResult:
    deleted_sandbox_ids: tuple[str, ...]
    deleted_count: int


@dataclass(frozen=True, slots=True)
class ExecHandle:
    exec_id: str
    sandbox_id: str
    state: ExecState
    command: tuple[str, ...]
    cwd: str
    env_overrides: Mapping[str, str]
    exit_code: int | None = None
    error: str | None = None
    last_event_sequence: int = 0
    stdout_log_path: str | None = None
    stderr_log_path: str | None = None


__all__ = [
    "CompanionContainerEventDetails",
    "CompanionContainerSpec",
    "CopySpec",
    "DeleteSandboxesResult",
    "ExecEventDetails",
    "ExecHandle",
    "HealthcheckConfig",
    "MountSpec",
    "PingInfo",
    "PortMapping",
    "SandboxEvent",
    "SandboxHandle",
    "SandboxPhaseDetails",
]
