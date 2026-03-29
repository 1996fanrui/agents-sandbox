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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
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
	cloned := &agboxv1.CreateSpec{
		Image:            spec.GetImage(),
		Labels:           cloneStringMap(spec.GetLabels()),
		Mounts:           cloneMounts(spec.GetMounts()),
		Copies:           cloneCopies(spec.GetCopies()),
		BuiltinTools:     slices.Clone(spec.GetBuiltinTools()),
		RequiredServices: cloneServiceSpecs(spec.GetRequiredServices()),
		OptionalServices: cloneServiceSpecs(spec.GetOptionalServices()),
		Envs:             cloneStringMap(spec.GetEnvs()),
	}
	if spec.GetIdleTtl() != nil {
		cloned.IdleTtl = durationpb.New(spec.GetIdleTtl().AsDuration())
	}
	return cloned
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

func cloneHealthcheck(healthcheck *agboxv1.HealthcheckConfig) *agboxv1.HealthcheckConfig {
	if healthcheck == nil {
		return nil
	}
	cloned := proto.Clone(healthcheck).(*agboxv1.HealthcheckConfig)
	return cloned
}

func cloneServiceSpecs(items []*agboxv1.ServiceSpec) []*agboxv1.ServiceSpec {
	result := make([]*agboxv1.ServiceSpec, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.ServiceSpec{
			Name:               item.GetName(),
			Image:              item.GetImage(),
			Envs:               cloneStringMap(item.GetEnvs()),
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
		CreatedAt:         handle.GetCreatedAt(),
		Image:             handle.GetImage(),
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
		EnvOverrides:      cloneStringMap(execRecord.GetEnvOverrides()),
		ExitCode:          execRecord.GetExitCode(),
		Error:             execRecord.GetError(),
		LastEventSequence: execRecord.GetLastEventSequence(),
	}
}

func cloneEvent(event *agboxv1.SandboxEvent) *agboxv1.SandboxEvent {
	if event == nil {
		return nil
	}
	cloned := &agboxv1.SandboxEvent{
		EventId:      event.GetEventId(),
		Sequence:     event.GetSequence(),
		SandboxId:    event.GetSandboxId(),
		EventType:    event.GetEventType(),
		OccurredAt:   event.GetOccurredAt(),
		Replay:       event.GetReplay(),
		Snapshot:     event.GetSnapshot(),
		SandboxState: event.GetSandboxState(),
	}
	switch d := event.GetDetails().(type) {
	case *agboxv1.SandboxEvent_SandboxPhase:
		if d != nil && d.SandboxPhase != nil {
			cloned.Details = &agboxv1.SandboxEvent_SandboxPhase{
				SandboxPhase: &agboxv1.SandboxPhaseDetails{
					Phase:        d.SandboxPhase.GetPhase(),
					ErrorCode:    d.SandboxPhase.GetErrorCode(),
					ErrorMessage: d.SandboxPhase.GetErrorMessage(),
					Reason:       d.SandboxPhase.GetReason(),
				},
			}
		}
	case *agboxv1.SandboxEvent_Exec:
		if d != nil && d.Exec != nil {
			cloned.Details = &agboxv1.SandboxEvent_Exec{
				Exec: &agboxv1.ExecEventDetails{
					ExecId:       d.Exec.GetExecId(),
					ExitCode:     d.Exec.GetExitCode(),
					ExecState:    d.Exec.GetExecState(),
					ErrorCode:    d.Exec.GetErrorCode(),
					ErrorMessage: d.Exec.GetErrorMessage(),
				},
			}
		}
	case *agboxv1.SandboxEvent_Service:
		if d != nil && d.Service != nil {
			cloned.Details = &agboxv1.SandboxEvent_Service{
				Service: &agboxv1.ServiceEventDetails{
					ServiceName:  d.Service.GetServiceName(),
					ErrorCode:    d.Service.GetErrorCode(),
					ErrorMessage: d.Service.GetErrorMessage(),
				},
			}
		}
	}
	return cloned
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
