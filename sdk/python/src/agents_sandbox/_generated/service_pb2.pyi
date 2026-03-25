import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class SandboxState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SANDBOX_STATE_UNSPECIFIED: _ClassVar[SandboxState]
    SANDBOX_STATE_PENDING: _ClassVar[SandboxState]
    SANDBOX_STATE_READY: _ClassVar[SandboxState]
    SANDBOX_STATE_FAILED: _ClassVar[SandboxState]
    SANDBOX_STATE_STOPPED: _ClassVar[SandboxState]
    SANDBOX_STATE_DELETING: _ClassVar[SandboxState]
    SANDBOX_STATE_DELETED: _ClassVar[SandboxState]

class EventType(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    EVENT_TYPE_UNSPECIFIED: _ClassVar[EventType]
    SANDBOX_ACCEPTED: _ClassVar[EventType]
    SANDBOX_PREPARING: _ClassVar[EventType]
    SANDBOX_DEPENDENCY_READY: _ClassVar[EventType]
    SANDBOX_READY: _ClassVar[EventType]
    SANDBOX_FAILED: _ClassVar[EventType]
    SANDBOX_STOP_REQUESTED: _ClassVar[EventType]
    SANDBOX_STOPPED: _ClassVar[EventType]
    SANDBOX_DELETE_REQUESTED: _ClassVar[EventType]
    SANDBOX_DELETED: _ClassVar[EventType]
    EXEC_STARTED: _ClassVar[EventType]
    EXEC_FINISHED: _ClassVar[EventType]
    EXEC_FAILED: _ClassVar[EventType]
    EXEC_CANCELLED: _ClassVar[EventType]

class ExecState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    EXEC_STATE_UNSPECIFIED: _ClassVar[ExecState]
    EXEC_STATE_RUNNING: _ClassVar[ExecState]
    EXEC_STATE_FINISHED: _ClassVar[ExecState]
    EXEC_STATE_FAILED: _ClassVar[ExecState]
    EXEC_STATE_CANCELLED: _ClassVar[ExecState]

class ProjectionMountMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    PROJECTION_MOUNT_MODE_UNSPECIFIED: _ClassVar[ProjectionMountMode]
    PROJECTION_MOUNT_MODE_BIND: _ClassVar[ProjectionMountMode]
    PROJECTION_MOUNT_MODE_SHADOW_COPY: _ClassVar[ProjectionMountMode]

class WorkspaceMaterializationMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    WORKSPACE_MATERIALIZATION_MODE_UNSPECIFIED: _ClassVar[WorkspaceMaterializationMode]
    WORKSPACE_MATERIALIZATION_MODE_DURABLE_COPY: _ClassVar[WorkspaceMaterializationMode]
    WORKSPACE_MATERIALIZATION_MODE_BIND: _ClassVar[WorkspaceMaterializationMode]
SANDBOX_STATE_UNSPECIFIED: SandboxState
SANDBOX_STATE_PENDING: SandboxState
SANDBOX_STATE_READY: SandboxState
SANDBOX_STATE_FAILED: SandboxState
SANDBOX_STATE_STOPPED: SandboxState
SANDBOX_STATE_DELETING: SandboxState
SANDBOX_STATE_DELETED: SandboxState
EVENT_TYPE_UNSPECIFIED: EventType
SANDBOX_ACCEPTED: EventType
SANDBOX_PREPARING: EventType
SANDBOX_DEPENDENCY_READY: EventType
SANDBOX_READY: EventType
SANDBOX_FAILED: EventType
SANDBOX_STOP_REQUESTED: EventType
SANDBOX_STOPPED: EventType
SANDBOX_DELETE_REQUESTED: EventType
SANDBOX_DELETED: EventType
EXEC_STARTED: EventType
EXEC_FINISHED: EventType
EXEC_FAILED: EventType
EXEC_CANCELLED: EventType
EXEC_STATE_UNSPECIFIED: ExecState
EXEC_STATE_RUNNING: ExecState
EXEC_STATE_FINISHED: ExecState
EXEC_STATE_FAILED: ExecState
EXEC_STATE_CANCELLED: ExecState
PROJECTION_MOUNT_MODE_UNSPECIFIED: ProjectionMountMode
PROJECTION_MOUNT_MODE_BIND: ProjectionMountMode
PROJECTION_MOUNT_MODE_SHADOW_COPY: ProjectionMountMode
WORKSPACE_MATERIALIZATION_MODE_UNSPECIFIED: WorkspaceMaterializationMode
WORKSPACE_MATERIALIZATION_MODE_DURABLE_COPY: WorkspaceMaterializationMode
WORKSPACE_MATERIALIZATION_MODE_BIND: WorkspaceMaterializationMode

class PingRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class PingResponse(_message.Message):
    __slots__ = ("version", "daemon")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    DAEMON_FIELD_NUMBER: _ClassVar[int]
    version: str
    daemon: str
    def __init__(self, version: _Optional[str] = ..., daemon: _Optional[str] = ...) -> None: ...

class CallerMetadata(_message.Message):
    __slots__ = ("product", "session_id", "run_id")
    PRODUCT_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    RUN_ID_FIELD_NUMBER: _ClassVar[int]
    product: str
    session_id: str
    run_id: str
    def __init__(self, product: _Optional[str] = ..., session_id: _Optional[str] = ..., run_id: _Optional[str] = ...) -> None: ...

class KeyValue(_message.Message):
    __slots__ = ("key", "value")
    KEY_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    key: str
    value: str
    def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...

class CacheProjectionRequest(_message.Message):
    __slots__ = ("cache_id", "enabled")
    CACHE_ID_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    cache_id: str
    enabled: bool
    def __init__(self, cache_id: _Optional[str] = ..., enabled: bool = ...) -> None: ...

class ToolingProjectionRequest(_message.Message):
    __slots__ = ("capability_id", "writable", "source_path", "target_path")
    CAPABILITY_ID_FIELD_NUMBER: _ClassVar[int]
    WRITABLE_FIELD_NUMBER: _ClassVar[int]
    SOURCE_PATH_FIELD_NUMBER: _ClassVar[int]
    TARGET_PATH_FIELD_NUMBER: _ClassVar[int]
    capability_id: str
    writable: bool
    source_path: str
    target_path: str
    def __init__(self, capability_id: _Optional[str] = ..., writable: bool = ..., source_path: _Optional[str] = ..., target_path: _Optional[str] = ...) -> None: ...

class DependencySpec(_message.Message):
    __slots__ = ("dependency_name", "image", "network_alias", "environment")
    DEPENDENCY_NAME_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    NETWORK_ALIAS_FIELD_NUMBER: _ClassVar[int]
    ENVIRONMENT_FIELD_NUMBER: _ClassVar[int]
    dependency_name: str
    image: str
    network_alias: str
    environment: _containers.RepeatedCompositeFieldContainer[KeyValue]
    def __init__(self, dependency_name: _Optional[str] = ..., image: _Optional[str] = ..., network_alias: _Optional[str] = ..., environment: _Optional[_Iterable[_Union[KeyValue, _Mapping]]] = ...) -> None: ...

class MountSpec(_message.Message):
    __slots__ = ("source", "target", "writable")
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    TARGET_FIELD_NUMBER: _ClassVar[int]
    WRITABLE_FIELD_NUMBER: _ClassVar[int]
    source: str
    target: str
    writable: bool
    def __init__(self, source: _Optional[str] = ..., target: _Optional[str] = ..., writable: bool = ...) -> None: ...

class CopySpec(_message.Message):
    __slots__ = ("source", "target", "exclude_patterns")
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    TARGET_FIELD_NUMBER: _ClassVar[int]
    EXCLUDE_PATTERNS_FIELD_NUMBER: _ClassVar[int]
    source: str
    target: str
    exclude_patterns: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, source: _Optional[str] = ..., target: _Optional[str] = ..., exclude_patterns: _Optional[_Iterable[str]] = ...) -> None: ...

class WorkspaceSpec(_message.Message):
    __slots__ = ("path", "mode")
    PATH_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    path: str
    mode: WorkspaceMaterializationMode
    def __init__(self, path: _Optional[str] = ..., mode: _Optional[_Union[WorkspaceMaterializationMode, str]] = ...) -> None: ...

class CreateSpec(_message.Message):
    __slots__ = ("workspace", "cache_projections", "tooling_projections", "dependencies", "image", "mounts", "copies", "builtin_resources")
    WORKSPACE_FIELD_NUMBER: _ClassVar[int]
    CACHE_PROJECTIONS_FIELD_NUMBER: _ClassVar[int]
    TOOLING_PROJECTIONS_FIELD_NUMBER: _ClassVar[int]
    DEPENDENCIES_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    MOUNTS_FIELD_NUMBER: _ClassVar[int]
    COPIES_FIELD_NUMBER: _ClassVar[int]
    BUILTIN_RESOURCES_FIELD_NUMBER: _ClassVar[int]
    workspace: WorkspaceSpec
    cache_projections: _containers.RepeatedCompositeFieldContainer[CacheProjectionRequest]
    tooling_projections: _containers.RepeatedCompositeFieldContainer[ToolingProjectionRequest]
    dependencies: _containers.RepeatedCompositeFieldContainer[DependencySpec]
    image: str
    mounts: _containers.RepeatedCompositeFieldContainer[MountSpec]
    copies: _containers.RepeatedCompositeFieldContainer[CopySpec]
    builtin_resources: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, workspace: _Optional[_Union[WorkspaceSpec, _Mapping]] = ..., cache_projections: _Optional[_Iterable[_Union[CacheProjectionRequest, _Mapping]]] = ..., tooling_projections: _Optional[_Iterable[_Union[ToolingProjectionRequest, _Mapping]]] = ..., dependencies: _Optional[_Iterable[_Union[DependencySpec, _Mapping]]] = ..., image: _Optional[str] = ..., mounts: _Optional[_Iterable[_Union[MountSpec, _Mapping]]] = ..., copies: _Optional[_Iterable[_Union[CopySpec, _Mapping]]] = ..., builtin_resources: _Optional[_Iterable[str]] = ...) -> None: ...

class ResolvedProjectionHandle(_message.Message):
    __slots__ = ("capability_id", "source_path", "target_path", "mount_mode", "writable", "write_back")
    CAPABILITY_ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_PATH_FIELD_NUMBER: _ClassVar[int]
    TARGET_PATH_FIELD_NUMBER: _ClassVar[int]
    MOUNT_MODE_FIELD_NUMBER: _ClassVar[int]
    WRITABLE_FIELD_NUMBER: _ClassVar[int]
    WRITE_BACK_FIELD_NUMBER: _ClassVar[int]
    capability_id: str
    source_path: str
    target_path: str
    mount_mode: ProjectionMountMode
    writable: bool
    write_back: bool
    def __init__(self, capability_id: _Optional[str] = ..., source_path: _Optional[str] = ..., target_path: _Optional[str] = ..., mount_mode: _Optional[_Union[ProjectionMountMode, str]] = ..., writable: bool = ..., write_back: bool = ...) -> None: ...

class SandboxHandle(_message.Message):
    __slots__ = ("sandbox_id", "sandbox_owner", "state", "resolved_tooling_projections", "dependencies", "last_event_cursor")
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_OWNER_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    RESOLVED_TOOLING_PROJECTIONS_FIELD_NUMBER: _ClassVar[int]
    DEPENDENCIES_FIELD_NUMBER: _ClassVar[int]
    LAST_EVENT_CURSOR_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    sandbox_owner: str
    state: SandboxState
    resolved_tooling_projections: _containers.RepeatedCompositeFieldContainer[ResolvedProjectionHandle]
    dependencies: _containers.RepeatedCompositeFieldContainer[DependencySpec]
    last_event_cursor: str
    def __init__(self, sandbox_id: _Optional[str] = ..., sandbox_owner: _Optional[str] = ..., state: _Optional[_Union[SandboxState, str]] = ..., resolved_tooling_projections: _Optional[_Iterable[_Union[ResolvedProjectionHandle, _Mapping]]] = ..., dependencies: _Optional[_Iterable[_Union[DependencySpec, _Mapping]]] = ..., last_event_cursor: _Optional[str] = ...) -> None: ...

class SandboxEvent(_message.Message):
    __slots__ = ("event_id", "sequence", "cursor", "sandbox_id", "event_type", "occurred_at", "replay", "snapshot", "phase", "dependency_name", "error_code", "error_message", "reason", "exec_id", "exit_code", "sandbox_state", "exec_state")
    EVENT_ID_FIELD_NUMBER: _ClassVar[int]
    SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    OCCURRED_AT_FIELD_NUMBER: _ClassVar[int]
    REPLAY_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    PHASE_FIELD_NUMBER: _ClassVar[int]
    DEPENDENCY_NAME_FIELD_NUMBER: _ClassVar[int]
    ERROR_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    EXIT_CODE_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_STATE_FIELD_NUMBER: _ClassVar[int]
    EXEC_STATE_FIELD_NUMBER: _ClassVar[int]
    event_id: str
    sequence: int
    cursor: str
    sandbox_id: str
    event_type: EventType
    occurred_at: _timestamp_pb2.Timestamp
    replay: bool
    snapshot: bool
    phase: str
    dependency_name: str
    error_code: str
    error_message: str
    reason: str
    exec_id: str
    exit_code: int
    sandbox_state: SandboxState
    exec_state: ExecState
    def __init__(self, event_id: _Optional[str] = ..., sequence: _Optional[int] = ..., cursor: _Optional[str] = ..., sandbox_id: _Optional[str] = ..., event_type: _Optional[_Union[EventType, str]] = ..., occurred_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., replay: bool = ..., snapshot: bool = ..., phase: _Optional[str] = ..., dependency_name: _Optional[str] = ..., error_code: _Optional[str] = ..., error_message: _Optional[str] = ..., reason: _Optional[str] = ..., exec_id: _Optional[str] = ..., exit_code: _Optional[int] = ..., sandbox_state: _Optional[_Union[SandboxState, str]] = ..., exec_state: _Optional[_Union[ExecState, str]] = ...) -> None: ...

class ExecStatus(_message.Message):
    __slots__ = ("exec_id", "sandbox_id", "state", "command", "cwd", "env_overrides", "exit_code", "error", "stdout", "stderr")
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    CWD_FIELD_NUMBER: _ClassVar[int]
    ENV_OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    EXIT_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    STDOUT_FIELD_NUMBER: _ClassVar[int]
    STDERR_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    sandbox_id: str
    state: ExecState
    command: _containers.RepeatedScalarFieldContainer[str]
    cwd: str
    env_overrides: _containers.RepeatedCompositeFieldContainer[KeyValue]
    exit_code: int
    error: str
    stdout: str
    stderr: str
    def __init__(self, exec_id: _Optional[str] = ..., sandbox_id: _Optional[str] = ..., state: _Optional[_Union[ExecState, str]] = ..., command: _Optional[_Iterable[str]] = ..., cwd: _Optional[str] = ..., env_overrides: _Optional[_Iterable[_Union[KeyValue, _Mapping]]] = ..., exit_code: _Optional[int] = ..., error: _Optional[str] = ..., stdout: _Optional[str] = ..., stderr: _Optional[str] = ...) -> None: ...

class CreateSandboxRequest(_message.Message):
    __slots__ = ("sandbox_owner", "create_spec", "caller_metadata")
    SANDBOX_OWNER_FIELD_NUMBER: _ClassVar[int]
    CREATE_SPEC_FIELD_NUMBER: _ClassVar[int]
    CALLER_METADATA_FIELD_NUMBER: _ClassVar[int]
    sandbox_owner: str
    create_spec: CreateSpec
    caller_metadata: CallerMetadata
    def __init__(self, sandbox_owner: _Optional[str] = ..., create_spec: _Optional[_Union[CreateSpec, _Mapping]] = ..., caller_metadata: _Optional[_Union[CallerMetadata, _Mapping]] = ...) -> None: ...

class CreateSandboxResponse(_message.Message):
    __slots__ = ("sandbox_id", "initial_state")
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    INITIAL_STATE_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    initial_state: SandboxState
    def __init__(self, sandbox_id: _Optional[str] = ..., initial_state: _Optional[_Union[SandboxState, str]] = ...) -> None: ...

class GetSandboxRequest(_message.Message):
    __slots__ = ("sandbox_id",)
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    def __init__(self, sandbox_id: _Optional[str] = ...) -> None: ...

class GetSandboxResponse(_message.Message):
    __slots__ = ("sandbox",)
    SANDBOX_FIELD_NUMBER: _ClassVar[int]
    sandbox: SandboxHandle
    def __init__(self, sandbox: _Optional[_Union[SandboxHandle, _Mapping]] = ...) -> None: ...

class ListSandboxesRequest(_message.Message):
    __slots__ = ("sandbox_owner", "include_deleted")
    SANDBOX_OWNER_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_DELETED_FIELD_NUMBER: _ClassVar[int]
    sandbox_owner: str
    include_deleted: bool
    def __init__(self, sandbox_owner: _Optional[str] = ..., include_deleted: bool = ...) -> None: ...

class ListSandboxesResponse(_message.Message):
    __slots__ = ("sandboxes",)
    SANDBOXES_FIELD_NUMBER: _ClassVar[int]
    sandboxes: _containers.RepeatedCompositeFieldContainer[SandboxHandle]
    def __init__(self, sandboxes: _Optional[_Iterable[_Union[SandboxHandle, _Mapping]]] = ...) -> None: ...

class ResumeSandboxRequest(_message.Message):
    __slots__ = ("sandbox_id",)
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    def __init__(self, sandbox_id: _Optional[str] = ...) -> None: ...

class StopSandboxRequest(_message.Message):
    __slots__ = ("sandbox_id",)
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    def __init__(self, sandbox_id: _Optional[str] = ...) -> None: ...

class DeleteSandboxRequest(_message.Message):
    __slots__ = ("sandbox_id",)
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    def __init__(self, sandbox_id: _Optional[str] = ...) -> None: ...

class AcceptedResponse(_message.Message):
    __slots__ = ("accepted",)
    ACCEPTED_FIELD_NUMBER: _ClassVar[int]
    accepted: bool
    def __init__(self, accepted: bool = ...) -> None: ...

class SubscribeSandboxEventsRequest(_message.Message):
    __slots__ = ("sandbox_id", "from_cursor", "include_current_snapshot")
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    FROM_CURSOR_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_CURRENT_SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    from_cursor: str
    include_current_snapshot: bool
    def __init__(self, sandbox_id: _Optional[str] = ..., from_cursor: _Optional[str] = ..., include_current_snapshot: bool = ...) -> None: ...

class CreateExecRequest(_message.Message):
    __slots__ = ("sandbox_id", "command", "cwd", "env_overrides", "caller_metadata")
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    CWD_FIELD_NUMBER: _ClassVar[int]
    ENV_OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    CALLER_METADATA_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    command: _containers.RepeatedScalarFieldContainer[str]
    cwd: str
    env_overrides: _containers.RepeatedCompositeFieldContainer[KeyValue]
    caller_metadata: CallerMetadata
    def __init__(self, sandbox_id: _Optional[str] = ..., command: _Optional[_Iterable[str]] = ..., cwd: _Optional[str] = ..., env_overrides: _Optional[_Iterable[_Union[KeyValue, _Mapping]]] = ..., caller_metadata: _Optional[_Union[CallerMetadata, _Mapping]] = ...) -> None: ...

class CreateExecResponse(_message.Message):
    __slots__ = ("exec_id",)
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    def __init__(self, exec_id: _Optional[str] = ...) -> None: ...

class CancelExecRequest(_message.Message):
    __slots__ = ("exec_id",)
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    def __init__(self, exec_id: _Optional[str] = ...) -> None: ...

class GetExecRequest(_message.Message):
    __slots__ = ("exec_id",)
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    def __init__(self, exec_id: _Optional[str] = ...) -> None: ...

class GetExecResponse(_message.Message):
    __slots__ = ("exec",)
    EXEC_FIELD_NUMBER: _ClassVar[int]
    exec: ExecStatus
    def __init__(self, exec: _Optional[_Union[ExecStatus, _Mapping]] = ...) -> None: ...

class ListActiveExecsRequest(_message.Message):
    __slots__ = ("sandbox_id",)
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    def __init__(self, sandbox_id: _Optional[str] = ...) -> None: ...

class ListActiveExecsResponse(_message.Message):
    __slots__ = ("execs",)
    EXECS_FIELD_NUMBER: _ClassVar[int]
    execs: _containers.RepeatedCompositeFieldContainer[ExecStatus]
    def __init__(self, execs: _Optional[_Iterable[_Union[ExecStatus, _Mapping]]] = ...) -> None: ...
