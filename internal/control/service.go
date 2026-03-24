package control

import (
	"context"
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

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/proto/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/audit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ServiceConfig struct {
	ReplayLimit            int
	TransitionDelay        time.Duration
	PollInterval           time.Duration
	IdleTTL                time.Duration
	StateRoot              string
	ArtifactOutputRoot     string
	ArtifactOutputTemplate string
	Version                string
	DaemonName             string
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		ReplayLimit:            32,
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
	ownerKey                  string
	dependencies              []*agboxv1.DependencySpec
	events                    []*agboxv1.SandboxEvent
	execs                     map[string]*agboxv1.ExecStatus
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
	actionReason   audit.ActionReason
	actionStrategy audit.ActionStrategy
}

func NewService(config ServiceConfig) *Service {
	defaults := DefaultServiceConfig()
	if config.ReplayLimit <= 0 {
		config.ReplayLimit = defaults.ReplayLimit
	}
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
	if req.GetSandboxOwner() == nil || req.GetSandboxOwner().GetProduct() == "" || req.GetSandboxOwner().GetOwnerType() == "" || req.GetSandboxOwner().GetOwnerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "sandbox_owner is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ownerKey := makeOwnerKey(req.GetSandboxOwner())
	for _, record := range s.boxes {
		if record.ownerKey == ownerKey && record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			return nil, newStatusError(codes.AlreadyExists, ReasonSandboxConflict, "sandbox already exists for owner %s", ownerKey)
		}
	}

	s.nextBox++
	sandboxID := fmt.Sprintf("sandbox-%d", s.nextBox)
	record := &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId:                  sandboxID,
			Owner:                      cloneOwner(req.GetSandboxOwner()),
			State:                      agboxv1.SandboxState_SANDBOX_STATE_PENDING,
			ResolvedToolingProjections: resolveTooling(req.GetCreateSpec().GetToolingProjections()),
			Dependencies:               cloneDependencies(req.GetCreateSpec().GetDependencies()),
		},
		ownerKey:      ownerKey,
		dependencies:  cloneDependencies(req.GetCreateSpec().GetDependencies()),
		execs:         make(map[string]*agboxv1.ExecStatus),
		execArtifacts: make(map[string]string),
	}
	s.boxes[sandboxID] = record
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_PENDING,
		actionReason:   audit.ActionReasonStartNewSession,
		actionStrategy: audit.ActionStrategyMaterializeNewSessionRuntime,
	})
	go s.completeSandboxCreate(
		sandboxID,
		audit.ActionReasonStartNewSession,
		audit.ActionStrategyMaterializeNewSessionRuntime,
	)

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

	filterOwner := req.GetOwner() != nil && req.GetOwner().GetOwnerId() != ""
	expectedOwner := makeOwnerKey(req.GetOwner())
	var handles []*agboxv1.SandboxHandle
	for _, record := range s.boxes {
		if !req.GetIncludeDeleted() && record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			continue
		}
		if filterOwner && record.ownerKey != expectedOwner {
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
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_PENDING,
		actionReason:   audit.ActionReasonResumeSession,
		actionStrategy: audit.ActionStrategyResumeExistingRuntime,
	})
	s.mu.Unlock()
	go s.completeSandboxCreate(
		req.GetSandboxId(),
		audit.ActionReasonResumeSession,
		audit.ActionStrategyResumeExistingRuntime,
	)
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
	actionReason, actionStrategy, err := resolveStopAction(req)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason:         string(actionReason),
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	s.mu.Unlock()
	go s.completeSandboxStop(req.GetSandboxId(), actionReason, actionStrategy)
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
	actionReason, actionStrategy, err := resolveDeleteAction(req)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETING
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETE_REQUESTED, eventMutation{
		reason:         string(actionReason),
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_DELETING,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	s.mu.Unlock()
	go s.completeSandboxDelete(req.GetSandboxId(), actionReason, actionStrategy)
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
	if isCursorExpired(record, nextSequence) {
		s.mu.RUnlock()
		return newStatusError(codes.FailedPrecondition, ReasonSandboxEventCursorStale, "cursor %q has expired", req.GetFromCursor())
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
			if isCursorExpired(record, nextSequence) {
				s.mu.RUnlock()
				return newStatusError(codes.FailedPrecondition, ReasonSandboxEventCursorStale, "cursor for sandbox %s has expired", req.GetSandboxId())
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
	actionReason, actionStrategy, err := resolveCreateExecAction(req)
	if err != nil {
		return nil, err
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
		State:        agboxv1.ExecState_EXEC_STATE_CREATED,
		Command:      slices.Clone(req.GetCommand()),
		Cwd:          req.GetCwd(),
		EnvOverrides: cloneKeyValues(req.GetEnvOverrides()),
	}
	record.execs[execID] = execRecord
	s.execs[execID] = req.GetSandboxId()
	s.appendEventLocked(record, agboxv1.EventType_EXEC_CREATED, eventMutation{
		execID:         execID,
		execState:      agboxv1.ExecState_EXEC_STATE_CREATED,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	return &agboxv1.CreateExecResponse{ExecId: execID}, nil
}

func (s *Service) StartExec(_ context.Context, req *agboxv1.StartExecRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	sandboxID, execRecord, err := s.requireExecLocked(req.GetExecId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if isExecTerminal(execRecord.GetState()) {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonExecAlreadyTerminal, "exec %s is already terminal", req.GetExecId())
	}
	actionReason, actionStrategy, err := resolveStartExecAction(req)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	record := s.boxes[sandboxID]
	execRecord.State = agboxv1.ExecState_EXEC_STATE_RUNNING
	s.appendEventLocked(record, agboxv1.EventType_EXEC_STARTED, eventMutation{
		execID:         req.GetExecId(),
		execState:      agboxv1.ExecState_EXEC_STATE_RUNNING,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	s.mu.Unlock()
	go s.completeExec(req.GetExecId(), actionReason, actionStrategy)
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
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
	actionReason, actionStrategy, err := resolveCancelExecAction(req)
	if err != nil {
		return nil, err
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_CANCELLED
	record := s.boxes[sandboxID]
	record.lastTerminalRunFinishedAt = time.Now().UTC()
	if artifactPath := record.execArtifacts[req.GetExecId()]; artifactPath != "" {
		if err := writeExecArtifact(artifactPath, "cancelled"); err != nil {
			return nil, status.Errorf(codes.Internal, "write exec artifact: %v", err)
		}
	}
	s.appendEventLocked(record, agboxv1.EventType_EXEC_CANCELLED, eventMutation{
		execID:         req.GetExecId(),
		execState:      agboxv1.ExecState_EXEC_STATE_CANCELLED,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
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

func (s *Service) completeSandboxCreate(
	sandboxID string,
	actionReason audit.ActionReason,
	actionStrategy audit.ActionStrategy,
) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.Lock()
	record, ok := s.boxes[sandboxID]
	if !ok || record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_PENDING {
		s.mu.Unlock()
		return
	}
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_PREPARING, eventMutation{
		phase:          "materialize",
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_PENDING,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	for _, dependency := range record.dependencies {
		s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DEPENDENCY_READY, eventMutation{
			dependencyName: dependency.GetDependencyName(),
			sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_PENDING,
			actionReason:   actionReason,
			actionStrategy: actionStrategy,
		})
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_READY,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
	s.mu.Unlock()
}

func (s *Service) completeSandboxStop(sandboxID string, actionReason audit.ActionReason, actionStrategy audit.ActionStrategy) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.boxes[sandboxID]
	if !ok || record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_STOPPED
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOPPED, eventMutation{
		reason:         string(actionReason),
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
}

func (s *Service) completeSandboxDelete(sandboxID string, actionReason audit.ActionReason, actionStrategy audit.ActionStrategy) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.boxes[sandboxID]
	if !ok {
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETED
	s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
		sandboxState:   agboxv1.SandboxState_SANDBOX_STATE_DELETED,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
	})
}

func (s *Service) completeExec(execID string, actionReason audit.ActionReason, actionStrategy audit.ActionStrategy) {
	time.Sleep(s.config.TransitionDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	sandboxID, execRecord, err := s.requireExecLocked(execID)
	if err != nil || execRecord.GetState() != agboxv1.ExecState_EXEC_STATE_RUNNING {
		return
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_FINISHED
	execRecord.ExitCode = 0
	record := s.boxes[sandboxID]
	record.lastTerminalRunFinishedAt = time.Now().UTC()
	if artifactPath := record.execArtifacts[execID]; artifactPath != "" {
		if err := writeExecArtifact(artifactPath, "finished"); err != nil {
			execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
			execRecord.Error = fmt.Sprintf("write exec artifact: %v", err)
			s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
				execID:         execID,
				execState:      agboxv1.ExecState_EXEC_STATE_FAILED,
				errorCode:      "ARTIFACT_OUTPUT_WRITE_FAILED",
				errorMessage:   execRecord.GetError(),
				actionReason:   actionReason,
				actionStrategy: actionStrategy,
			})
			return
		}
	}
	s.appendEventLocked(record, agboxv1.EventType_EXEC_FINISHED, eventMutation{
		execID:         execID,
		execState:      agboxv1.ExecState_EXEC_STATE_FINISHED,
		exitCode:       0,
		actionReason:   actionReason,
		actionStrategy: actionStrategy,
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

func writeExecArtifact(path string, state string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(fmt.Sprintf("{\"state\":\"%s\"}\n", state))
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
		reason:         string(audit.ActionReasonCleanupIdleSession),
		actionReason:   audit.ActionReasonCleanupIdleSession,
		actionStrategy: audit.ActionStrategyIdleSessionStop,
	})
	s.mu.Unlock()
	go s.completeSandboxStop(
		sandboxID,
		audit.ActionReasonCleanupIdleSession,
		audit.ActionStrategyIdleSessionStop,
	)
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
		ActionReason:   protoActionReason(mutation.actionReason),
		ActionStrategy: protoActionStrategy(mutation.actionStrategy),
	}
	record.events = append(record.events, event)
	if len(record.events) > s.config.ReplayLimit {
		record.events = slices.Clone(record.events[len(record.events)-s.config.ReplayLimit:])
	}
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

func makeOwnerKey(owner *agboxv1.SandboxOwner) string {
	if owner == nil {
		return ""
	}
	return owner.GetProduct() + "|" + owner.GetOwnerType() + "|" + owner.GetOwnerId()
}

func makeCursor(sandboxID string, sequence uint64) string {
	return sandboxID + ":" + strconv.FormatUint(sequence, 10)
}

func parseCursor(expectedSandboxID string, cursor string) (uint64, error) {
	if cursor == "" {
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

func isCursorExpired(record *sandboxRecord, sequence uint64) bool {
	if len(record.events) == 0 {
		return false
	}
	firstSequence := record.events[0].GetSequence()
	return sequence > 0 && sequence < firstSequence-1
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
	case agboxv1.ExecState_EXEC_STATE_CREATED:
		return agboxv1.EventType_EXEC_CREATED
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

func cloneOwner(owner *agboxv1.SandboxOwner) *agboxv1.SandboxOwner {
	if owner == nil {
		return nil
	}
	return &agboxv1.SandboxOwner{
		Product:   owner.GetProduct(),
		OwnerType: owner.GetOwnerType(),
		OwnerId:   owner.GetOwnerId(),
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
		Owner:                      cloneOwner(handle.GetOwner()),
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
		ActionReason:   event.GetActionReason(),
		ActionStrategy: event.GetActionStrategy(),
	}
}

func resolveStopAction(req *agboxv1.StopSandboxRequest) (audit.ActionReason, audit.ActionStrategy, error) {
	actionReason, err := resolveActionReason(req.GetActionReason(), req.GetReason(), audit.ActionReasonCleanupIdleSession)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	actionStrategy, err := resolveActionStrategy(req.GetActionStrategy(), audit.ActionStrategyIdleSessionStop, audit.ActionStrategyIdleSessionStop)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	return actionReason, actionStrategy, nil
}

func resolveDeleteAction(req *agboxv1.DeleteSandboxRequest) (audit.ActionReason, audit.ActionStrategy, error) {
	actionReason, err := resolveActionReason(req.GetActionReason(), req.GetReason(), audit.ActionReasonCleanupLeakedSessionRuntime)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	actionStrategy, err := resolveActionStrategy(req.GetActionStrategy(), audit.ActionStrategyDeleteSandboxRuntime, audit.ActionStrategyDeleteSandboxRuntime)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	return actionReason, actionStrategy, nil
}

func resolveCreateExecAction(req *agboxv1.CreateExecRequest) (audit.ActionReason, audit.ActionStrategy, error) {
	actionReason, err := resolveActionReason(req.GetActionReason(), "", audit.ActionReasonExecuteRun)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	actionStrategy, err := resolveActionStrategy(req.GetActionStrategy(), audit.ActionStrategyCreateRunExec, audit.ActionStrategyCreateRunExec)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	return actionReason, actionStrategy, nil
}

func resolveStartExecAction(req *agboxv1.StartExecRequest) (audit.ActionReason, audit.ActionStrategy, error) {
	actionReason, err := resolveActionReason(req.GetActionReason(), "", audit.ActionReasonExecuteRun)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	actionStrategy, err := resolveActionStrategy(req.GetActionStrategy(), audit.ActionStrategyStartRunExec, audit.ActionStrategyStartRunExec)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	return actionReason, actionStrategy, nil
}

func resolveCancelExecAction(req *agboxv1.CancelExecRequest) (audit.ActionReason, audit.ActionStrategy, error) {
	actionReason, err := resolveActionReason(req.GetActionReason(), "", audit.ActionReasonExecuteRun)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	actionStrategy, err := resolveActionStrategy(req.GetActionStrategy(), audit.ActionStrategyCancelRunExec, audit.ActionStrategyCancelRunExec)
	if err != nil {
		return "", "", status.Error(codes.InvalidArgument, err.Error())
	}
	return actionReason, actionStrategy, nil
}

func resolveActionReason(requested agboxv1.ActionReason, legacy string, fallback audit.ActionReason) (audit.ActionReason, error) {
	if requested != agboxv1.ActionReason_ACTION_REASON_UNSPECIFIED {
		return auditActionReasonFromProto(requested)
	}
	if legacy != "" {
		return audit.ParseActionReason(legacy)
	}
	return fallback, nil
}

func resolveActionStrategy(requested agboxv1.ActionStrategy, fallback audit.ActionStrategy, allowed ...audit.ActionStrategy) (audit.ActionStrategy, error) {
	actionStrategy := fallback
	if requested != agboxv1.ActionStrategy_ACTION_STRATEGY_UNSPECIFIED {
		var err error
		actionStrategy, err = auditActionStrategyFromProto(requested)
		if err != nil {
			return "", err
		}
	}
	for _, candidate := range allowed {
		if actionStrategy == candidate {
			return actionStrategy, nil
		}
	}
	return "", fmt.Errorf("unsupported action strategy %q", actionStrategy)
}

func auditActionReasonFromProto(reason agboxv1.ActionReason) (audit.ActionReason, error) {
	switch reason {
	case agboxv1.ActionReason_ACTION_REASON_START_NEW_SESSION:
		return audit.ActionReasonStartNewSession, nil
	case agboxv1.ActionReason_ACTION_REASON_RESUME_SESSION:
		return audit.ActionReasonResumeSession, nil
	case agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN:
		return audit.ActionReasonExecuteRun, nil
	case agboxv1.ActionReason_ACTION_REASON_CLEANUP_IDLE_SESSION:
		return audit.ActionReasonCleanupIdleSession, nil
	case agboxv1.ActionReason_ACTION_REASON_CLEANUP_LEAKED_SESSION_RESOURCES:
		return audit.ActionReasonCleanupLeakedSessionRuntime, nil
	case agboxv1.ActionReason_ACTION_REASON_CLEANUP_ARCHIVED_SESSION:
		return audit.ActionReasonCleanupArchivedSession, nil
	default:
		return "", fmt.Errorf("unsupported action reason %s", reason)
	}
}

func auditActionStrategyFromProto(strategy agboxv1.ActionStrategy) (audit.ActionStrategy, error) {
	switch strategy {
	case agboxv1.ActionStrategy_ACTION_STRATEGY_MATERIALIZE_NEW_SESSION_RUNTIME:
		return audit.ActionStrategyMaterializeNewSessionRuntime, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_RESUME_EXISTING_RUNTIME:
		return audit.ActionStrategyResumeExistingRuntime, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_CREATE_RUN_EXEC:
		return audit.ActionStrategyCreateRunExec, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_START_RUN_EXEC:
		return audit.ActionStrategyStartRunExec, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_CANCEL_RUN_EXEC:
		return audit.ActionStrategyCancelRunExec, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_IDLE_SESSION_STOP:
		return audit.ActionStrategyIdleSessionStop, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME:
		return audit.ActionStrategyDeleteSandboxRuntime, nil
	case agboxv1.ActionStrategy_ACTION_STRATEGY_LEAKED_SIDECAR_REMOVE:
		return audit.ActionStrategyLeakedSidecarRemove, nil
	default:
		return "", fmt.Errorf("unsupported action strategy %s", strategy)
	}
}

func protoActionReason(reason audit.ActionReason) agboxv1.ActionReason {
	switch reason {
	case audit.ActionReasonStartNewSession:
		return agboxv1.ActionReason_ACTION_REASON_START_NEW_SESSION
	case audit.ActionReasonResumeSession:
		return agboxv1.ActionReason_ACTION_REASON_RESUME_SESSION
	case audit.ActionReasonExecuteRun:
		return agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN
	case audit.ActionReasonCleanupIdleSession:
		return agboxv1.ActionReason_ACTION_REASON_CLEANUP_IDLE_SESSION
	case audit.ActionReasonCleanupLeakedSessionRuntime:
		return agboxv1.ActionReason_ACTION_REASON_CLEANUP_LEAKED_SESSION_RESOURCES
	case audit.ActionReasonCleanupArchivedSession:
		return agboxv1.ActionReason_ACTION_REASON_CLEANUP_ARCHIVED_SESSION
	default:
		return agboxv1.ActionReason_ACTION_REASON_UNSPECIFIED
	}
}

func protoActionStrategy(strategy audit.ActionStrategy) agboxv1.ActionStrategy {
	switch strategy {
	case audit.ActionStrategyMaterializeNewSessionRuntime:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_MATERIALIZE_NEW_SESSION_RUNTIME
	case audit.ActionStrategyResumeExistingRuntime:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_RESUME_EXISTING_RUNTIME
	case audit.ActionStrategyCreateRunExec:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_CREATE_RUN_EXEC
	case audit.ActionStrategyStartRunExec:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_START_RUN_EXEC
	case audit.ActionStrategyCancelRunExec:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_CANCEL_RUN_EXEC
	case audit.ActionStrategyIdleSessionStop:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_IDLE_SESSION_STOP
	case audit.ActionStrategyDeleteSandboxRuntime:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME
	case audit.ActionStrategyLeakedSidecarRemove:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_LEAKED_SIDECAR_REMOVE
	default:
		return agboxv1.ActionStrategy_ACTION_STRATEGY_UNSPECIFIED
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
