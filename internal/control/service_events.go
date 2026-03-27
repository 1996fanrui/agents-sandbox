package control

import (
	"context"
	"fmt"
	"log"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const eventRetentionCleanupInterval = 5 * time.Minute

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
		case statusValue, ok := <-statuses:
			if !ok {
				return drained, false
			}
			drained = append(drained, statusValue)
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
	record.handle.LastEventSequence = event.GetSequence()
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

func validateSequenceNotExpired(record *sandboxRecord, afterSequence uint64) error {
	if afterSequence <= record.nextSequence {
		return nil
	}
	return newStatusError(
		codes.OutOfRange,
		ReasonSandboxEventSequenceExpired,
		"sandbox %s event sequence %d is outside retained history",
		record.handle.GetSandboxId(),
		afterSequence,
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
			SandboxId:         lastEvent.GetSandboxId(),
			State:             sandboxState,
			LastEventSequence: lastEvent.GetSequence(),
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
