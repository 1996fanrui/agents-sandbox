package client

import (
	"fmt"
	"maps"
	"slices"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// SandboxState is the public sandbox lifecycle enum.
type SandboxState int32

const (
	SandboxStateUnspecified SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED)
	SandboxStatePending     SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_PENDING)
	SandboxStateReady       SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_READY)
	SandboxStateFailed      SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_FAILED)
	SandboxStateStopped     SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
	SandboxStateDeleting    SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_DELETING)
	SandboxStateDeleted     SandboxState = SandboxState(agboxv1.SandboxState_SANDBOX_STATE_DELETED)
)

// ExecState is the public exec lifecycle enum.
type ExecState int32

const (
	ExecStateUnspecified ExecState = ExecState(agboxv1.ExecState_EXEC_STATE_UNSPECIFIED)
	ExecStateRunning     ExecState = ExecState(agboxv1.ExecState_EXEC_STATE_RUNNING)
	ExecStateFinished    ExecState = ExecState(agboxv1.ExecState_EXEC_STATE_FINISHED)
	ExecStateFailed      ExecState = ExecState(agboxv1.ExecState_EXEC_STATE_FAILED)
	ExecStateCancelled   ExecState = ExecState(agboxv1.ExecState_EXEC_STATE_CANCELLED)
)

// IsTerminal reports whether the exec state is terminal.
func (s ExecState) IsTerminal() bool {
	return s == ExecStateFinished || s == ExecStateFailed || s == ExecStateCancelled
}

// SandboxEventType is the public sandbox event enum.
type SandboxEventType int32

const (
	SandboxEventTypeUnspecified            SandboxEventType = SandboxEventType(agboxv1.EventType_EVENT_TYPE_UNSPECIFIED)
	SandboxEventTypeSandboxAccepted        SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_ACCEPTED)
	SandboxEventTypeSandboxPreparing       SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_PREPARING)
	SandboxEventTypeSandboxReady           SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_READY)
	SandboxEventTypeSandboxFailed          SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_FAILED)
	SandboxEventTypeSandboxStopRequested   SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_STOP_REQUESTED)
	SandboxEventTypeSandboxStopped         SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_STOPPED)
	SandboxEventTypeSandboxDeleteRequested SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_DELETE_REQUESTED)
	SandboxEventTypeSandboxDeleted         SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_DELETED)
	SandboxEventTypeExecStarted            SandboxEventType = SandboxEventType(agboxv1.EventType_EXEC_STARTED)
	SandboxEventTypeExecFinished           SandboxEventType = SandboxEventType(agboxv1.EventType_EXEC_FINISHED)
	SandboxEventTypeExecFailed             SandboxEventType = SandboxEventType(agboxv1.EventType_EXEC_FAILED)
	SandboxEventTypeExecCancelled          SandboxEventType = SandboxEventType(agboxv1.EventType_EXEC_CANCELLED)
	SandboxEventTypeSandboxServiceReady    SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_SERVICE_READY)
	SandboxEventTypeSandboxServiceFailed   SandboxEventType = SandboxEventType(agboxv1.EventType_SANDBOX_SERVICE_FAILED)
)

// PingInfo is the public ping response type.
type PingInfo struct {
	Version string
	Daemon  string
}

// HealthcheckConfig is the public service healthcheck type.
type HealthcheckConfig struct {
	Test          []string
	Interval      *string
	Timeout       *string
	Retries       *uint32
	StartPeriod   *string
	StartInterval *string
}

// ServiceSpec is the public service declaration type.
type ServiceSpec struct {
	Name               string
	Image              string
	Environment        map[string]string
	Healthcheck        *HealthcheckConfig
	PostStartOnPrimary []string
}

// MountSpec is the public mount declaration type.
type MountSpec struct {
	Source   string
	Target   string
	Writable bool
}

// CopySpec is the public copy declaration type.
type CopySpec struct {
	Source          string
	Target          string
	ExcludePatterns []string
}

// SandboxHandle is the public sandbox state snapshot.
type SandboxHandle struct {
	SandboxID         string
	State             SandboxState
	LastEventSequence uint64
	RequiredServices  []ServiceSpec
	OptionalServices  []ServiceSpec
	Labels            map[string]string
}

// DeleteSandboxesResult is the public bulk delete result.
type DeleteSandboxesResult struct {
	DeletedSandboxIDs []string
	DeletedCount      uint32
}

// ExecHandle is the public exec state snapshot.
type ExecHandle struct {
	ExecID            string
	SandboxID         string
	State             ExecState
	Command           []string
	Cwd               *string
	EnvOverrides      map[string]string
	ExitCode          *int32
	Error             *string
	Stdout            *string
	Stderr            *string
	LastEventSequence uint64
}

// SandboxEvent is the public sandbox event type.
type SandboxEvent struct {
	EventID      string
	Sequence     uint64
	SandboxID    string
	EventType    SandboxEventType
	OccurredAt   time.Time
	Replay       bool
	Snapshot     bool
	Phase        *string
	ServiceName  *string
	ErrorCode    *string
	ErrorMessage *string
	Reason       *string
	ExecID       *string
	ExitCode     *int32
	SandboxState *SandboxState
	ExecState    *ExecState
}

// EventOrError is the channel item exposed by SubscribeSandboxEvents.
type EventOrError struct {
	Event *SandboxEvent
	Err   error
}

func toPingInfo(response *agboxv1.PingResponse) PingInfo {
	return PingInfo{
		Version: response.GetVersion(),
		Daemon:  response.GetDaemon(),
	}
}

func toSandboxHandle(handle *agboxv1.SandboxHandle) (SandboxHandle, error) {
	if handle == nil {
		return SandboxHandle{}, fmt.Errorf("sandbox handle is required")
	}
	return SandboxHandle{
		SandboxID:         handle.GetSandboxId(),
		State:             SandboxState(handle.GetState()),
		LastEventSequence: handle.GetLastEventSequence(),
		RequiredServices:  toServices(handle.GetRequiredServices()),
		OptionalServices:  toServices(handle.GetOptionalServices()),
		Labels:            cloneStringMap(handle.GetLabels()),
	}, nil
}

func toExecHandle(execStatus *agboxv1.ExecStatus) ExecHandle {
	state := ExecState(execStatus.GetState())
	var exitCode *int32
	if state.IsTerminal() {
		exitCode = int32Ptr(execStatus.GetExitCode())
	}
	return ExecHandle{
		ExecID:            execStatus.GetExecId(),
		SandboxID:         execStatus.GetSandboxId(),
		State:             state,
		Command:           slices.Clone(execStatus.GetCommand()),
		Cwd:               emptyStringPtr(execStatus.GetCwd()),
		EnvOverrides:      keyValuesToMap(execStatus.GetEnvOverrides()),
		ExitCode:          exitCode,
		Error:             emptyStringPtr(execStatus.GetError()),
		Stdout:            emptyStringPtr(execStatus.GetStdout()),
		Stderr:            emptyStringPtr(execStatus.GetStderr()),
		LastEventSequence: execStatus.GetLastEventSequence(),
	}
}

type execSnapshot struct {
	handle            ExecHandle
	lastEventSequence uint64
}

func toExecSnapshot(response *agboxv1.GetExecResponse) (execSnapshot, error) {
	if response == nil || response.GetExec() == nil {
		return execSnapshot{}, fmt.Errorf("get exec response is missing exec")
	}
	handle := toExecHandle(response.GetExec())
	lastEventSequence := handle.LastEventSequence
	if lastEventSequence == 0 {
		return execSnapshot{}, fmt.Errorf("get exec response for exec %s is missing last_event_sequence", handle.ExecID)
	}
	return execSnapshot{
		handle:            handle,
		lastEventSequence: lastEventSequence,
	}, nil
}

func toSandboxEvent(event *agboxv1.SandboxEvent) (SandboxEvent, error) {
	if event == nil {
		return SandboxEvent{}, fmt.Errorf("sandbox event is required")
	}
	if event.GetOccurredAt() == nil {
		return SandboxEvent{}, fmt.Errorf("sandbox event %s is missing occurred_at", fallbackID(event.GetEventId()))
	}
	var exitCode *int32
	if event.GetEventType() == agboxv1.EventType_EXEC_FINISHED || event.GetExitCode() != 0 {
		exitCode = int32Ptr(event.GetExitCode())
	}

	return SandboxEvent{
		EventID:      event.GetEventId(),
		Sequence:     event.GetSequence(),
		SandboxID:    event.GetSandboxId(),
		EventType:    SandboxEventType(event.GetEventType()),
		OccurredAt:   event.GetOccurredAt().AsTime().UTC(),
		Replay:       event.GetReplay(),
		Snapshot:     event.GetSnapshot(),
		Phase:        emptyStringPtr(event.GetPhase()),
		ServiceName:  emptyStringPtr(event.GetServiceName()),
		ErrorCode:    emptyStringPtr(event.GetErrorCode()),
		ErrorMessage: emptyStringPtr(event.GetErrorMessage()),
		Reason:       emptyStringPtr(event.GetReason()),
		ExecID:       emptyStringPtr(event.GetExecId()),
		ExitCode:     exitCode,
		SandboxState: sandboxStatePtr(event.GetSandboxState()),
		ExecState:    execStatePtr(event.GetExecState()),
	}, nil
}

func toProtoMount(spec MountSpec) *agboxv1.MountSpec {
	return &agboxv1.MountSpec{
		Source:   spec.Source,
		Target:   spec.Target,
		Writable: spec.Writable,
	}
}

func toProtoCopy(spec CopySpec) *agboxv1.CopySpec {
	return &agboxv1.CopySpec{
		Source:          spec.Source,
		Target:          spec.Target,
		ExcludePatterns: slices.Clone(spec.ExcludePatterns),
	}
}

func toProtoService(spec ServiceSpec) *agboxv1.ServiceSpec {
	return &agboxv1.ServiceSpec{
		Name:               spec.Name,
		Image:              spec.Image,
		Environment:        mapToKeyValues(spec.Environment),
		Healthcheck:        toProtoHealthcheck(spec.Healthcheck),
		PostStartOnPrimary: slices.Clone(spec.PostStartOnPrimary),
	}
}

func toProtoHealthcheck(config *HealthcheckConfig) *agboxv1.HealthcheckConfig {
	if config == nil {
		return nil
	}
	return &agboxv1.HealthcheckConfig{
		Test:          slices.Clone(config.Test),
		Interval:      valueOrEmpty(config.Interval),
		Timeout:       valueOrEmpty(config.Timeout),
		Retries:       valueOrZero(config.Retries),
		StartPeriod:   valueOrEmpty(config.StartPeriod),
		StartInterval: valueOrEmpty(config.StartInterval),
	}
}

func toServices(specs []*agboxv1.ServiceSpec) []ServiceSpec {
	result := make([]ServiceSpec, 0, len(specs))
	for _, spec := range specs {
		if spec == nil {
			continue
		}
		result = append(result, ServiceSpec{
			Name:               spec.GetName(),
			Image:              spec.GetImage(),
			Environment:        keyValuesToMap(spec.GetEnvironment()),
			Healthcheck:        toHealthcheck(spec.GetHealthcheck()),
			PostStartOnPrimary: slices.Clone(spec.GetPostStartOnPrimary()),
		})
	}
	return result
}

func toHealthcheck(config *agboxv1.HealthcheckConfig) *HealthcheckConfig {
	if config == nil {
		return nil
	}
	return &HealthcheckConfig{
		Test:          slices.Clone(config.GetTest()),
		Interval:      emptyStringPtr(config.GetInterval()),
		Timeout:       emptyStringPtr(config.GetTimeout()),
		Retries:       zeroUint32Ptr(config.GetRetries()),
		StartPeriod:   emptyStringPtr(config.GetStartPeriod()),
		StartInterval: emptyStringPtr(config.GetStartInterval()),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	return maps.Clone(values)
}

func mapToKeyValues(values map[string]string) []*agboxv1.KeyValue {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]*agboxv1.KeyValue, 0, len(keys))
	for _, key := range keys {
		result = append(result, &agboxv1.KeyValue{
			Key:   key,
			Value: values[key],
		})
	}
	return result
}

func keyValuesToMap(values []*agboxv1.KeyValue) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result[value.GetKey()] = value.GetValue()
	}
	return result
}

func emptyStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func int32Ptr(value int32) *int32 {
	return &value
}

func sandboxStatePtr(state agboxv1.SandboxState) *SandboxState {
	if state == agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
		return nil
	}
	value := SandboxState(state)
	return &value
}

func execStatePtr(state agboxv1.ExecState) *ExecState {
	if state == agboxv1.ExecState_EXEC_STATE_UNSPECIFIED {
		return nil
	}
	value := ExecState(state)
	return &value
}

func zeroUint32Ptr(value uint32) *uint32 {
	if value == 0 {
		return nil
	}
	return &value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *uint32) uint32 {
	if value == nil {
		return 0
	}
	return *value
}

func fallbackID(value string) string {
	if value == "" {
		return "<unknown>"
	}
	return value
}
