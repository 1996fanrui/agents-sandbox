package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ServiceConfig struct {
	TransitionDelay        time.Duration
	PollInterval           time.Duration
	IdleTTL                time.Duration
	EventRetentionTTL      time.Duration
	StateRoot              string
	ArtifactOutputRoot     string
	ArtifactOutputTemplate string
	Version                string
	DaemonName             string
	runtimeBackend         runtimeBackend
	idRegistry             idRegistry
	eventStore             eventStore
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		TransitionDelay:        10 * time.Millisecond,
		PollInterval:           10 * time.Millisecond,
		IdleTTL:                30 * time.Minute,
		EventRetentionTTL:      168 * time.Hour,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.log",
		Version:                "0.1.0",
		DaemonName:             "agboxd",
	}
}

type Service struct {
	agboxv1.UnimplementedSandboxServiceServer

	mu     sync.RWMutex
	config ServiceConfig
	boxes  map[string]*sandboxRecord
	execs  map[string]string
}

const eventRetentionCleanupInterval = 5 * time.Minute

var (
	errArtifactPathEscapesRoot    = errors.New("artifact path escapes configured root")
	errArtifactPathUsesSymlink    = errors.New("artifact path uses symlink boundary")
	errArtifactPathUsesHardlink   = errors.New("artifact path uses hardlink boundary")
	errArtifactTemplateFieldEmpty = errors.New("artifact template field is empty")
)

type sandboxRecord struct {
	handle                    *agboxv1.SandboxHandle
	createSpec                *agboxv1.CreateSpec
	requiredServices          []*agboxv1.ServiceSpec
	optionalServices          []*agboxv1.ServiceSpec
	runtimeState              *sandboxRuntimeState
	events                    []*agboxv1.SandboxEvent
	execs                     map[string]*agboxv1.ExecStatus
	execCancel                map[string]context.CancelFunc
	execArtifacts             map[string]string
	nextSequence              uint64
	lastTerminalRunFinishedAt time.Time
	deletedAtRecorded         bool
	recoveredOnly             bool
}

type eventMutation struct {
	phase        string
	serviceName  string
	errorCode    string
	errorMessage string
	reason       string
	execID       string
	exitCode     int32
	sandboxState agboxv1.SandboxState
	execState    agboxv1.ExecState
}

var callerProvidedIDPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func NewService(config ServiceConfig) (*Service, io.Closer, error) {
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
	if config.EventRetentionTTL <= 0 {
		config.EventRetentionTTL = defaults.EventRetentionTTL
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
	record := &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId:        sandboxID,
			State:            agboxv1.SandboxState_SANDBOX_STATE_PENDING,
			Labels:           cloneStringMap(req.GetCreateSpec().GetLabels()),
			RequiredServices: cloneServiceSpecs(req.GetCreateSpec().GetRequiredServices()),
			OptionalServices: cloneServiceSpecs(req.GetCreateSpec().GetOptionalServices()),
		},
		createSpec:       cloneCreateSpec(req.GetCreateSpec()),
		requiredServices: cloneServiceSpecs(req.GetCreateSpec().GetRequiredServices()),
		optionalServices: cloneServiceSpecs(req.GetCreateSpec().GetOptionalServices()),
		execs:            make(map[string]*agboxv1.ExecStatus),
		execCancel:       make(map[string]context.CancelFunc),
		execArtifacts:    make(map[string]string),
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_ACCEPTED, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	}); err != nil {
		if releaseErr := s.config.idRegistry.ReleaseSandboxID(sandboxID); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release sandbox id %s: %w", sandboxID, releaseErr))
		}
		return nil, status.Errorf(codes.Internal, "append SANDBOX_ACCEPTED event: %v", err)
	}
	s.boxes[sandboxID] = record
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
	if err := requireMutableSandbox(record); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_STOPPED {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, "sandbox %s is not stopped", req.GetSandboxId())
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
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) StopSandbox(_ context.Context, req *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.mu.Lock()
	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := requireMutableSandbox(record); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		s.mu.Unlock()
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxInvalidState, "sandbox %s is not ready", req.GetSandboxId())
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason: "stop_requested",
	}); err != nil {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Internal, "append SANDBOX_STOP_REQUESTED event: %v", err)
	}
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
	if record.recoveredOnly {
		if err := s.finishRecoveredSandboxDeleteLocked(record, "delete_requested"); err != nil {
			s.mu.Unlock()
			return nil, status.Errorf(codes.Internal, "delete recovered sandbox: %v", err)
		}
		s.mu.Unlock()
		return &agboxv1.AcceptedResponse{Accepted: true}, nil
	}
	s.mu.Unlock()
	go s.completeSandboxDelete(req.GetSandboxId(), "delete_requested")
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (s *Service) DeleteSandboxes(_ context.Context, req *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
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
		if record.recoveredOnly {
			if err := s.finishRecoveredSandboxDeleteLocked(record, "delete_requested"); err != nil {
				s.mu.Unlock()
				return nil, status.Errorf(codes.Internal, "delete recovered sandbox: %v", err)
			}
			continue
		}
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
	if err := validateCursorNotStale(record, nextSequence); err != nil {
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
			if err := validateCursorNotStale(record, nextSequence); err != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.requireSandboxLocked(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	if err := requireMutableSandbox(record); err != nil {
		return nil, err
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		return nil, newStatusError(codes.FailedPrecondition, ReasonSandboxNotReady, "sandbox %s is not ready", req.GetSandboxId())
	}
	if len(req.GetCommand()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "command must not be empty")
	}
	execID, err := s.allocateExecID(req.GetExecId())
	if err != nil {
		return nil, err
	}
	artifactPath, artifactErr := s.prepareExecArtifactPath(record.handle.GetSandboxId(), execID)
	if artifactErr != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "prepare exec artifact output: %v", artifactErr)
	}
	execRecord := &agboxv1.ExecStatus{
		ExecId:       execID,
		SandboxId:    req.GetSandboxId(),
		State:        agboxv1.ExecState_EXEC_STATE_RUNNING,
		Command:      slices.Clone(req.GetCommand()),
		Cwd:          req.GetCwd(),
		EnvOverrides: cloneKeyValues(req.GetEnvOverrides()),
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_STARTED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_RUNNING,
	}); err != nil {
		cleanupErr := s.config.idRegistry.ReleaseExecID(execID)
		if artifactPath != "" {
			cleanupErr = errors.Join(cleanupErr, os.Remove(artifactPath))
		}
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		return nil, status.Errorf(codes.Internal, "append EXEC_STARTED event: %v", err)
	}
	if artifactPath != "" {
		record.execArtifacts[execID] = artifactPath
	}
	record.execs[execID] = execRecord
	s.execs[execID] = req.GetSandboxId()
	execContext, cancel := context.WithCancel(context.Background())
	record.execCancel[execID] = cancel
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
	record := s.boxes[sandboxID]
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_CANCELLED, eventMutation{
		execID:    req.GetExecId(),
		execState: agboxv1.ExecState_EXEC_STATE_CANCELLED,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "append EXEC_CANCELLED event: %v", err)
	}
	execRecord.State = agboxv1.ExecState_EXEC_STATE_CANCELLED
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
				return "", newStatusError(codes.AlreadyExists, duplicateReason, "%s %s already exists", fieldName, requestedID)
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
		return status.Errorf(codes.InvalidArgument, "%s must match %s", fieldName, callerProvidedIDPattern.String())
	}
	return nil
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
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_PREPARING, eventMutation{
		phase:        "materialize",
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_PENDING,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_PREPARING, err)
		s.mu.Unlock()
		return
	}
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
		if appendErr := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_CREATE_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		}); appendErr != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		return
	}
	record.runtimeState = result.RuntimeState
	if err := s.appendServiceEventsLocked(record, result.ServiceStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	optionalStatuses, optionalStatusesOpen := drainAvailableRuntimeServiceStatuses(result.OptionalServiceStatuses)
	if err := s.appendServiceEventsLocked(record, optionalStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_READY, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
	if optionalStatusesOpen {
		go s.completeOptionalServiceCreate(sandboxID, result.OptionalServiceStatuses)
	}
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

	result, err := s.config.runtimeBackend.ResumeSandbox(context.Background(), record)

	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok = s.boxes[sandboxID]
	if !ok {
		return
	}
	if err != nil {
		if appendErr := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_RESUME_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		}); appendErr != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		return
	}
	if err := s.appendServiceEventsLocked(record, result.ServiceStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_READY, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
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
		if appendErr := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_STOP_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		}); appendErr != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOPPED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_STOPPED, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_STOPPED
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
		if appendErr := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    "SANDBOX_DELETE_FAILED",
			errorMessage: err.Error(),
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		}); appendErr != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETED,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_DELETED, err)
		return
	}
	if err := s.config.eventStore.MarkDeleted(sandboxID, time.Now()); err != nil {
		log.Printf("mark sandbox %s deleted: %v", sandboxID, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETED
	record.deletedAtRecorded = true
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
	if execContext.Err() != nil {
		delete(record.execCancel, execID)
		return
	}
	if runErr != nil {
		execErrorMessage := strings.TrimSpace(strings.Join([]string{result.Stderr, result.Stdout}, "\n"))
		if execErrorMessage == "" {
			execErrorMessage = runErr.Error()
		}
		if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
			execID:       execID,
			execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
			exitCode:     result.ExitCode,
			errorCode:    "EXEC_RUN_FAILED",
			errorMessage: execErrorMessage,
		}); err != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EXEC_FAILED, err)
			return
		}
		delete(record.execCancel, execID)
		execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
		execRecord.ExitCode = result.ExitCode
		execRecord.Stdout = result.Stdout
		execRecord.Stderr = result.Stderr
		execRecord.Error = execErrorMessage
		return
	}
	finishedExec := cloneExec(execRecord)
	finishedExec.State = agboxv1.ExecState_EXEC_STATE_FINISHED
	finishedExec.ExitCode = result.ExitCode
	finishedExec.Stdout = result.Stdout
	finishedExec.Stderr = result.Stderr
	if artifactPath := record.execArtifacts[execID]; artifactPath != "" {
		if err := writeExecArtifact(artifactPath, finishedExec); err != nil {
			artifactError := fmt.Sprintf("write exec artifact: %v", err)
			if appendErr := s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
				execID:       execID,
				execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
				errorCode:    "ARTIFACT_OUTPUT_WRITE_FAILED",
				errorMessage: artifactError,
			}); appendErr != nil {
				logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EXEC_FAILED, appendErr)
				return
			}
			delete(record.execCancel, execID)
			execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
			execRecord.ExitCode = result.ExitCode
			execRecord.Stdout = result.Stdout
			execRecord.Stderr = result.Stderr
			execRecord.Error = artifactError
			record.lastTerminalRunFinishedAt = time.Now().UTC()
			return
		}
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_FINISHED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_FINISHED,
		exitCode:  result.ExitCode,
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EXEC_FINISHED, err)
		return
	}
	delete(record.execCancel, execID)
	execRecord.State = agboxv1.ExecState_EXEC_STATE_FINISHED
	execRecord.ExitCode = result.ExitCode
	execRecord.Stdout = result.Stdout
	execRecord.Stderr = result.Stderr
	record.lastTerminalRunFinishedAt = time.Now().UTC()
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
	seenServiceNames := make(map[string]struct{}, len(spec.GetRequiredServices())+len(spec.GetOptionalServices()))
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
	if err := validateServiceSpecs(spec.GetRequiredServices(), true, seenServiceNames); err != nil {
		return err
	}
	if err := validateServiceSpecs(spec.GetOptionalServices(), false, seenServiceNames); err != nil {
		return err
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

func validateServiceSpecs(items []*agboxv1.ServiceSpec, required bool, seen map[string]struct{}) error {
	for _, service := range items {
		if service.GetName() == "" {
			return errors.New("service name is required")
		}
		if service.GetImage() == "" {
			return fmt.Errorf("service %q image is required", service.GetName())
		}
		if _, exists := seen[service.GetName()]; exists {
			return fmt.Errorf("duplicate service name %q", service.GetName())
		}
		seen[service.GetName()] = struct{}{}
		if required && service.GetHealthcheck() == nil {
			return fmt.Errorf("required service %q must define healthcheck", service.GetName())
		}
		if !required && len(service.GetPostStartOnPrimary()) > 0 {
			return fmt.Errorf("optional service %q must not define post_start_on_primary", service.GetName())
		}
		if err := validateHealthcheck(service.GetName(), service.GetHealthcheck(), required); err != nil {
			return err
		}
	}
	return nil
}

func validateHealthcheck(serviceName string, healthcheck *agboxv1.HealthcheckConfig, required bool) error {
	if healthcheck == nil {
		return nil
	}
	if len(healthcheck.GetTest()) == 0 {
		return fmt.Errorf("service %q healthcheck.test must not be empty", serviceName)
	}
	command := healthcheck.GetTest()[0]
	allowed := map[string]struct{}{
		"CMD":       {},
		"CMD-SHELL": {},
	}
	if !required {
		allowed["NONE"] = struct{}{}
	}
	if _, ok := allowed[command]; !ok {
		return fmt.Errorf("service %q healthcheck.test[0] %q is invalid", serviceName, command)
	}
	if command == "NONE" && len(healthcheck.GetTest()) > 1 {
		return fmt.Errorf("service %q healthcheck.test must not include extra args when NONE is used", serviceName)
	}
	if (command == "CMD" || command == "CMD-SHELL") && len(healthcheck.GetTest()) < 2 {
		return fmt.Errorf("service %q healthcheck.test for %s must include a command", serviceName, command)
	}
	for _, raw := range []string{
		healthcheck.GetInterval(),
		healthcheck.GetTimeout(),
		healthcheck.GetStartPeriod(),
		healthcheck.GetStartInterval(),
	} {
		if raw == "" {
			continue
		}
		if _, err := time.ParseDuration(raw); err != nil {
			return fmt.Errorf("service %q healthcheck duration %q is invalid: %w", serviceName, raw, err)
		}
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
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
		reason: "idle_ttl",
	}); err != nil {
		logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_SANDBOX_STOP_REQUESTED, err)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	go s.completeSandboxStop(sandboxID, "idle_ttl")
}

func (s *Service) cleanupExpiredEvents() error {
	removedSandboxIDs, err := s.config.eventStore.Cleanup(s.config.EventRetentionTTL)
	if err != nil {
		return err
	}
	if len(removedSandboxIDs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sandboxID := range removedSandboxIDs {
		record := s.boxes[sandboxID]
		if record != nil {
			for execID := range record.execs {
				delete(s.execs, execID)
			}
		}
		delete(s.boxes, sandboxID)
	}
	return nil
}

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(eventRetentionCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cleanupExpiredEvents(); err != nil {
				log.Printf("cleanup expired sandbox events: %v", err)
			}
		}
	}
}

func hasActiveExec(record *sandboxRecord) bool {
	for _, execRecord := range record.execs {
		if !isExecTerminal(execRecord.GetState()) {
			return true
		}
	}
	return false
}

func drainAvailableRuntimeServiceStatuses(statuses <-chan runtimeServiceStatus) ([]runtimeServiceStatus, bool) {
	if statuses == nil {
		return nil, false
	}
	drained := make([]runtimeServiceStatus, 0)
	for {
		select {
		case status, ok := <-statuses:
			if !ok {
				return drained, false
			}
			drained = append(drained, status)
		default:
			return drained, true
		}
	}
}

func (s *Service) completeOptionalServiceCreate(sandboxID string, statuses <-chan runtimeServiceStatus) {
	for serviceStatus := range statuses {
		s.mu.Lock()
		record, ok := s.boxes[sandboxID]
		if !ok || record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
			s.mu.Unlock()
			continue
		}
		if err := s.appendServiceEventsLocked(record, []runtimeServiceStatus{serviceStatus}, agboxv1.SandboxState_SANDBOX_STATE_READY); err != nil {
			logAsyncEventAppendFailure(sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Service) appendServiceEventsLocked(record *sandboxRecord, statuses []runtimeServiceStatus, sandboxState agboxv1.SandboxState) error {
	for _, serviceStatus := range statuses {
		if serviceStatus.Ready {
			if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_SERVICE_READY, eventMutation{
				serviceName:  serviceStatus.Name,
				sandboxState: sandboxState,
			}); err != nil {
				return err
			}
			continue
		}
		if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_SERVICE_FAILED, eventMutation{
			serviceName:  serviceStatus.Name,
			errorCode:    "SANDBOX_SERVICE_FAILED",
			errorMessage: serviceStatus.Message,
			sandboxState: sandboxState,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) appendEventLocked(record *sandboxRecord, eventType agboxv1.EventType, mutation eventMutation) error {
	nextSequence := record.nextSequence + 1
	event := &agboxv1.SandboxEvent{
		EventId:      fmt.Sprintf("%s-%d", record.handle.GetSandboxId(), nextSequence),
		Sequence:     nextSequence,
		Cursor:       makeCursor(record.handle.GetSandboxId(), nextSequence),
		SandboxId:    record.handle.GetSandboxId(),
		EventType:    eventType,
		OccurredAt:   timestamppb.Now(),
		Replay:       false,
		Snapshot:     false,
		Phase:        mutation.phase,
		ErrorCode:    mutation.errorCode,
		ErrorMessage: mutation.errorMessage,
		Reason:       mutation.reason,
		ExecId:       mutation.execID,
		ExitCode:     mutation.exitCode,
		SandboxState: mutation.sandboxState,
		ExecState:    mutation.execState,
		ServiceName:  mutation.serviceName,
	}
	if err := s.config.eventStore.Append(record.handle.GetSandboxId(), event); err != nil {
		return err
	}
	record.nextSequence = nextSequence
	record.events = append(record.events, event)
	record.handle.LastEventCursor = event.GetCursor()
	return nil
}

func (s *Service) requireSandboxLocked(sandboxID string) (*sandboxRecord, error) {
	record, ok := s.boxes[sandboxID]
	if !ok {
		return nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, "sandbox %s was not found", sandboxID)
	}
	return record, nil
}

func requireMutableSandbox(record *sandboxRecord) error {
	if !record.recoveredOnly {
		return nil
	}
	return newStatusError(
		codes.FailedPrecondition,
		ReasonSandboxRecoveredOnly,
		"sandbox %s only has recovered event history",
		record.handle.GetSandboxId(),
	)
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

func validateCursorNotStale(record *sandboxRecord, afterSequence uint64) error {
	if afterSequence <= record.nextSequence {
		return nil
	}
	return newStatusError(
		codes.OutOfRange,
		ReasonSandboxEventCursorExpired,
		"cursor sequence %d is outside sandbox %s event history",
		afterSequence,
		record.handle.GetSandboxId(),
	)
}

func logAsyncEventAppendFailure(sandboxID string, eventType agboxv1.EventType, err error) {
	if eventType == agboxv1.EventType_EVENT_TYPE_UNSPECIFIED {
		log.Printf("append sandbox events for %s: %v", sandboxID, err)
		return
	}
	log.Printf("append %s event for sandbox %s: %v", eventType.String(), sandboxID, err)
}

func (s *Service) restorePersistedSandboxes() error {
	sandboxIDs, err := s.config.eventStore.LoadAllSandboxIDs()
	if err != nil {
		return fmt.Errorf("load persisted sandbox ids: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sandboxID := range sandboxIDs {
		events, err := s.config.eventStore.LoadEvents(sandboxID)
		if err != nil {
			return fmt.Errorf("load events for sandbox %s: %w", sandboxID, err)
		}
		if len(events) == 0 {
			continue
		}
		maxSequence, err := s.config.eventStore.MaxSequence(sandboxID)
		if err != nil {
			return fmt.Errorf("load max sequence for sandbox %s: %w", sandboxID, err)
		}
		_, deletedRecorded, err := s.config.eventStore.DeletedAt(sandboxID)
		if err != nil {
			return fmt.Errorf("load deleted metadata for sandbox %s: %w", sandboxID, err)
		}
		record, err := newRecoveredSandboxRecord(events, maxSequence, deletedRecorded)
		if err != nil {
			return fmt.Errorf("restore sandbox %s: %w", sandboxID, err)
		}
		s.boxes[sandboxID] = record
	}
	return nil
}

func newRecoveredSandboxRecord(events []*agboxv1.SandboxEvent, maxSequence uint64, deletedRecorded bool) (*sandboxRecord, error) {
	lastEvent := events[len(events)-1]
	sandboxState := agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].GetSandboxState() == agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
			continue
		}
		sandboxState = events[index].GetSandboxState()
		break
	}
	if sandboxState == agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
		return nil, fmt.Errorf("sandbox %s has no recoverable sandbox state", lastEvent.GetSandboxId())
	}
	return &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId:       lastEvent.GetSandboxId(),
			State:           sandboxState,
			LastEventCursor: lastEvent.GetCursor(),
		},
		events:            events,
		execs:             make(map[string]*agboxv1.ExecStatus),
		execCancel:        make(map[string]context.CancelFunc),
		execArtifacts:     make(map[string]string),
		nextSequence:      maxSequence,
		deletedAtRecorded: deletedRecorded,
		recoveredOnly:     true,
	}, nil
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
		BuiltinResources: slices.Clone(spec.GetBuiltinResources()),
		RequiredServices: cloneServiceSpecs(spec.GetRequiredServices()),
		OptionalServices: cloneServiceSpecs(spec.GetOptionalServices()),
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
		SandboxId:        handle.GetSandboxId(),
		State:            handle.GetState(),
		LastEventCursor:  handle.GetLastEventCursor(),
		Labels:           cloneStringMap(handle.GetLabels()),
		RequiredServices: cloneServiceSpecs(handle.GetRequiredServices()),
		OptionalServices: cloneServiceSpecs(handle.GetOptionalServices()),
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

func (s *Service) finishRecoveredSandboxDeleteLocked(record *sandboxRecord, reason string) error {
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETED,
	}); err != nil {
		return err
	}
	if err := s.config.eventStore.MarkDeleted(record.handle.GetSandboxId(), time.Now()); err != nil {
		return err
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETED
	record.deletedAtRecorded = true
	return nil
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
		EventId:      event.GetEventId(),
		Sequence:     event.GetSequence(),
		Cursor:       event.GetCursor(),
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
