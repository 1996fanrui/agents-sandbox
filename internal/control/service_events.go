package control

import (
	"context"
	"fmt"
	"log/slog"
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
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_STOP_REQUESTED, err)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	go s.completeSandboxStop(sandboxID, "idle_ttl")
}

func (s *Service) cleanupExpiredEvents() error {
	removedSandboxIDs, err := s.config.eventStore.Cleanup(s.config.CleanupTTL)
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
	s.config.Logger.Debug("cleanup expired sandbox events", slog.Int("removed_count", len(removedSandboxIDs)))
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
				s.config.Logger.Warn("cleanup expired sandbox events failed", slog.Any("error", err))
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
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
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
		SandboxState: mutation.sandboxState,
	}

	// Populate the oneof details field based on which mutation fields are set.
	switch {
	case mutation.execID != "":
		event.Details = &agboxv1.SandboxEvent_Exec{
			Exec: &agboxv1.ExecEventDetails{
				ExecId:       mutation.execID,
				ExitCode:     mutation.exitCode,
				ExecState:    mutation.execState,
				ErrorCode:    mutation.errorCode,
				ErrorMessage: mutation.errorMessage,
			},
		}
	case mutation.serviceName != "":
		event.Details = &agboxv1.SandboxEvent_Service{
			Service: &agboxv1.ServiceEventDetails{
				ServiceName:  mutation.serviceName,
				ErrorCode:    mutation.errorCode,
				ErrorMessage: mutation.errorMessage,
			},
		}
	case mutation.phase != "" || mutation.errorCode != "" || mutation.errorMessage != "" || mutation.reason != "":
		event.Details = &agboxv1.SandboxEvent_SandboxPhase{
			SandboxPhase: &agboxv1.SandboxPhaseDetails{
				Phase:        mutation.phase,
				ErrorCode:    mutation.errorCode,
				ErrorMessage: mutation.errorMessage,
				Reason:       mutation.reason,
			},
		}
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
		return nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, map[string]string{"sandbox_id": sandboxID}, "sandbox %s was not found", sandboxID)
	}
	return record, nil
}

func (s *Service) requireExecLocked(execID string) (string, *agboxv1.ExecStatus, error) {
	sandboxID, ok := s.execs[execID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonExecNotFound, map[string]string{"exec_id": execID}, "exec %s was not found", execID)
	}
	record, ok := s.boxes[sandboxID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonSandboxNotFound, map[string]string{"sandbox_id": sandboxID}, "sandbox %s was not found", sandboxID)
	}
	execRecord, ok := record.execs[execID]
	if !ok {
		return "", nil, newStatusError(codes.NotFound, ReasonExecNotFound, map[string]string{"exec_id": execID}, "exec %s was not found", execID)
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
		map[string]string{"sandbox_id": record.handle.GetSandboxId(), "from_sequence": fmt.Sprintf("%d", afterSequence)},
		"sandbox %s event sequence %d is outside retained history",
		record.handle.GetSandboxId(),
		afterSequence,
	)
}

func logAsyncEventAppendFailure(logger *slog.Logger, sandboxID string, eventType agboxv1.EventType, err error) {
	if eventType == agboxv1.EventType_EVENT_TYPE_UNSPECIFIED {
		logger.Error("append sandbox events failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	logger.Error("append event failed", slog.String("sandbox_id", sandboxID), slog.String("event_type", eventType.String()), slog.Any("error", err))
}

func (s *Service) restorePersistedSandboxes(ctx context.Context) error {
	configs, err := s.config.eventStore.LoadAllSandboxConfigs()
	if err != nil {
		return fmt.Errorf("load all sandbox configs: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for sandboxID, createSpec := range configs {
		events, err := s.config.eventStore.LoadEvents(sandboxID)
		if err != nil {
			return fmt.Errorf("load events for sandbox %s: %w", sandboxID, err)
		}
		if len(events) == 0 {
			s.config.Logger.Warn("sandbox config exists but no events, skipping", slog.String("sandbox_id", sandboxID))
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

		// Compute last known sandbox state from events (scan backward for non-UNSPECIFIED SandboxState).
		persistedState := agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].GetSandboxState() != agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
				persistedState = events[i].GetSandboxState()
				break
			}
		}
		if persistedState == agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
			s.config.Logger.Warn("sandbox has no recoverable state, skipping", slog.String("sandbox_id", sandboxID))
			continue
		}

		// Compute lastTerminalRunFinishedAt from events.
		var lastTerminalRunFinishedAt time.Time
		for _, event := range events {
			if event.GetEventType() == agboxv1.EventType_EXEC_FINISHED ||
				event.GetEventType() == agboxv1.EventType_EXEC_CANCELLED {
				eventTime := event.GetOccurredAt().AsTime()
				if eventTime.After(lastTerminalRunFinishedAt) {
					lastTerminalRunFinishedAt = eventTime
				}
			}
		}

		// Extract created_at from the first event (SANDBOX_ACCEPTED).
		var createdAt *timestamppb.Timestamp
		if len(events) > 0 {
			createdAt = events[0].GetOccurredAt()
		}

		// Build the record with nextSequence set before any event append.
		record := &sandboxRecord{
			handle: &agboxv1.SandboxHandle{
				SandboxId:         sandboxID,
				State:             persistedState,
				LastEventSequence: events[len(events)-1].GetSequence(),
				Labels:            cloneStringMap(createSpec.GetLabels()),
				RequiredServices:  cloneServiceSpecs(createSpec.GetRequiredServices()),
				OptionalServices:  cloneServiceSpecs(createSpec.GetOptionalServices()),
				CreatedAt:         createdAt,
				Image:             createSpec.GetImage(),
			},
			createSpec:                cloneCreateSpec(createSpec),
			requiredServices:          cloneServiceSpecs(createSpec.GetRequiredServices()),
			optionalServices:          cloneServiceSpecs(createSpec.GetOptionalServices()),
			events:                    events,
			execs:                     make(map[string]*agboxv1.ExecStatus),
			execCancel:                make(map[string]context.CancelFunc),
			nextSequence:              maxSequence,
			lastTerminalRunFinishedAt: lastTerminalRunFinishedAt,
			deletedAtRecorded:         deletedRecorded,
		}

		// Build runtime state for non-terminal sandboxes.
		if persistedState != agboxv1.SandboxState_SANDBOX_STATE_DELETED &&
			persistedState != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
			serviceContainers := make([]runtimeServiceContainer, 0, len(createSpec.GetRequiredServices())+len(createSpec.GetOptionalServices()))
			for _, svc := range createSpec.GetRequiredServices() {
				serviceContainers = append(serviceContainers, runtimeServiceContainer{
					Name:          svc.GetName(),
					ContainerName: dockerServiceContainerName(sandboxID, svc.GetName()),
					Required:      true,
				})
			}
			for _, svc := range createSpec.GetOptionalServices() {
				serviceContainers = append(serviceContainers, runtimeServiceContainer{
					Name:          svc.GetName(),
					ContainerName: dockerServiceContainerName(sandboxID, svc.GetName()),
					Required:      false,
				})
			}
			record.runtimeState = &sandboxRuntimeState{
				NetworkName:          dockerNetworkName(sandboxID),
				PrimaryContainerName: dockerPrimaryContainerName(sandboxID),
				ServiceContainers:    serviceContainers,
			}
		}

		// State reconciliation based on Docker inspect.
		reconciledState := persistedState
		switch persistedState {
		case agboxv1.SandboxState_SANDBOX_STATE_READY:
			primaryName := dockerPrimaryContainerName(sandboxID)
			inspectResult, inspectErr := s.config.runtimeBackend.InspectContainer(ctx, primaryName)
			if inspectErr != nil {
				return fmt.Errorf("inspect primary container for sandbox %s: %w", sandboxID, inspectErr)
			}
			if !inspectResult.Exists || !inspectResult.Running {
				reconciledState = agboxv1.SandboxState_SANDBOX_STATE_FAILED
				if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
					errorCode:    "CONTAINER_NOT_RUNNING",
					errorMessage: "primary container not running after daemon restart",
					sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
				}); err != nil {
					return fmt.Errorf("append SANDBOX_FAILED for sandbox %s: %w", sandboxID, err)
				}
				record.runtimeState = nil
			}
		case agboxv1.SandboxState_SANDBOX_STATE_STOPPED:
			primaryName := dockerPrimaryContainerName(sandboxID)
			inspectResult, inspectErr := s.config.runtimeBackend.InspectContainer(ctx, primaryName)
			if inspectErr != nil {
				return fmt.Errorf("inspect primary container for sandbox %s: %w", sandboxID, inspectErr)
			}
			if !inspectResult.Exists {
				reconciledState = agboxv1.SandboxState_SANDBOX_STATE_FAILED
				if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
					errorCode:    "CONTAINER_NOT_RUNNING",
					errorMessage: "primary container missing after daemon restart",
					sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
				}); err != nil {
					return fmt.Errorf("append SANDBOX_FAILED for sandbox %s: %w", sandboxID, err)
				}
				record.runtimeState = nil
			}
			// Container exited but exists is expected for STOPPED.
		case agboxv1.SandboxState_SANDBOX_STATE_PENDING:
			reconciledState = agboxv1.SandboxState_SANDBOX_STATE_FAILED
			if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
				errorCode:    "INTERRUPTED_PENDING",
				errorMessage: "sandbox was pending when daemon restarted",
				sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
			}); err != nil {
				return fmt.Errorf("append SANDBOX_FAILED for sandbox %s: %w", sandboxID, err)
			}
			record.runtimeState = nil
		case agboxv1.SandboxState_SANDBOX_STATE_DELETING:
			reconciledState = agboxv1.SandboxState_SANDBOX_STATE_DELETED
			if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
				reason:       "daemon_restart_cleanup",
				sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETED,
			}); err != nil {
				return fmt.Errorf("append SANDBOX_DELETED for sandbox %s: %w", sandboxID, err)
			}
			if err := s.config.eventStore.MarkDeleted(sandboxID, time.Now()); err != nil {
				return fmt.Errorf("mark deleted for sandbox %s: %w", sandboxID, err)
			}
			record.deletedAtRecorded = true
			// Best-effort cleanup of Docker resources left behind by interrupted deletion.
			if record.runtimeState != nil {
				if err := s.config.runtimeBackend.DeleteSandbox(ctx, record); err != nil {
					s.config.Logger.Warn("cleanup Docker resources for deleting sandbox failed",
						slog.String("sandbox_id", sandboxID), slog.Any("error", err))
				}
			}
			record.runtimeState = nil
		case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
			// Already failed, no reconciliation needed.
			record.runtimeState = nil
		case agboxv1.SandboxState_SANDBOX_STATE_DELETED:
			// Already deleted, no reconciliation needed.
			record.runtimeState = nil
		}
		record.handle.State = reconciledState

		// Restore exec states.
		execConfigs, err := s.config.eventStore.LoadExecConfigs(sandboxID)
		if err != nil {
			return fmt.Errorf("load exec configs for sandbox %s: %w", sandboxID, err)
		}
		for _, execCfg := range execConfigs {
			execID := execCfg.GetExecId()
			execState, exitCode, _, errorMsg := resolveExecStateFromEvents(events, execID)

			if execState == agboxv1.ExecState_EXEC_STATE_RUNNING ||
				execState == agboxv1.ExecState_EXEC_STATE_UNSPECIFIED {
				// Exec was running or never started (config saved but event not written) when daemon died.
				if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
					execID:       execID,
					errorCode:    "DAEMON_RESTARTED",
					errorMessage: "exec was running when daemon restarted",
					execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
				}); err != nil {
					return fmt.Errorf("append EXEC_FAILED for exec %s: %w", execID, err)
				}
				execState = agboxv1.ExecState_EXEC_STATE_FAILED
				errorMsg = "exec was running when daemon restarted"
			}
			record.execs[execID] = &agboxv1.ExecStatus{
				ExecId:       execID,
				SandboxId:    sandboxID,
				State:        execState,
				Command:      execCfg.GetCommand(),
				Cwd:          execCfg.GetCwd(),
				EnvOverrides: execCfg.GetEnvOverrides(),
				ExitCode:     exitCode,
				Error:        errorMsg,
			}
			s.execs[execID] = sandboxID
		}

		s.boxes[sandboxID] = record
		s.config.Logger.Info("sandbox restored",
			slog.String("sandbox_id", sandboxID),
			slog.String("persisted_state", persistedState.String()),
			slog.String("reconciled_state", reconciledState.String()),
		)

		// Schedule idle stop for READY sandboxes with terminal run history.
		if reconciledState == agboxv1.SandboxState_SANDBOX_STATE_READY && !lastTerminalRunFinishedAt.IsZero() {
			go s.scheduleIdleStop(sandboxID)
		}
	}
	return nil
}

// resolveExecStateFromEvents scans events to determine the final state of an exec.
func resolveExecStateFromEvents(events []*agboxv1.SandboxEvent, execID string) (agboxv1.ExecState, int32, string, string) {
	state := agboxv1.ExecState_EXEC_STATE_UNSPECIFIED
	var exitCode int32
	var errorCode, errorMsg string
	for _, event := range events {
		execDetails, ok := event.GetDetails().(*agboxv1.SandboxEvent_Exec)
		if !ok || execDetails == nil || execDetails.Exec.GetExecId() != execID {
			continue
		}
		switch event.GetEventType() {
		case agboxv1.EventType_EXEC_STARTED:
			state = agboxv1.ExecState_EXEC_STATE_RUNNING
		case agboxv1.EventType_EXEC_FINISHED:
			state = agboxv1.ExecState_EXEC_STATE_FINISHED
			exitCode = execDetails.Exec.GetExitCode()
		case agboxv1.EventType_EXEC_FAILED:
			state = agboxv1.ExecState_EXEC_STATE_FAILED
			exitCode = execDetails.Exec.GetExitCode()
			errorCode = execDetails.Exec.GetErrorCode()
			errorMsg = execDetails.Exec.GetErrorMessage()
		case agboxv1.EventType_EXEC_CANCELLED:
			state = agboxv1.ExecState_EXEC_STATE_CANCELLED
		}
	}
	return state, exitCode, errorCode, errorMsg
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
			SandboxState: record.handle.GetState(),
			Details: &agboxv1.SandboxEvent_Exec{
				Exec: &agboxv1.ExecEventDetails{
					ExecId:    execRecord.GetExecId(),
					ExecState: execRecord.GetState(),
				},
			},
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
