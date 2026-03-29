package control

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc"
)

func cloneMounts(items []*agboxv1.MountSpec) []*agboxv1.MountSpec {
	result := make([]*agboxv1.MountSpec, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.MountSpec{
			Source:   item.GetSource(),
			Target:   item.GetTarget(),
			Writable: item.GetWritable(),
		})
	}
	return result
}

func cloneCopies(items []*agboxv1.CopySpec) []*agboxv1.CopySpec {
	result := make([]*agboxv1.CopySpec, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.CopySpec{
			Source:          item.GetSource(),
			Target:          item.GetTarget(),
			ExcludePatterns: slices.Clone(item.GetExcludePatterns()),
		})
	}
	return result
}

func cloneCreateSpec(spec *agboxv1.CreateSpec) *agboxv1.CreateSpec {
	if spec == nil {
		return &agboxv1.CreateSpec{}
	}
	return &agboxv1.CreateSpec{
		Image:            spec.GetImage(),
		Labels:           cloneStringMap(spec.GetLabels()),
		Mounts:           cloneMounts(spec.GetMounts()),
		Copies:           cloneCopies(spec.GetCopies()),
		BuiltinTools: slices.Clone(spec.GetBuiltinTools()),
		RequiredServices: cloneServiceSpecs(spec.GetRequiredServices()),
		OptionalServices: cloneServiceSpecs(spec.GetOptionalServices()),
		Envs:             cloneKeyValues(spec.GetEnvs()),
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneKeyValues(items []*agboxv1.KeyValue) []*agboxv1.KeyValue {
	result := make([]*agboxv1.KeyValue, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.KeyValue{Key: item.GetKey(), Value: item.GetValue()})
	}
	return result
}

func cloneHealthcheck(healthcheck *agboxv1.HealthcheckConfig) *agboxv1.HealthcheckConfig {
	if healthcheck == nil {
		return nil
	}
	return &agboxv1.HealthcheckConfig{
		Test:          slices.Clone(healthcheck.GetTest()),
		Interval:      healthcheck.GetInterval(),
		Timeout:       healthcheck.GetTimeout(),
		Retries:       healthcheck.GetRetries(),
		StartPeriod:   healthcheck.GetStartPeriod(),
		StartInterval: healthcheck.GetStartInterval(),
	}
}

func cloneServiceSpecs(items []*agboxv1.ServiceSpec) []*agboxv1.ServiceSpec {
	result := make([]*agboxv1.ServiceSpec, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.ServiceSpec{
			Name:               item.GetName(),
			Image:              item.GetImage(),
			Environment:        cloneKeyValues(item.GetEnvironment()),
			Healthcheck:        cloneHealthcheck(item.GetHealthcheck()),
			PostStartOnPrimary: slices.Clone(item.GetPostStartOnPrimary()),
		})
	}
	return result
}

func cloneHandle(handle *agboxv1.SandboxHandle) *agboxv1.SandboxHandle {
	if handle == nil {
		return nil
	}
	return &agboxv1.SandboxHandle{
		SandboxId:         handle.GetSandboxId(),
		State:             handle.GetState(),
		LastEventSequence: handle.GetLastEventSequence(),
		Labels:            cloneStringMap(handle.GetLabels()),
		RequiredServices:  cloneServiceSpecs(handle.GetRequiredServices()),
		OptionalServices:  cloneServiceSpecs(handle.GetOptionalServices()),
	}
}

func matchesLabelSelector(labels map[string]string, selector map[string]string) bool {
	for key, want := range selector {
		if got, ok := labels[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func (s *Service) beginSandboxDeleteLocked(record *sandboxRecord, reason string) (bool, error) {
	if record == nil || record.handle == nil {
		return false, nil
	}
	switch record.handle.GetState() {
	case agboxv1.SandboxState_SANDBOX_STATE_DELETED, agboxv1.SandboxState_SANDBOX_STATE_DELETING:
		return false, nil
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETE_REQUESTED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETING,
	}); err != nil {
		return false, err
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETING
	return true, nil
}

func cloneExec(execRecord *agboxv1.ExecStatus) *agboxv1.ExecStatus {
	if execRecord == nil {
		return nil
	}
	return &agboxv1.ExecStatus{
		ExecId:            execRecord.GetExecId(),
		SandboxId:         execRecord.GetSandboxId(),
		State:             execRecord.GetState(),
		Command:           slices.Clone(execRecord.GetCommand()),
		Cwd:               execRecord.GetCwd(),
		EnvOverrides:      cloneKeyValues(execRecord.GetEnvOverrides()),
		ExitCode:          execRecord.GetExitCode(),
		Error:             execRecord.GetError(),
		LastEventSequence: execRecord.GetLastEventSequence(),
	}
}

func cloneEvent(event *agboxv1.SandboxEvent) *agboxv1.SandboxEvent {
	if event == nil {
		return nil
	}
	return &agboxv1.SandboxEvent{
		EventId:      event.GetEventId(),
		Sequence:     event.GetSequence(),
		SandboxId:    event.GetSandboxId(),
		EventType:    event.GetEventType(),
		OccurredAt:   event.GetOccurredAt(),
		Replay:       event.GetReplay(),
		Snapshot:     event.GetSnapshot(),
		Phase:        event.GetPhase(),
		ErrorCode:    event.GetErrorCode(),
		ErrorMessage: event.GetErrorMessage(),
		Reason:       event.GetReason(),
		ExecId:       event.GetExecId(),
		ExitCode:     event.GetExitCode(),
		SandboxState: event.GetSandboxState(),
		ExecState:    event.GetExecState(),
		ServiceName:  event.GetServiceName(),
	}
}

func ListenAndServe(ctx context.Context, socketPath string, service *Service, logger *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(socketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	logger.Info("listening", slog.String("socket_path", socketPath))
	defer listener.Close()

	server := grpc.NewServer()
	agboxv1.RegisterSandboxServiceServer(server, service)

	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		server.GracefulStop()
	}()

	return server.Serve(listener)
}
