"""Internal request types used by the SDK client to build protobuf messages."""

from __future__ import annotations

import datetime
from collections.abc import Mapping
from dataclasses import dataclass, field

from .types import CompanionContainerSpec, CopySpec, MountSpec, PortMapping


@dataclass(frozen=True, slots=True)
class CreateSandboxSpec:
    image: str | None = None
    mounts: tuple[MountSpec, ...] = ()
    copies: tuple[CopySpec, ...] = ()
    ports: tuple[PortMapping, ...] = ()
    builtin_tools: tuple[str, ...] = ()
    companion_containers: tuple[CompanionContainerSpec, ...] = ()
    labels: Mapping[str, str] = field(default_factory=dict)
    envs: Mapping[str, str] = field(default_factory=dict)
    idle_ttl: datetime.timedelta | None = None

    def __post_init__(self) -> None:
        object.__setattr__(self, "labels", dict(self.labels))
        object.__setattr__(self, "envs", dict(self.envs))


@dataclass(frozen=True, slots=True)
class CreateSandboxRequest:
    create_spec: CreateSandboxSpec
    sandbox_id: str | None = None
    config_yaml: bytes = b""


@dataclass(frozen=True, slots=True)
class CreateExecRequest:
    sandbox_id: str
    command: tuple[str, ...]
    exec_id: str | None = None
    cwd: str | None = None
    env_overrides: Mapping[str, str] = field(default_factory=dict)
