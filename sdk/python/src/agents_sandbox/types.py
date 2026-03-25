"""Public SDK request, response, and handle types."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime

from .models import (
    ExecState,
    ProjectionMountMode,
    SandboxEventType,
    SandboxState,
    WorkspaceMaterializationMode,
)


@dataclass(frozen=True, slots=True)
class PingInfo:
    version: str
    daemon: str


@dataclass(frozen=True, slots=True)
class CallerMetadata:
    product: str
    session_id: str
    run_id: str


@dataclass(frozen=True, slots=True)
class WorkspaceMaterializationSpec:
    mode: WorkspaceMaterializationMode
    source_root: str | None = None


@dataclass(frozen=True, slots=True)
class DependencySpec:
    name: str
    image: str
    network_alias: str | None = None
    environment: Mapping[str, str] = field(default_factory=dict)


@dataclass(frozen=True, slots=True)
class CacheProjectionSpec:
    capability_id: str
    enabled: bool = True


@dataclass(frozen=True, slots=True)
class ToolingProjectionSpec:
    capability_id: str
    writable: bool = False
    source_root: str | None = None
    target_path: str | None = None


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
class CreateSandboxSpec:
    image: str
    workspace: WorkspaceMaterializationSpec | None = None
    dependencies: tuple[DependencySpec, ...] = ()
    cache_projections: tuple[CacheProjectionSpec, ...] = ()
    tooling_projections: tuple[ToolingProjectionSpec, ...] = ()
    mounts: tuple[MountSpec, ...] = ()
    copies: tuple[CopySpec, ...] = ()
    builtin_resources: tuple[str, ...] = ()


@dataclass(frozen=True, slots=True)
class CreateSandboxRequest:
    sandbox_owner: str
    create_spec: CreateSandboxSpec
    caller_metadata: CallerMetadata | None = None


@dataclass(frozen=True, slots=True)
class CreateExecRequest:
    sandbox_id: str
    command: tuple[str, ...]
    cwd: str | None = None
    env_overrides: Mapping[str, str] = field(default_factory=dict)
    caller_metadata: CallerMetadata | None = None


@dataclass(frozen=True, slots=True)
class ResolvedProjectionHandle:
    capability_id: str
    source_path: str | None
    target_path: str | None
    mount_mode: ProjectionMountMode
    writable: bool
    write_back: bool


@dataclass(frozen=True, slots=True)
class SandboxEvent:
    event_id: str
    sequence: int
    cursor: str
    sandbox_id: str
    event_type: SandboxEventType
    occurred_at: datetime
    replay: bool = False
    snapshot: bool = False
    phase: str | None = None
    dependency_name: str | None = None
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
    sandbox_owner: str
    state: SandboxState
    resolved_tooling_projections: tuple[ResolvedProjectionHandle, ...] = ()
    dependencies: tuple[DependencySpec, ...] = ()
    last_event_cursor: str = ""


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
    stdout: str | None = None
    stderr: str | None = None


__all__ = [
    "CacheProjectionSpec",
    "CallerMetadata",
    "CopySpec",
    "CreateExecRequest",
    "CreateSandboxRequest",
    "CreateSandboxSpec",
    "DependencySpec",
    "ExecHandle",
    "MountSpec",
    "PingInfo",
    "ResolvedProjectionHandle",
    "SandboxEvent",
    "SandboxHandle",
    "ToolingProjectionSpec",
    "WorkspaceMaterializationSpec",
]
