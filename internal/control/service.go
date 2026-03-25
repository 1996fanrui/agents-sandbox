package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ServiceConfig struct {
	TransitionDelay        time.Duration
	PollInterval           time.Duration
	IdleTTL                time.Duration
	StateRoot              string
	ArtifactOutputRoot     string
	ArtifactOutputTemplate string
	Version                string
	DaemonName             string
	runtimeBackend         runtimeBackend
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		TransitionDelay:        10 * time.Millisecond,
		PollInterval:           10 * time.Millisecond,
		IdleTTL:                30 * time.Minute,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.jsonl",
		Version:                "0.1.0",
		DaemonName:             "agboxd",
	}
}

type Service struct {
	agboxv1.UnimplementedSandboxServiceServer

	mu       sync.RWMutex
	config   ServiceConfig
	nextBox  uint64
	nextExec uint64
	boxes    map[string]*sandboxRecord
	execs    map[string]string
}

var (
	errArtifactPathEscapesRoot    = errors.New("artifact path escapes configured root")
	errArtifactPathUsesSymlink    = errors.New("artifact path uses symlink boundary")
	errArtifactPathUsesHardlink   = errors.New("artifact path uses hardlink boundary")
	errArtifactTemplateFieldEmpty = errors.New("artifact template field is empty")
)

type sandboxRecord struct {
	handle                    *agboxv1.SandboxHandle
	createSpec                *agboxv1.CreateSpec
	sandboxOwner              string
	dependencies              []*agboxv1.DependencySpec
	runtimeState              *sandboxRuntimeState
	events                    []*agboxv1.SandboxEvent
	execs                     map[string]*agboxv1.ExecStatus
	execCancel                map[string]context.CancelFunc
	execArtifacts             map[string]string
	nextSequence              uint64
	lastTerminalRunFinishedAt time.Time
}

type eventMutation struct {
	phase          string
	dependencyName string
	errorCode      string
	errorMessage   string
	reason         string
	execID         string
	exitCode       int32
	sandboxState   agboxv1.SandboxState
	execState      agboxv1.ExecState
}

func NewService(config ServiceConfig) *Service {
	defaults := DefaultServiceConfig()
	if config.TransitionDelay <= 0 {
		config.TransitionDelay = defaults.TransitionDelay
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaults.PollInterval
	}
	if config.IdleTTL <= 0 {
		config.IdleTTL = defaults.IdleTTL
	}
	if config.ArtifactOutputTemplate == "" {
		config.ArtifactOutputTemplate = defaults.ArtifactOutputTemplate
	}
	if config.Version == "" {
		config.Version = defaults.Version
	}
	if config.DaemonName == "" {
		config.DaemonName = defaults.DaemonName
	}
	if config.runtimeBackend == nil {
		config.runtimeBackend = newDockerRuntimeBackend(config)
	}
	return &Service{
		config: config,
		boxes:  make(map[string]*sandboxRecord),
		execs:  make(map[string]string),
	}
}

func (s *Service) Ping(context.Context, *agboxv1.PingRequest) (*agboxv1.PingResponse, error) {
	return &agboxv1.PingResponse{
		Version: s.config.Version,
		Daemon:  s.config.DaemonName,
	}, nil
}

func (s *Service) CreateSandbox(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	if req.GetSandboxOwner() == "" {
		return nil, status.Error(codes.InvalidArgument, "sandbox_owner is required")
	}
	if req.GetCreateSpec() == nil {
		return nil, status.Error(codes.InvalidArgument, "create_spec is required")
	}
	if req.GetCreateSpec().GetImage() == "" {
		return nil, status.Error(codes.InvalidArgument, "create_spec.image is required")
	}
	if err := validateCreateSpec(req.GetCreateSpec()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, record := range s.boxes {
		if record.sandboxOwner == req.GetSandboxOwner() && record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			return nil, newStatusError(codes.AlreadyExists, ReasonSandboxConflict, "sandbox already exists for owner %s", req.GetSandboxOwner())
		}
	}

	s.nextBox++
	sandboxID := fmt.Sprintf("sandbox-%d", s.nextBox)
	record := &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId:    sandboxID,
			SandboxOwner: req.GetSandboxOwner(),
			State:        agboxv1.SandboxState_SANDBOX_STATE_PENDING,
			Dependencies: cloneDependencies(req.GetCreateSpec().GetDependencies()),
		},
		createSpec:    cloneCreateSpec(req.GetCreateSpec()),
		sandboxOwner:  req.GetSandboxOwner(),
		dependencies:  cloneDependencies(req.GetCreateSpec().GetDependencies()),
		execs:         make(map[string]*agboxv1.ExecStatus),
		execCancel:    make(map[string]context.CancelFunc),
		execArtifacts: make(map[string]string),
	}
	s.boxes[sandboxID] = record
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	})
	go s.completeSandboxCreate(sandboxID)

	return &agboxv1.CreateSandboxResponse{
		SandboxId:    sandboxID,
		InitialState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	}, nil
}

func (s *Service) GetSandbox(_ context.Context, req *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.boxes[req.GetSandboxId()]
	if !ok {
		return nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", req.GetSandboxId())
	}
	return &agboxv1.GetSandboxResponse{Sandbox: cloneHandle(record.handle)}, nil
}

func (s *Service) ListSandboxes(_ context.Context, req *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filterOwner := req.GetSandboxOwner() != ""
	var handles []*agboxv1.SandboxHandle
	for _, record := range s.boxes {
		if !req.GetIncludeDeleted() && record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			continue
		}
		if filterOwner && record.sandboxOwner != req.GetSandboxOwner() {
			continue
		}
		handles = append(handles, cloneHandle(record.handle))
	}
	slices.SortFunc(handles, func(left, right *agboxv1.SandboxHandle) int {
		return strings.Compare(left.GetSandboxId(), right.GetSandboxId())
	})
	return &agboxv1.ListSandboxesResponse{Sandboxes: handles}, nil
}

func (s *Service) ResumeSandbox(_ context.Context, req *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_STOPPED {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, "sandbox %s is not stopped", req.GetSandboxId())
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_PENDING
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	})
	s.mu.Unlock()
	go s.completeSandboxResume(req.GetSandboxId())
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) StopSandbox(_ context.Context, req *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, "sandbox %s is not ready", req.GetSandboxId())
	}
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason: "stop_requested",
	})
	s.mu.Unlock()
	go s.completeSandboxStop(req.GetSandboxId(), "stop_requested")
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) DeleteSandbox(_ context.Context, req *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
		s.mu.Unlock()
		return &agboxv1.AcceptedResponse{Accepted: true}, nil
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETING
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETE_REQUESTED, eventMutation{
		reason:       "delete_requested",
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETING,
	})
	s.mu.Unlock()
	go s.completeSandboxDelete(req.GetSandboxId(), "delete_requested")
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) SubscribeSandboxEvents(req *agboxv1.SubscribeSandboxEventsRequest, stream agboxv1.SandboxService_SubscribeSandboxEventsServer) error {
	s.mu.RLock()
	record, ok := s.boxes[req.GetSandboxId()]
	if !ok {
		s.mu.RUnlock()
		return newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", req.GetSandboxId())
	}
	if req.GetIncludeCurrentSnapshot() {
		for _, event := range snapshotEvents(record) {
			if err := stream.Send(event); err != nil {
				s.mu.RUnlock()
				return err
			}
		}
	}
	nextSequence, err := parseCursor(req.GetSandboxId(), req.GetFromCursor())
	if err != nil {
		s.mu.RUnlock()
		return err
	}
	initialEvents := eventsAfter(record, nextSequence)
	s.mu.RUnlock()

	for _, event := range initialEvents {
		if err := stream.Send(event); err != nil {
			return err
		}
		nextSequence = event.GetSequence()
	}

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
			s.mu.RLock()
			record, ok := s.boxes[req.GetSandboxId()]
			if !ok {
				s.mu.RUnlock()
				return newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", req.GetSandboxId())
			}
			pendingEvents := eventsAfter(record, nextSequence)
			s.mu.RUnlock()
			for _, event := range pendingEvents {
				if err := stream.Send(event); err != nil {
					return err
				}
				nextSequence = event.GetSequence()
			}
		}
	}
}

func (s *Service) CreateExec(_ context.Context, req *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxNotReady, "sandbox %s is not ready", req.GetSandboxId())
	}
	if len(req.GetCommand()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "command must not be empty")
	}
	s.nextExec++
	execID := fmt.Sprintf("exec-%d", s.nextExec)
	if artifactPath, artifactErr := s.prepareExecArtifactPath(record.handle.GetSandboxId(), execID); artifactErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "prepare exec artifact output: %v", artifactErr)
	} else if artifactPath != "" {
		record.execArtifacts[execID] = artifactPath
	}
	execRecord := &agboxv1.ExecStatus{
		ExecId:       execID,
		SandboxId:    req.GetSandboxId(),
		State:        agboxv1.ExecState_EXEC_STATE_RUNNING,
		Command:      slices.Clone(req.GetCommand()),
		Cwd:          req.GetCwd(),
		EnvOverrides: cloneKeyValues(req.GetEnvOverrides()),
	}
	record.execs[execID] = execRecord
	s.execs[execID] = req.GetSandboxId()
	execContext, cancel := context.WithCancel(context.Background())
	record.execCancel[execID] = cancel
	s.appendEventLocked(record, agboxv1.EventType_EXEC_STARTED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_RUNNING,
	})
	go s.completeExec(execContext, execID)
	return &agboxv1.CreateExecResponse{ExecId: execID}, nil
}

func (s *Service) CancelExec(_ context.Context, req *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sandboxID, execRecord, err := s.requireExecLocked(req.GetExecId())
	if err != nil {
		return nil, err
	}
	if isExecTerminal(execRecord.GetState()) {
		return nil, newStatusError(codes.FailedPrecondition, ReasonExecAlreadyTerminal, "exec %s is already terminal", req.GetExecId())
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_CANCELLED
	record := s.boxes[sandboxID]
	if cancel := record.execCancel[req.GetExecId()]; cancel != nil {
		cancel()
		delete(record.execCancel, req.GetExecId())
	}
	record.lastTerminalRunFinishedAt = time.Now().UTC()
	if artifactPath := record.execArtifacts[req.GetExecId()]; artifactPath != "" {
		if err := writeExecArtifact(artifactPath, execRecord); err != nil {
			return nil, status.Errorf(codes.Internal, "write exec artifact: %v", err)
		}
	}
	s.appendEventLocked(record, agboxv1.EventType_EXEC_CANCELLED, eventMutation{
		execID:    req.GetExecId(),
		execState: agboxv1.ExecState_EXEC_STATE_CANCELLED,
	})
	go s.scheduleIdleStop(sandboxID)
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) GetExec(_ context.Context, req *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, execRecord, err := s.requireExecLocked(req.GetExecId())
	if err != nil {
		return nil, err
	}
	return &agboxv1.GetExecResponse{Exec: cloneExec(execRecord)}, nil
}

func (s *Service) ListActiveExecs(_ context.Context, req *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var execs []*agboxv1.ExecStatus
	for _, record := range s.boxes {
		if req.GetSandboxId() != "" && record.handle.GetSandboxId() != req.GetSandboxId() {
			continue
		}
		for _, execRecord := range record.execs {
			if isExecTerminal(execRecord.GetState()) {
				continue
			}
			execs = append(execs, cloneExec(execRecord))
		}
	}
	slices.SortFunc(execs, func(left, right *agboxv1.ExecStatus) int {
		return strings.Compare(left.GetExecId(), right.GetExecId())
	})
	return &agboxv1.ListActiveExecsResponse{Execs: execs}, nil
}

func (s *Service) completeSandboxCreate(sandboxID string) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.Lock()
	record, ok := s.boxes[sandboxID]
	if !ok || record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_PENDING {
		s.mu.Unlock()
		return
	}
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_PREPARING, eventMutation{
		phase:        "materialize",
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	})
	s.mu.Unlock()

	result, err := s.config.runtimeBackend.CreateSandbox(context.Background(), record)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok = s.boxes[sandboxID]
	if !ok {
		if err == nil && result.RuntimeState != nil {
			_ = s.config.runtimeBackend.DeleteSandbox(context.Background(), &sandboxRecord{runtimeState: result.RuntimeState})
		}
		return
	}
	if err != nil {
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_CREATE_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		})
		return
	}
	record.runtimeState = result.RuntimeState
	record.handle.ResolvedToolingProjections = cloneResolvedProjections(result.ResolvedTooling)
	for _, dependencyName := range result.DependencyNames {
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DEPENDENCY_READY, eventMutation{
			dependencyName: dependencyName,
			sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_PENDING,
		})
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	})
}

func (s *Service) completeSandboxResume(sandboxID string) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.RLock()
	record, ok := s.boxes[sandboxID]
	if !ok || record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_PENDING {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	err := s.config.runtimeBackend.ResumeSandbox(context.Background(), record)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok = s.boxes[sandboxID]
	if !ok {
		return
	}
	if err != nil {
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_RESUME_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		})
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	})
}

func (s *Service) completeSandboxStop(sandboxID string, reason string) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.RLock()
	record, ok := s.boxes[sandboxID]
	if !ok || record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	err := s.config.runtimeBackend.StopSandbox(context.Background(), record)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok = s.boxes[sandboxID]
	if !ok {
		return
	}
	if err != nil {
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_STOP_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		})
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_STOPPED
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOPPED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
	})
}

func (s *Service) completeSandboxDelete(sandboxID string, reason string) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.RLock()
	record, ok := s.boxes[sandboxID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	err := s.config.runtimeBackend.DeleteSandbox(context.Background(), record)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok = s.boxes[sandboxID]
	if !ok {
		return
	}
	if err != nil {
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_DELETE_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		})
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETED
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETED,
	})
}

func (s *Service) completeExec(execContext context.Context, execID string) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.RLock()
	sandboxID, execRecord, err := s.requireExecLocked(execID)
	if err != nil || execRecord.GetState() != agboxv1.ExecState_EXEC_STATE_RUNNING {
		s.mu.RUnlock()
		return
	}
	record := s.boxes[sandboxID]
	execSnapshot := cloneExec(execRecord)
	s.mu.RUnlock()

	result, runErr := s.config.runtimeBackend.RunExec(execContext, record, execSnapshot)

	s.mu.Lock()
	defer s.mu.Unlock()
	sandboxID, execRecord, err = s.requireExecLocked(execID)
	if err != nil || execRecord.GetState() != agboxv1.ExecState_EXEC_STATE_RUNNING {
		return
	}
	record = s.boxes[sandboxID]
	delete(record.execCancel, execID)
	if execContext.Err() != nil {
		return
	}
	if runErr != nil {
		execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
		execRecord.ExitCode = result.ExitCode
		execRecord.Stdout = result.Stdout
		execRecord.Stderr = result.Stderr
		execRecord.Error = strings.TrimSpace(strings.Join([]string{result.Stderr, result.Stdout}, "\n"))
		if execRecord.Error == "" {
			execRecord.Error = runErr.Error()
		}
		s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
			execID:       execID,
			execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
			exitCode:     result.ExitCode,
			errorCode:    "EXEC_RUN_FAILED",
			errorMessage: execRecord.GetError(),
		})
		return
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_FINISHED
	execRecord.ExitCode = result.ExitCode
	execRecord.Stdout = result.Stdout
	execRecord.Stderr = result.Stderr
	record.lastTerminalRunFinishedAt = time.Now().UTC()
	if artifactPath := record.execArtifacts[execID]; artifactPath != "" {
		if err := writeExecArtifact(artifactPath, execRecord); err != nil {
			execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
			execRecord.Error = fmt.Sprintf("write exec artifact: %v", err)
			s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
				execID:       execID,
				execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
				errorCode:    "ARTIFACT_OUTPUT_WRITE_FAILED",
				errorMessage: execRecord.GetError(),
			})
			return
		}
	}
	s.appendEventLocked(record, agboxv1.EventType_EXEC_FINISHED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_FINISHED,
		exitCode:  result.ExitCode,
	})
	go s.scheduleIdleStop(sandboxID)
}

func (s *Service) prepareExecArtifactPath(sandboxID string, execID string) (string, error) {
	if s.config.ArtifactOutputRoot == "" || s.config.ArtifactOutputTemplate == "" {
		return "", nil
	}
	return prepareExecOutputPath(
		s.config.ArtifactOutputRoot,
		s.config.ArtifactOutputTemplate,
		map[string]string{
			"sandbox_id": sandboxID,
			"exec_id":    execID,
		},
	)
}

func validateCreateSpec(spec *agboxv1.CreateSpec) error {
	targets := make(map[string]string)
	seenDependencyNames := make(map[string]struct{}, len(spec.GetDependencies()))
	registerTarget := func(kind string, target string) error {
		if target == "" {
			return fmt.Errorf("%s target is required", kind)
		}
		if !filepath.IsAbs(target) {
			return fmt.Errorf("%s target must be absolute: %s", kind, target)
		}
		if existingKind, exists := targets[target]; exists {
			return fmt.Errorf("conflicting target %s between %s and %s", target, existingKind, kind)
		}
		targets[target] = kind
		return nil
	}
	if workspace := spec.GetWorkspace(); workspace != nil && workspace.GetPath() != "" {
		if err := registerTarget("workspace", "/workspace"); err != nil {
			return err
		}
	}
	for _, mount := range spec.GetMounts() {
		if mount.GetSource() == "" {
			return errors.New("mount source is required")
		}
		if err := validateGenericSourcePath("mount", mount.GetSource()); err != nil {
			return err
		}
		if err := registerTarget("mount", mount.GetTarget()); err != nil {
			return err
		}
	}
	for _, copy := range spec.GetCopies() {
		if copy.GetSource() == "" {
			return errors.New("copy source is required")
		}
		if err := validateGenericSourcePath("copy", copy.GetSource()); err != nil {
			return err
		}
		if err := registerTarget("copy", copy.GetTarget()); err != nil {
			return err
		}
	}
	for _, dependency := range spec.GetDependencies() {
		if dependency.GetDependencyName() == "" {
			return errors.New("dependency name is required")
		}
		if dependency.GetImage() == "" {
			return fmt.Errorf("dependency %q image is required", dependency.GetDependencyName())
		}
		if _, exists := seenDependencyNames[dependency.GetDependencyName()]; exists {
			return fmt.Errorf("duplicate dependency name %q", dependency.GetDependencyName())
		}
		seenDependencyNames[dependency.GetDependencyName()] = struct{}{}
	}
	seenBuiltin := make(map[string]struct{}, len(spec.GetBuiltinResources()))
	for _, builtin := range spec.GetBuiltinResources() {
		if builtin == "" {
			return errors.New("builtin resource id must not be empty")
		}
		if _, exists := seenBuiltin[builtin]; exists {
			return fmt.Errorf("duplicate builtin resource %q", builtin)
		}
		if _, ok := profile.CapabilityByID(builtin); !ok {
			return fmt.Errorf("unknown builtin resource %q", builtin)
		}
		seenBuiltin[builtin] = struct{}{}
	}
	return nil
}

func validateGenericSourcePath(kind string, source string) error {
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("%s source path is invalid: %w", kind, err)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("%s source path is invalid: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s source must not be a symlink: %s", kind, sourcePath)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("%s source must be a file or directory: %s", kind, sourcePath)
	}
	return nil
}

func writeExecArtifact(path string, execRecord *agboxv1.ExecStatus) error {
	payload, err := json.Marshal(map[string]any{
		"state":     execRecord.GetState().String(),
		"exit_code": execRecord.GetExitCode(),
		"stdout":    execRecord.GetStdout(),
		"stderr":    execRecord.GetStderr(),
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(string(payload) + "\n")
	return err
}

func prepareExecOutputPath(root string, template string, fields map[string]string) (string, error) {
	relativePath, err := expandArtifactTemplate(template, fields)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relativePath) {
		return "", errArtifactPathEscapesRoot
	}
	cleanRelative := filepath.Clean(relativePath)
	if cleanRelative == "." || cleanRelative == "" || strings.HasPrefix(cleanRelative, "..") {
		return "", errArtifactPathEscapesRoot
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return "", err
	}
	targetPath := filepath.Join(rootAbs, cleanRelative)
	parentPath := filepath.Dir(targetPath)
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return "", err
	}
	parentRealPath, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(rootAbs, parentRealPath) {
		return "", errArtifactPathEscapesRoot
	}
	targetInfo, err := os.Lstat(targetPath)
	if err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return "", errArtifactPathUsesSymlink
		}
		if usesHardlink(targetInfo) {
			return "", errArtifactPathUsesHardlink
		}
		return targetPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return targetPath, nil
}

func expandArtifactTemplate(template string, fields map[string]string) (string, error) {
	resolved := template
	for key, value := range fields {
		if value == "" {
			return "", fmt.Errorf("%w: %s", errArtifactTemplateFieldEmpty, key)
		}
		resolved = strings.ReplaceAll(resolved, "{"+key+"}", value)
	}
	if strings.Contains(resolved, "{") || strings.Contains(resolved, "}") {
		return "", fmt.Errorf("artifact template contains unresolved field: %s", resolved)
	}
	return resolved, nil
}

func pathWithinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..")
}

func usesHardlink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}

func (s *Service) scheduleIdleStop(sandboxID string) {
	if s.config.IdleTTL <= 0 {
		return
	}
	time.Sleep(s.config.IdleTTL)

	s.mu.Lock()
	record, ok := s.boxes[sandboxID]
	if !ok ||
		record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY ||
		record.lastTerminalRunFinishedAt.IsZero() ||
		time.Since(record.lastTerminalRunFinishedAt) < s.config.IdleTTL ||
		hasActiveExec(record) {
		s.mu.Unlock()
		return
	}
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason: "idle_ttl",
	})
	s.mu.Unlock()
	go s.completeSandboxStop(sandboxID, "idle_ttl")
}

func hasActiveExec(record *sandboxRecord) bool {
	for _, execRecord := range record.execs {
		if !isExecTerminal(execRecord.GetState()) {
			return true
		}
	}
	return false
}

func (s *Service) appendEventLocked(record *sandboxRecord, eventType agboxv1.EventType, mutation eventMutation) {
	record.nextSequence++
	event := &agboxv1.SandboxEvent{
		EventId:        fmt.Sprintf("%s-%d", record.handle.GetSandboxId(), record.nextSequence),
		Sequence:       record.nextSequence,
		Cursor:         makeCursor(record.handle.GetSandboxId(), record.nextSequence),
		SandboxId:      record.handle.GetSandboxId(),
		EventType:      eventType,
		OccurredAt:     timestamppb.Now(),
		Replay:         false,
		Snapshot:       false,
		Phase:          mutation.phase,
		DependencyName: mutation.dependencyName,
		ErrorCode:      mutation.errorCode,
		ErrorMessage:   mutation.errorMessage,
		Reason:         mutation.reason,
		ExecId:         mutation.execID,
		ExitCode:       mutation.exitCode,
		SandboxState:   mutation.sandboxState,
		ExecState:      mutation.execState,
	}
	record.events = append(record.events, event)
	record.handle.LastEventCursor = event.GetCursor()
}

func (s *Service) requireSandboxLocked(sandboxID string) (*sandboxRecord, error) {
	record, ok := s.boxes[sandboxID]
	if !ok {
		return nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", sandboxID)
	}
	return record, nil
}

func (s *Service) requireExecLocked(execID string) (string, *agboxv1.ExecStatus, error) {
	sandboxID, ok := s.execs[execID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonExecNotFound, "exec %s was not found", execID)
	}
	record, ok := s.boxes[sandboxID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", sandboxID)
	}
	execRecord, ok := record.execs[execID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonExecNotFound, "exec %s was not found", execID)
	}
	return sandboxID, execRecord, nil
}

func isExecTerminal(state agboxv1.ExecState) bool {
	return state == agboxv1.ExecState_EXEC_STATE_FINISHED ||
		state == agboxv1.ExecState_EXEC_STATE_FAILED ||
		state == agboxv1.ExecState_EXEC_STATE_CANCELLED
}

func makeCursor(sandboxID string, sequence uint64) string {
	return sandboxID + ":" + strconv.FormatUint(sequence, 10)
}

func parseCursor(expectedSandboxID string, cursor string) (uint64, error) {
	if cursor == "" || cursor == "0" {
		return 0, nil
	}
	prefix, rawSequence, ok := strings.Cut(cursor, ":")
	if !ok || prefix != expectedSandboxID {
		return 0, status.Error(codes.InvalidArgument, "cursor must belong to the requested sandbox")
	}
	sequence, err := strconv.ParseUint(rawSequence, 10, 64)
	if err != nil {
		return 0, status.Error(codes.InvalidArgument, "cursor sequence must be numeric")
	}
	return sequence, nil
}

func eventsAfter(record *sandboxRecord, afterSequence uint64) []*agboxv1.SandboxEvent {
	var result []*agboxv1.SandboxEvent
	for _, event := range record.events {
		if event.GetSequence() <= afterSequence {
			continue
		}
		clone := cloneEvent(event)
		clone.Replay = true
		result = append(result, clone)
	}
	return result
}

func snapshotEvents(record *sandboxRecord) []*agboxv1.SandboxEvent {
	var result []*agboxv1.SandboxEvent
	if len(record.events) > 0 {
		event := cloneEvent(record.events[len(record.events)-1])
		event.Replay = true
		event.Snapshot = true
		result = append(result, event)
	}
	for _, execRecord := range record.execs {
		if isExecTerminal(execRecord.GetState()) {
			continue
		}
		result = append(result, &agboxv1.SandboxEvent{
			EventId:      record.handle.GetSandboxId() + "-snapshot-" + execRecord.GetExecId(),
			Sequence:     record.nextSequence,
			Cursor:       record.handle.GetLastEventCursor(),
			SandboxId:    record.handle.GetSandboxId(),
			EventType:    eventTypeForExec(execRecord.GetState()),
			OccurredAt:   timestamppb.Now(),
			Replay:       true,
			Snapshot:     true,
			ExecId:       execRecord.GetExecId(),
			ExecState:    execRecord.GetState(),
			SandboxState: record.handle.GetState(),
		})
	}
	return result
}

func eventTypeForExec(state agboxv1.ExecState) agboxv1.EventType {
	switch state {
	case agboxv1.ExecState_EXEC_STATE_RUNNING:
		return agboxv1.EventType_EXEC_STARTED
	case agboxv1.ExecState_EXEC_STATE_CANCELLED:
		return agboxv1.EventType_EXEC_CANCELLED
	case agboxv1.ExecState_EXEC_STATE_FAILED:
		return agboxv1.EventType_EXEC_FAILED
	default:
		return agboxv1.EventType_EXEC_FINISHED
	}
}

func cloneDependencies(items []*agboxv1.DependencySpec) []*agboxv1.DependencySpec {
	result := make([]*agboxv1.DependencySpec, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.DependencySpec{
			DependencyName: item.GetDependencyName(),
			Image:          item.GetImage(),
			NetworkAlias:   item.GetNetworkAlias(),
			Environment:    cloneKeyValues(item.GetEnvironment()),
		})
	}
	return result
}

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
		Workspace:          cloneWorkspace(spec.GetWorkspace()),
		CacheProjections:   cloneCacheProjections(spec.GetCacheProjections()),
		ToolingProjections: cloneToolingProjections(spec.GetToolingProjections()),
		Dependencies:       cloneDependencies(spec.GetDependencies()),
		Image:              spec.GetImage(),
		Mounts:             cloneMounts(spec.GetMounts()),
		Copies:             cloneCopies(spec.GetCopies()),
		BuiltinResources:   slices.Clone(spec.GetBuiltinResources()),
	}
}

func cloneWorkspace(workspace *agboxv1.WorkspaceSpec) *agboxv1.WorkspaceSpec {
	if workspace == nil {
		return nil
	}
	return &agboxv1.WorkspaceSpec{
		Path: workspace.GetPath(),
		Mode: workspace.GetMode(),
	}
}

func cloneCacheProjections(items []*agboxv1.CacheProjectionRequest) []*agboxv1.CacheProjectionRequest {
	result := make([]*agboxv1.CacheProjectionRequest, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.CacheProjectionRequest{
			CacheId: item.GetCacheId(),
			Enabled: item.GetEnabled(),
		})
	}
	return result
}

func cloneToolingProjections(items []*agboxv1.ToolingProjectionRequest) []*agboxv1.ToolingProjectionRequest {
	result := make([]*agboxv1.ToolingProjectionRequest, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.ToolingProjectionRequest{
			CapabilityId: item.GetCapabilityId(),
			Writable:     item.GetWritable(),
			SourcePath:   item.GetSourcePath(),
			TargetPath:   item.GetTargetPath(),
		})
	}
	return result
}

func cloneKeyValues(items []*agboxv1.KeyValue) []*agboxv1.KeyValue {
	result := make([]*agboxv1.KeyValue, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.KeyValue{Key: item.GetKey(), Value: item.GetValue()})
	}
	return result
}

func resolveTooling(items []*agboxv1.ToolingProjectionRequest) []*agboxv1.ResolvedProjectionHandle {
	result := make([]*agboxv1.ResolvedProjectionHandle, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.ResolvedProjectionHandle{
			CapabilityId: item.GetCapabilityId(),
			SourcePath:   item.GetSourcePath(),
			TargetPath:   item.GetTargetPath(),
			MountMode:    agboxv1.ProjectionMountMode_PROJECTION_MOUNT_MODE_BIND,
			Writable:     item.GetWritable(),
			WriteBack:    item.GetWritable(),
		})
	}
	return result
}

func cloneHandle(handle *agboxv1.SandboxHandle) *agboxv1.SandboxHandle {
	if handle == nil {
		return nil
	}
	return &agboxv1.SandboxHandle{
		SandboxId:                  handle.GetSandboxId(),
		SandboxOwner:               handle.GetSandboxOwner(),
		State:                      handle.GetState(),
		ResolvedToolingProjections: cloneResolvedProjections(handle.GetResolvedToolingProjections()),
		Dependencies:               cloneDependencies(handle.GetDependencies()),
		LastEventCursor:            handle.GetLastEventCursor(),
	}
}

func cloneResolvedProjections(items []*agboxv1.ResolvedProjectionHandle) []*agboxv1.ResolvedProjectionHandle {
	result := make([]*agboxv1.ResolvedProjectionHandle, 0, len(items))
	for _, item := range items {
		result = append(result, &agboxv1.ResolvedProjectionHandle{
			CapabilityId: item.GetCapabilityId(),
			SourcePath:   item.GetSourcePath(),
			TargetPath:   item.GetTargetPath(),
			MountMode:    item.GetMountMode(),
			Writable:     item.GetWritable(),
			WriteBack:    item.GetWriteBack(),
		})
	}
	return result
}

func cloneExec(execRecord *agboxv1.ExecStatus) *agboxv1.ExecStatus {
	if execRecord == nil {
		return nil
	}
	return &agboxv1.ExecStatus{
		ExecId:       execRecord.GetExecId(),
		SandboxId:    execRecord.GetSandboxId(),
		State:        execRecord.GetState(),
		Command:      slices.Clone(execRecord.GetCommand()),
		Cwd:          execRecord.GetCwd(),
		EnvOverrides: cloneKeyValues(execRecord.GetEnvOverrides()),
		ExitCode:     execRecord.GetExitCode(),
		Error:        execRecord.GetError(),
		Stdout:       execRecord.GetStdout(),
		Stderr:       execRecord.GetStderr(),
	}
}

func cloneEvent(event *agboxv1.SandboxEvent) *agboxv1.SandboxEvent {
	if event == nil {
		return nil
	}
	return &agboxv1.SandboxEvent{
		EventId:        event.GetEventId(),
		Sequence:       event.GetSequence(),
		Cursor:         event.GetCursor(),
		SandboxId:      event.GetSandboxId(),
		EventType:      event.GetEventType(),
		OccurredAt:     event.GetOccurredAt(),
		Replay:         event.GetReplay(),
		Snapshot:       event.GetSnapshot(),
		Phase:          event.GetPhase(),
		DependencyName: event.GetDependencyName(),
		ErrorCode:      event.GetErrorCode(),
		ErrorMessage:   event.GetErrorMessage(),
		Reason:         event.GetReason(),
		ExecId:         event.GetExecId(),
		ExitCode:       event.GetExitCode(),
		SandboxState:   event.GetSandboxState(),
		ExecState:      event.GetExecState(),
	}
}

func ListenAndServe(ctx context.Context, socketPath string, service *Service) error {
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
	defer listener.Close()

	server := grpc.NewServer()
	agboxv1.RegisterSandboxServiceServer(server, service)

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	return server.Serve(listener)
}
