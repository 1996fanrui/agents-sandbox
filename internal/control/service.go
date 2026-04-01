package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/version"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ServiceConfig struct {
	TransitionDelay        time.Duration
	PollInterval           time.Duration
	IdleTTL                time.Duration
	CleanupTTL             time.Duration
	CleanupInterval        time.Duration
	ArtifactOutputRoot     string
	ArtifactOutputTemplate string
	Version                string
	DaemonName             string
	LogLevel               string
	Logger                 *slog.Logger
	runtimeBackend         runtimeBackend
	idRegistry             idRegistry
	eventStore             eventStore
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		TransitionDelay:        10 * time.Millisecond,
		PollInterval:           10 * time.Millisecond,
		IdleTTL:                10 * time.Minute,
		CleanupTTL:             360 * time.Hour,
		CleanupInterval:        2 * time.Minute,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}",
		Version:                version.Version,
		DaemonName:             "agboxd",
		LogLevel:               "info",
	}
}

type Service struct {
	agboxv1.UnimplementedSandboxServiceServer

	mu     sync.RWMutex
	config ServiceConfig
	boxes  map[string]*sandboxRecord
	execs  map[string]string
}

var (
	errArtifactPathEscapesRoot    = errors.New("artifact path escapes configured root")
	errArtifactTemplateFieldEmpty = errors.New("artifact template field is empty")
)

// execArtifactPaths holds the host-side log file paths for a single exec.
// These paths correspond to the files written by the container via the bind-mounted exec log directory.
type execArtifactPaths struct {
	StdoutPath string
	StderrPath string
}

type sandboxRecord struct {
	handle                    *agboxv1.SandboxHandle
	createSpec                *agboxv1.CreateSpec
	companionContainers       []*agboxv1.CompanionContainerSpec
	runtimeState              *sandboxRuntimeState
	events                    []*agboxv1.SandboxEvent
	execs                     map[string]*agboxv1.ExecStatus
	execCancel                map[string]context.CancelFunc
	nextSequence              uint64
	lastTerminalRunFinishedAt time.Time
	deletedAtRecorded         bool
}

type eventMutation struct {
	phase                  string
	companionContainerName string
	errorCode              string
	errorMessage           string
	reason                 string
	execID                 string
	exitCode               int32
	sandboxState           agboxv1.SandboxState
	execState              agboxv1.ExecState
}

var callerProvidedIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{2,198}[a-zA-Z0-9]$`)

func NewService(config ServiceConfig) (*Service, io.Closer, error) {
	if config.Logger == nil {
		return nil, nil, errors.New("ServiceConfig.Logger must not be nil")
	}
	defaults := DefaultServiceConfig()
	if config.TransitionDelay <= 0 {
		config.TransitionDelay = defaults.TransitionDelay
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaults.PollInterval
	}
	if config.CleanupTTL <= 0 {
		config.CleanupTTL = defaults.CleanupTTL
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = defaults.CleanupInterval
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
	var runtimeCloser io.Closer
	if config.runtimeBackend == nil {
		runtimeBackend, closer, err := newDockerRuntimeBackend(config)
		if err != nil {
			return nil, nil, err
		}
		config.runtimeBackend = runtimeBackend
		runtimeCloser = closer
	}
	if config.idRegistry == nil {
		config.idRegistry = newMemoryIDRegistry()
	}
	if config.eventStore == nil {
		config.eventStore = newMemoryEventStore()
	}
	return &Service{
		config: config,
		boxes:  make(map[string]*sandboxRecord),
		execs:  make(map[string]string),
	}, runtimeCloser, nil
}

func (s *Service) Ping(context.Context, *agboxv1.PingRequest) (*agboxv1.PingResponse, error) {
	return &agboxv1.PingResponse{
		Version: s.config.Version,
		Daemon:  s.config.DaemonName,
	}, nil
}

func (s *Service) CreateSandbox(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	s.config.Logger.Debug("gRPC CreateSandbox", slog.String("image", req.GetCreateSpec().GetImage()))

	// YAML parsing and merging (must happen before validation).
	if len(req.GetConfigYaml()) > 0 {
		yamlCfg, err := parseYAMLConfig(req.GetConfigYaml())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid config_yaml: %v", err)
		}
		yamlSpec, err := yamlConfigToCreateSpec(yamlCfg)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid config_yaml: %v", err)
		}
		override := req.GetCreateSpec()
		if override == nil {
			override = &agboxv1.CreateSpec{}
		}
		req.CreateSpec = mergeCreateSpecs(yamlSpec, override)
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

	sandboxID, err := s.allocateSandboxID(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if err := s.config.eventStore.SaveSandboxConfig(sandboxID, proto.Clone(req.GetCreateSpec()).(*agboxv1.CreateSpec)); err != nil {
		if releaseErr := s.config.idRegistry.ReleaseSandboxID(sandboxID); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release sandbox id %s: %w", sandboxID, releaseErr))
		}
		return nil, status.Errorf(codes.Internal, "save sandbox config: %v", err)
	}
	record := &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId:           sandboxID,
			State:               agboxv1.SandboxState_SANDBOX_STATE_PENDING,
			Labels:              cloneStringMap(req.GetCreateSpec().GetLabels()),
			CompanionContainers: cloneCompanionContainerSpecs(req.GetCreateSpec().GetCompanionContainers()),
			CreatedAt:           timestamppb.Now(),
			Image:               req.GetCreateSpec().GetImage(),
		},
		createSpec:          cloneCreateSpec(req.GetCreateSpec()),
		companionContainers: cloneCompanionContainerSpecs(req.GetCreateSpec().GetCompanionContainers()),
		execs:               make(map[string]*agboxv1.ExecStatus),
		execCancel:           make(map[string]context.CancelFunc),
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	}); err != nil {
		_ = s.config.eventStore.DeleteSandboxConfig(sandboxID)
		if releaseErr := s.config.idRegistry.ReleaseSandboxID(sandboxID); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release sandbox id %s: %w", sandboxID, releaseErr))
		}
		return nil, status.Errorf(codes.Internal, "append SANDBOX_ACCEPTED event: %v", err)
	}
	s.boxes[sandboxID] = record
	go s.completeSandboxCreate(sandboxID)
	s.config.Logger.Info("sandbox create accepted", slog.String("sandbox_id", sandboxID))

	return &agboxv1.CreateSandboxResponse{
		Sandbox: cloneHandle(record.handle),
	}, nil
}

func (s *Service) GetSandbox(_ context.Context, req *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
	s.config.Logger.Debug("gRPC GetSandbox", slog.String("sandbox_id", req.GetSandboxId()))
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.boxes[req.GetSandboxId()]
	if !ok {
		return nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s was not found", req.GetSandboxId())
	}
	return &agboxv1.GetSandboxResponse{Sandbox: cloneHandle(record.handle)}, nil
}

func (s *Service) ListSandboxes(_ context.Context, req *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	s.config.Logger.Debug("gRPC ListSandboxes")
	s.mu.RLock()
	defer s.mu.RUnlock()

	var handles []*agboxv1.SandboxHandle
	for _, record := range s.boxes {
		if !req.GetIncludeDeleted() && record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			continue
		}
		if !matchesLabelSelector(record.handle.GetLabels(), req.GetLabelSelector()) {
			continue
		}
		handles = append(handles, cloneHandle(record.handle))
	}
	// Sort by created_at descending (newest first), matching docker ps convention.
	slices.SortFunc(handles, func(left, right *agboxv1.SandboxHandle) int {
		lt := left.GetCreatedAt().AsTime()
		rt := right.GetCreatedAt().AsTime()
		switch {
		case lt.After(rt):
			return -1
		case lt.Before(rt):
			return 1
		default:
			return strings.Compare(left.GetSandboxId(), right.GetSandboxId())
		}
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
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s is not stopped", req.GetSandboxId())
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	}); err != nil {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Internal, "append SANDBOX_ACCEPTED event: %v", err)
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_PENDING
	s.mu.Unlock()
	go s.completeSandboxResume(req.GetSandboxId())
	s.config.Logger.Debug("gRPC ResumeSandbox", slog.String("sandbox_id", req.GetSandboxId()))
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) StopSandbox(_ context.Context, req *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.config.Logger.Debug("gRPC StopSandbox", slog.String("sandbox_id", req.GetSandboxId()))
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s is not ready", req.GetSandboxId())
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason: "stop_requested",
	}); err != nil {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Internal, "append SANDBOX_STOP_REQUESTED event: %v", err)
	}
	s.mu.Unlock()
	go s.completeSandboxStop(req.GetSandboxId(), "stop_requested")
	s.config.Logger.Info("sandbox stop requested", slog.String("sandbox_id", req.GetSandboxId()), slog.String("reason", "stop_requested"))
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) DeleteSandbox(_ context.Context, req *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.config.Logger.Debug("gRPC DeleteSandbox", slog.String("sandbox_id", req.GetSandboxId()))
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
		if !record.deletedAtRecorded {
			if err := s.config.eventStore.MarkDeleted(req.GetSandboxId(), time.Now()); err != nil {
				s.mu.Unlock()
				return nil, status.Errorf(codes.Internal, "mark sandbox deleted: %v", err)
			}
			record.deletedAtRecorded = true
		}
		s.mu.Unlock()
		return &agboxv1.AcceptedResponse{Accepted: true}, nil
	}
	started, err := s.beginSandboxDeleteLocked(record, "delete_requested")
	if err != nil {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Internal, "append SANDBOX_DELETE_REQUESTED event: %v", err)
	}
	if !started {
		s.mu.Unlock()
		return &agboxv1.AcceptedResponse{Accepted: true}, nil
	}
	s.mu.Unlock()
	go s.completeSandboxDelete(req.GetSandboxId(), "delete_requested")
	s.config.Logger.Info("sandbox delete requested", slog.String("sandbox_id", req.GetSandboxId()))
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) DeleteSandboxes(_ context.Context, req *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	s.config.Logger.Debug("gRPC DeleteSandboxes")
	if len(req.GetLabelSelector()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "label_selector must not be empty")
	}

	s.mu.Lock()
	sandboxIDs := make([]string, 0)
	asyncDeleteSandboxIDs := make([]string, 0)
	for sandboxID, record := range s.boxes {
		if !matchesLabelSelector(record.handle.GetLabels(), req.GetLabelSelector()) {
			continue
		}
		started, err := s.beginSandboxDeleteLocked(record, "delete_requested")
		if err != nil {
			s.mu.Unlock()
			return nil, status.Errorf(codes.Internal, "append SANDBOX_DELETE_REQUESTED event: %v", err)
		}
		if !started {
			continue
		}
		sandboxIDs = append(sandboxIDs, sandboxID)
		asyncDeleteSandboxIDs = append(asyncDeleteSandboxIDs, sandboxID)
	}
	s.mu.Unlock()

	slices.Sort(sandboxIDs)
	slices.Sort(asyncDeleteSandboxIDs)
	for _, sandboxID := range asyncDeleteSandboxIDs {
		go s.completeSandboxDelete(sandboxID, "delete_requested")
	}

	return &agboxv1.DeleteSandboxesResponse{
		DeletedSandboxIds: sandboxIDs,
		DeletedCount:      uint32(len(sandboxIDs)),
	}, nil
}

func (s *Service) SubscribeSandboxEvents(req *agboxv1.SubscribeSandboxEventsRequest, stream agboxv1.SandboxService_SubscribeSandboxEventsServer) error {
	s.config.Logger.Debug("gRPC SubscribeToSandboxEvents", slog.String("sandbox_id", req.GetSandboxId()))
	s.mu.RLock()
	record, ok := s.boxes[req.GetSandboxId()]
	if !ok {
		s.mu.RUnlock()
		return newStatusError(codes.NotFound, ReasonSandboxNotFound, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s was not found", req.GetSandboxId())
	}
	if req.GetIncludeCurrentSnapshot() {
		for _, event := range snapshotEvents(record) {
			if err := stream.Send(event); err != nil {
				s.mu.RUnlock()
				return err
			}
		}
	}
	nextSequence := req.GetFromSequence()
	if err := validateSequenceNotExpired(record, nextSequence); err != nil {
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
				return newStatusError(codes.NotFound, ReasonSandboxNotFound, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s was not found", req.GetSandboxId())
			}
			if err := validateSequenceNotExpired(record, nextSequence); err != nil {
				s.mu.RUnlock()
				return err
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
	s.config.Logger.Debug("gRPC ExecInSandbox", slog.String("sandbox_id", req.GetSandboxId()))
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxNotReady, map[string]string{"sandbox_id": req.GetSandboxId()}, "sandbox %s is not ready", req.GetSandboxId())
	}
	if len(req.GetCommand()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "command must not be empty")
	}
	execID, err := s.allocateExecID(req.GetExecId())
	if err != nil {
		return nil, err
	}
	execConfigCopy := &agboxv1.CreateExecRequest{
		SandboxId:    req.GetSandboxId(),
		ExecId:       execID,
		Command:      slices.Clone(req.GetCommand()),
		Cwd:          req.GetCwd(),
		EnvOverrides: cloneStringMap(req.GetEnvOverrides()),
	}
	if err := s.config.eventStore.SaveExecConfig(req.GetSandboxId(), execConfigCopy); err != nil {
		cleanupErr := s.config.idRegistry.ReleaseExecID(execID)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return nil, status.Errorf(codes.Internal, "save exec config: %v", err)
	}
	artifactPaths, artifactErr := s.prepareExecArtifactPaths(record.handle.GetSandboxId(), execID)
	if artifactErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "prepare exec artifact output: %v", artifactErr)
	}
	execRecord := &agboxv1.ExecStatus{
		ExecId:       execID,
		SandboxId:    req.GetSandboxId(),
		State:        agboxv1.ExecState_EXEC_STATE_RUNNING,
		Command:      slices.Clone(req.GetCommand()),
		Cwd:          req.GetCwd(),
		EnvOverrides: cloneStringMap(req.GetEnvOverrides()),
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_STARTED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_RUNNING,
	}); err != nil {
		cleanupErr := s.config.idRegistry.ReleaseExecID(execID)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return nil, status.Errorf(codes.Internal, "append EXEC_STARTED event: %v", err)
	}
	record.execs[execID] = execRecord
	s.execs[execID] = req.GetSandboxId()
	execContext, cancel := context.WithCancel(context.Background())
	record.execCancel[execID] = cancel
	go s.completeExec(execContext, execID)
	s.config.Logger.Info("exec started", slog.String("sandbox_id", req.GetSandboxId()), slog.String("exec_id", execID))
	return &agboxv1.CreateExecResponse{
		ExecId:        execID,
		StdoutLogPath: artifactPaths.StdoutPath,
		StderrLogPath: artifactPaths.StderrPath,
	}, nil
}

func (s *Service) CancelExec(_ context.Context, req *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sandboxID, execRecord, err := s.requireExecLocked(req.GetExecId())
	if err != nil {
		return nil, err
	}
	if isExecTerminal(execRecord.GetState()) {
		return nil, newStatusError(codes.FailedPrecondition, ReasonExecAlreadyTerminal, map[string]string{"exec_id": req.GetExecId()}, "exec %s is already terminal", req.GetExecId())
	}
	record := s.boxes[sandboxID]
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_CANCELLED, eventMutation{
		execID:    req.GetExecId(),
		execState: agboxv1.ExecState_EXEC_STATE_CANCELLED,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "append EXEC_CANCELLED event: %v", err)
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_CANCELLED
	s.config.Logger.Info("exec cancelled", slog.String("sandbox_id", sandboxID), slog.String("exec_id", req.GetExecId()))
	if cancel := record.execCancel[req.GetExecId()]; cancel != nil {
		cancel()
		delete(record.execCancel, req.GetExecId())
	}
	record.lastTerminalRunFinishedAt = time.Now().UTC()
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) GetExec(_ context.Context, req *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sandboxID, execRecord, err := s.requireExecLocked(req.GetExecId())
	if err != nil {
		return nil, err
	}
	record := s.boxes[sandboxID]
	snapshot := cloneExec(execRecord)
	snapshot.LastEventSequence = record.handle.GetLastEventSequence()
	return &agboxv1.GetExecResponse{Exec: snapshot}, nil
}

func (s *Service) allocateSandboxID(requestedID string) (string, error) {
	return s.allocateID(
		requestedID,
		s.config.idRegistry.ReserveSandboxID,
		ReasonSandboxIDAlreadyExists,
		"sandbox_id",
	)
}

func (s *Service) allocateExecID(requestedID string) (string, error) {
	return s.allocateID(
		requestedID,
		s.config.idRegistry.ReserveExecID,
		ReasonExecIDAlreadyExists,
		"exec_id",
	)
}

func (s *Service) allocateID(
	requestedID string,
	reserve func(string, time.Time) error,
	duplicateReason string,
	fieldName string,
) (string, error) {
	if requestedID != "" {
		if err := validateCallerProvidedID(fieldName, requestedID); err != nil {
			return "", err
		}
		if err := reserve(requestedID, time.Now().UTC()); err != nil {
			if errors.Is(err, errSandboxIDAlreadyExists) || errors.Is(err, errExecIDAlreadyExists) {
				return "", newStatusError(codes.AlreadyExists, duplicateReason, map[string]string{fieldName: requestedID}, "%s %s already exists", fieldName, requestedID)
			}
			return "", status.Errorf(codes.Internal, "reserve %s: %v", fieldName, err)
		}
		return requestedID, nil
	}
	for {
		generatedID := uuid.NewString()
		if err := reserve(generatedID, time.Now().UTC()); err != nil {
			if errors.Is(err, errSandboxIDAlreadyExists) || errors.Is(err, errExecIDAlreadyExists) {
				continue
			}
			return "", status.Errorf(codes.Internal, "reserve %s: %v", fieldName, err)
		}
		return generatedID, nil
	}
}

func validateCallerProvidedID(fieldName string, id string) error {
	if len(id) < 4 {
		return status.Errorf(codes.InvalidArgument, "%s must be at least 4 characters", fieldName)
	}
	if !callerProvidedIDPattern.MatchString(id) {
		return status.Errorf(codes.InvalidArgument,
			"%s must be 4-200 characters, start and end with a letter or digit, and contain only letters, digits, hyphens, or underscores",
			fieldName)
	}
	return nil
}

func (s *Service) ListActiveExecs(_ context.Context, req *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var execs []*agboxv1.ExecStatus
	for _, record := range s.boxes {
		if req.SandboxId != nil && record.handle.GetSandboxId() != req.GetSandboxId() {
			continue
		}
		for _, execRecord := range record.execs {
			if isExecTerminal(execRecord.GetState()) {
				continue
			}
			snapshot := cloneExec(execRecord)
			snapshot.LastEventSequence = record.handle.GetLastEventSequence()
			execs = append(execs, snapshot)
		}
	}
	slices.SortFunc(execs, func(left, right *agboxv1.ExecStatus) int {
		return strings.Compare(left.GetExecId(), right.GetExecId())
	})
	return &agboxv1.ListActiveExecsResponse{Execs: execs}, nil
}
