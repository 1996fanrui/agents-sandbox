import datetime

from google.protobuf import duration_pb2 as _duration_pb2
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
    SANDBOX_SERVICE_READY: _ClassVar[EventType]
    SANDBOX_SERVICE_FAILED: _ClassVar[EventType]

class ExecState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    EXEC_STATE_UNSPECIFIED: _ClassVar[ExecState]
    EXEC_STATE_RUNNING: _ClassVar[ExecState]
    EXEC_STATE_FINISHED: _ClassVar[ExecState]
    EXEC_STATE_FAILED: _ClassVar[ExecState]
    EXEC_STATE_CANCELLED: _ClassVar[ExecState]
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
SANDBOX_SERVICE_READY: EventType
SANDBOX_SERVICE_FAILED: EventType
EXEC_STATE_UNSPECIFIED: ExecState
EXEC_STATE_RUNNING: ExecState
EXEC_STATE_FINISHED: ExecState
EXEC_STATE_FAILED: ExecState
EXEC_STATE_CANCELLED: ExecState

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

class HealthcheckConfig(_message.Message):
    __slots__ = ("test", "interval", "timeout", "retries", "start_period", "start_interval")
    TEST_FIELD_NUMBER: _ClassVar[int]
    INTERVAL_FIELD_NUMBER: _ClassVar[int]
    TIMEOUT_FIELD_NUMBER: _ClassVar[int]
    RETRIES_FIELD_NUMBER: _ClassVar[int]
    START_PERIOD_FIELD_NUMBER: _ClassVar[int]
    START_INTERVAL_FIELD_NUMBER: _ClassVar[int]
    test: _containers.RepeatedScalarFieldContainer[str]
    interval: _duration_pb2.Duration
    timeout: _duration_pb2.Duration
    retries: int
    start_period: _duration_pb2.Duration
    start_interval: _duration_pb2.Duration
    def __init__(self, test: _Optional[_Iterable[str]] = ..., interval: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., timeout: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., retries: _Optional[int] = ..., start_period: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., start_interval: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ...) -> None: ...

class ServiceSpec(_message.Message):
    __slots__ = ("name", "image", "envs", "healthcheck", "post_start_on_primary")
    class EnvsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    NAME_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    ENVS_FIELD_NUMBER: _ClassVar[int]
    HEALTHCHECK_FIELD_NUMBER: _ClassVar[int]
    POST_START_ON_PRIMARY_FIELD_NUMBER: _ClassVar[int]
    name: str
    image: str
    envs: _containers.ScalarMap[str, str]
    healthcheck: HealthcheckConfig
    post_start_on_primary: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, name: _Optional[str] = ..., image: _Optional[str] = ..., envs: _Optional[_Mapping[str, str]] = ..., healthcheck: _Optional[_Union[HealthcheckConfig, _Mapping]] = ..., post_start_on_primary: _Optional[_Iterable[str]] = ...) -> None: ...

class CreateSpec(_message.Message):
    __slots__ = ("image", "mounts", "copies", "builtin_tools", "required_services", "optional_services", "labels", "envs", "idle_ttl")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    class EnvsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    MOUNTS_FIELD_NUMBER: _ClassVar[int]
    COPIES_FIELD_NUMBER: _ClassVar[int]
    BUILTIN_TOOLS_FIELD_NUMBER: _ClassVar[int]
    REQUIRED_SERVICES_FIELD_NUMBER: _ClassVar[int]
    OPTIONAL_SERVICES_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    ENVS_FIELD_NUMBER: _ClassVar[int]
    IDLE_TTL_FIELD_NUMBER: _ClassVar[int]
    image: str
    mounts: _containers.RepeatedCompositeFieldContainer[MountSpec]
    copies: _containers.RepeatedCompositeFieldContainer[CopySpec]
    builtin_tools: _containers.RepeatedScalarFieldContainer[str]
    required_services: _containers.RepeatedCompositeFieldContainer[ServiceSpec]
    optional_services: _containers.RepeatedCompositeFieldContainer[ServiceSpec]
    labels: _containers.ScalarMap[str, str]
    envs: _containers.ScalarMap[str, str]
    idle_ttl: _duration_pb2.Duration
    def __init__(self, image: _Optional[str] = ..., mounts: _Optional[_Iterable[_Union[MountSpec, _Mapping]]] = ..., copies: _Optional[_Iterable[_Union[CopySpec, _Mapping]]] = ..., builtin_tools: _Optional[_Iterable[str]] = ..., required_services: _Optional[_Iterable[_Union[ServiceSpec, _Mapping]]] = ..., optional_services: _Optional[_Iterable[_Union[ServiceSpec, _Mapping]]] = ..., labels: _Optional[_Mapping[str, str]] = ..., envs: _Optional[_Mapping[str, str]] = ..., idle_ttl: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ...) -> None: ...

class SandboxHandle(_message.Message):
    __slots__ = ("sandbox_id", "state", "last_event_sequence", "required_services", "optional_services", "labels", "created_at", "image", "error_code", "error_message", "state_changed_at")
    class LabelsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    LAST_EVENT_SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    REQUIRED_SERVICES_FIELD_NUMBER: _ClassVar[int]
    OPTIONAL_SERVICES_FIELD_NUMBER: _ClassVar[int]
    LABELS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    ERROR_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    STATE_CHANGED_AT_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    state: SandboxState
    last_event_sequence: int
    required_services: _containers.RepeatedCompositeFieldContainer[ServiceSpec]
    optional_services: _containers.RepeatedCompositeFieldContainer[ServiceSpec]
    labels: _containers.ScalarMap[str, str]
    created_at: _timestamp_pb2.Timestamp
    image: str
    error_code: str
    error_message: str
    state_changed_at: _timestamp_pb2.Timestamp
    def __init__(self, sandbox_id: _Optional[str] = ..., state: _Optional[_Union[SandboxState, str]] = ..., last_event_sequence: _Optional[int] = ..., required_services: _Optional[_Iterable[_Union[ServiceSpec, _Mapping]]] = ..., optional_services: _Optional[_Iterable[_Union[ServiceSpec, _Mapping]]] = ..., labels: _Optional[_Mapping[str, str]] = ..., created_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., image: _Optional[str] = ..., error_code: _Optional[str] = ..., error_message: _Optional[str] = ..., state_changed_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class SandboxPhaseDetails(_message.Message):
    __slots__ = ("phase", "error_code", "error_message", "reason")
    PHASE_FIELD_NUMBER: _ClassVar[int]
    ERROR_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    phase: str
    error_code: str
    error_message: str
    reason: str
    def __init__(self, phase: _Optional[str] = ..., error_code: _Optional[str] = ..., error_message: _Optional[str] = ..., reason: _Optional[str] = ...) -> None: ...

class ExecEventDetails(_message.Message):
    __slots__ = ("exec_id", "exit_code", "exec_state", "error_code", "error_message")
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    EXIT_CODE_FIELD_NUMBER: _ClassVar[int]
    EXEC_STATE_FIELD_NUMBER: _ClassVar[int]
    ERROR_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    exit_code: int
    exec_state: ExecState
    error_code: str
    error_message: str
    def __init__(self, exec_id: _Optional[str] = ..., exit_code: _Optional[int] = ..., exec_state: _Optional[_Union[ExecState, str]] = ..., error_code: _Optional[str] = ..., error_message: _Optional[str] = ...) -> None: ...

class ServiceEventDetails(_message.Message):
    __slots__ = ("service_name", "error_code", "error_message")
    SERVICE_NAME_FIELD_NUMBER: _ClassVar[int]
    ERROR_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    service_name: str
    error_code: str
    error_message: str
    def __init__(self, service_name: _Optional[str] = ..., error_code: _Optional[str] = ..., error_message: _Optional[str] = ...) -> None: ...

class SandboxEvent(_message.Message):
    __slots__ = ("event_id", "sequence", "sandbox_id", "event_type", "occurred_at", "replay", "snapshot", "sandbox_state", "sandbox_phase", "exec", "service")
    EVENT_ID_FIELD_NUMBER: _ClassVar[int]
    SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    EVENT_TYPE_FIELD_NUMBER: _ClassVar[int]
    OCCURRED_AT_FIELD_NUMBER: _ClassVar[int]
    REPLAY_FIELD_NUMBER: _ClassVar[int]
    SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_STATE_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_PHASE_FIELD_NUMBER: _ClassVar[int]
    EXEC_FIELD_NUMBER: _ClassVar[int]
    SERVICE_FIELD_NUMBER: _ClassVar[int]
    event_id: str
    sequence: int
    sandbox_id: str
    event_type: EventType
    occurred_at: _timestamp_pb2.Timestamp
    replay: bool
    snapshot: bool
    sandbox_state: SandboxState
    sandbox_phase: SandboxPhaseDetails
    exec: ExecEventDetails
    service: ServiceEventDetails
    def __init__(self, event_id: _Optional[str] = ..., sequence: _Optional[int] = ..., sandbox_id: _Optional[str] = ..., event_type: _Optional[_Union[EventType, str]] = ..., occurred_at: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., replay: bool = ..., snapshot: bool = ..., sandbox_state: _Optional[_Union[SandboxState, str]] = ..., sandbox_phase: _Optional[_Union[SandboxPhaseDetails, _Mapping]] = ..., exec: _Optional[_Union[ExecEventDetails, _Mapping]] = ..., service: _Optional[_Union[ServiceEventDetails, _Mapping]] = ...) -> None: ...

class ExecStatus(_message.Message):
    __slots__ = ("exec_id", "sandbox_id", "state", "command", "cwd", "env_overrides", "exit_code", "error", "last_event_sequence")
    class EnvOverridesEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    CWD_FIELD_NUMBER: _ClassVar[int]
    ENV_OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    EXIT_CODE_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    LAST_EVENT_SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    sandbox_id: str
    state: ExecState
    command: _containers.RepeatedScalarFieldContainer[str]
    cwd: str
    env_overrides: _containers.ScalarMap[str, str]
    exit_code: int
    error: str
    last_event_sequence: int
    def __init__(self, exec_id: _Optional[str] = ..., sandbox_id: _Optional[str] = ..., state: _Optional[_Union[ExecState, str]] = ..., command: _Optional[_Iterable[str]] = ..., cwd: _Optional[str] = ..., env_overrides: _Optional[_Mapping[str, str]] = ..., exit_code: _Optional[int] = ..., error: _Optional[str] = ..., last_event_sequence: _Optional[int] = ...) -> None: ...

class CreateSandboxRequest(_message.Message):
    __slots__ = ("create_spec", "sandbox_id", "config_yaml")
    CREATE_SPEC_FIELD_NUMBER: _ClassVar[int]
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    CONFIG_YAML_FIELD_NUMBER: _ClassVar[int]
    create_spec: CreateSpec
    sandbox_id: str
    config_yaml: bytes
    def __init__(self, create_spec: _Optional[_Union[CreateSpec, _Mapping]] = ..., sandbox_id: _Optional[str] = ..., config_yaml: _Optional[bytes] = ...) -> None: ...

class CreateSandboxResponse(_message.Message):
    __slots__ = ("sandbox",)
    SANDBOX_FIELD_NUMBER: _ClassVar[int]
    sandbox: SandboxHandle
    def __init__(self, sandbox: _Optional[_Union[SandboxHandle, _Mapping]] = ...) -> None: ...

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
    __slots__ = ("include_deleted", "label_selector")
    class LabelSelectorEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    INCLUDE_DELETED_FIELD_NUMBER: _ClassVar[int]
    LABEL_SELECTOR_FIELD_NUMBER: _ClassVar[int]
    include_deleted: bool
    label_selector: _containers.ScalarMap[str, str]
    def __init__(self, include_deleted: bool = ..., label_selector: _Optional[_Mapping[str, str]] = ...) -> None: ...

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

class DeleteSandboxesRequest(_message.Message):
    __slots__ = ("label_selector",)
    class LabelSelectorEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    LABEL_SELECTOR_FIELD_NUMBER: _ClassVar[int]
    label_selector: _containers.ScalarMap[str, str]
    def __init__(self, label_selector: _Optional[_Mapping[str, str]] = ...) -> None: ...

class DeleteSandboxesResponse(_message.Message):
    __slots__ = ("deleted_sandbox_ids", "deleted_count")
    DELETED_SANDBOX_IDS_FIELD_NUMBER: _ClassVar[int]
    DELETED_COUNT_FIELD_NUMBER: _ClassVar[int]
    deleted_sandbox_ids: _containers.RepeatedScalarFieldContainer[str]
    deleted_count: int
    def __init__(self, deleted_sandbox_ids: _Optional[_Iterable[str]] = ..., deleted_count: _Optional[int] = ...) -> None: ...

class AcceptedResponse(_message.Message):
    __slots__ = ("accepted",)
    ACCEPTED_FIELD_NUMBER: _ClassVar[int]
    accepted: bool
    def __init__(self, accepted: bool = ...) -> None: ...

class SubscribeSandboxEventsRequest(_message.Message):
    __slots__ = ("sandbox_id", "from_sequence", "include_current_snapshot")
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    FROM_SEQUENCE_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_CURRENT_SNAPSHOT_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    from_sequence: int
    include_current_snapshot: bool
    def __init__(self, sandbox_id: _Optional[str] = ..., from_sequence: _Optional[int] = ..., include_current_snapshot: bool = ...) -> None: ...

class CreateExecRequest(_message.Message):
    __slots__ = ("sandbox_id", "command", "cwd", "env_overrides", "exec_id")
    class EnvOverridesEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    SANDBOX_ID_FIELD_NUMBER: _ClassVar[int]
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    CWD_FIELD_NUMBER: _ClassVar[int]
    ENV_OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    sandbox_id: str
    command: _containers.RepeatedScalarFieldContainer[str]
    cwd: str
    env_overrides: _containers.ScalarMap[str, str]
    exec_id: str
    def __init__(self, sandbox_id: _Optional[str] = ..., command: _Optional[_Iterable[str]] = ..., cwd: _Optional[str] = ..., env_overrides: _Optional[_Mapping[str, str]] = ..., exec_id: _Optional[str] = ...) -> None: ...

class CreateExecResponse(_message.Message):
    __slots__ = ("exec_id", "stdout_log_path", "stderr_log_path")
    EXEC_ID_FIELD_NUMBER: _ClassVar[int]
    STDOUT_LOG_PATH_FIELD_NUMBER: _ClassVar[int]
    STDERR_LOG_PATH_FIELD_NUMBER: _ClassVar[int]
    exec_id: str
    stdout_log_path: str
    stderr_log_path: str
    def __init__(self, exec_id: _Optional[str] = ..., stdout_log_path: _Optional[str] = ..., stderr_log_path: _Optional[str] = ...) -> None: ...

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
