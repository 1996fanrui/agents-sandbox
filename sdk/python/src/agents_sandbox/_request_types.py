"""Internal request types used by the SDK client to build protobuf messages."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field

from .types import CopySpec, MountSpec, ServiceSpec


@dataclass(frozen=True, slots=True)
class CreateSandboxSpec:
    image: str | None = None
    mounts: tuple[MountSpec, ...] = ()
    copies: tuple[CopySpec, ...] = ()
    builtin_resources: tuple[str, ...] = ()
    required_services: tuple[ServiceSpec, ...] = ()
    optional_services: tuple[ServiceSpec, ...] = ()
    labels: Mapping[str, str] = field(default_factory=dict)
    envs: Mapping[str, str] = field(default_factory=dict)

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
