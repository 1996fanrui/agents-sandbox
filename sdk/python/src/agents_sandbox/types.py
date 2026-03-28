"""Public SDK request, response, and handle types."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime

from .models import ExecState, SandboxEventType, SandboxState


@dataclass(frozen=True, slots=True)
class PingInfo:
    version: str
    daemon: str


@dataclass(frozen=True, slots=True)
class HealthcheckConfig:
    test: tuple[str, ...]
    interval: str | None = None
    timeout: str | None = None
    retries: int | None = None
    start_period: str | None = None
    start_interval: str | None = None


@dataclass(frozen=True, slots=True)
class ServiceSpec:
    name: str
    image: str
    environment: Mapping[str, str] = field(default_factory=dict)
    healthcheck: HealthcheckConfig | None = None
    post_start_on_primary: tuple[str, ...] = ()


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
class SandboxEvent:
    event_id: str
    sequence: int
    sandbox_id: str
    event_type: SandboxEventType
    occurred_at: datetime
    replay: bool = False
    snapshot: bool = False
    phase: str | None = None
    service_name: str | None = None
    error_code: str | None = None
    error_message: str | None = None
    reason: str | None = None
    exec_id: str | None = None
    exit_code: int | None = None
    sandbox_state: SandboxState | None = None
    exec_state: ExecState | None = None


@dataclass(frozen=True, slots=True)
class SandboxHandle:
    sandbox_id: str
    state: SandboxState
    last_event_sequence: int = 0
    required_services: tuple[ServiceSpec, ...] = ()
    optional_services: tuple[ServiceSpec, ...] = ()
    labels: Mapping[str, str] = field(default_factory=dict)

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
    cwd: str | None
    env_overrides: Mapping[str, str]
    exit_code: int | None = None
    error: str | None = None
    last_event_sequence: int = 0
    stdout_log_path: str | None = None
    stderr_log_path: str | None = None


__all__ = [
    "CopySpec",
    "DeleteSandboxesResult",
    "ExecHandle",
    "HealthcheckConfig",
    "MountSpec",
    "PingInfo",
    "SandboxEvent",
    "SandboxHandle",
    "ServiceSpec",
]
