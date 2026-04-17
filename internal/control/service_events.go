package control

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/coreos/go-systemd/v22/unit"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// effectiveIdleTTL returns the idle TTL for a sandbox. If the sandbox has a
// per-sandbox override (IdleTtl != nil), that value is used; otherwise the
// global daemon IdleTTL is returned.
func effectiveIdleTTL(record *sandboxRecord, globalTTL time.Duration) time.Duration {
	if record.createSpec.GetIdleTtl() != nil {
		return record.createSpec.GetIdleTtl().AsDuration()
	}
	return globalTTL
}

// idleScanAndStop scans all READY sandboxes and stops those that have been idle
// longer than their effective IdleTTL. Idle reference time: lastTerminalRunFinishedAt
// if any exec has completed, otherwise createdAt (fixing the never-ran-exec leak).
// Each sandbox uses its own effective TTL: per-sandbox override if set, else global.
// A TTL <= 0 for a sandbox means idle detection is disabled for that sandbox.
func (s *Service) idleScanAndStop() {
	s.mu.Lock()
	var idleSandboxIDs []string
	for sandboxID, record := range s.boxes {
		if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
			continue
		}
		if hasActiveExec(record) {
			continue
		}
		ttl := effectiveIdleTTL(record, s.config.IdleTTL)
		if ttl <= 0 {
			continue
		}
		// Determine idle reference time.
		idleRef := record.lastTerminalRunFinishedAt
		if idleRef.IsZero() {
			idleRef = record.handle.GetCreatedAt().AsTime()
		}
		if time.Since(idleRef) < ttl {
			continue
		}
		if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOP_REQUESTED, eventMutation{
			reason: "idle_ttl",
		}); err != nil {
			s.config.Logger.Warn("idle scan: append STOP_REQUESTED failed",
				slog.String("sandbox_id", sandboxID), slog.Any("error", err))
			continue
		}
		idleSandboxIDs = append(idleSandboxIDs, sandboxID)
	}
	s.mu.Unlock()

	for _, sandboxID := range idleSandboxIDs {
		go s.completeSandboxStop(sandboxID, "idle_ttl")
	}
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

// cleanupStoppedSandboxes scans all STOPPED sandboxes and triggers deletion
// for those that have been stopped longer than CleanupTTL. The stoppedAt time
// is derived from the SANDBOX_STOPPED event in the event stream.
func (s *Service) cleanupStoppedSandboxes() {
	s.mu.Lock()
	var deleteSandboxIDs []string
	for sandboxID, record := range s.boxes {
		if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_STOPPED {
			continue
		}
		// Find stoppedAt from events (reverse scan for SANDBOX_STOPPED).
		var stoppedAt time.Time
		for i := len(record.events) - 1; i >= 0; i-- {
			if record.events[i].GetEventType() == agboxv1.EventType_SANDBOX_STOPPED {
				stoppedAt = record.events[i].GetOccurredAt().AsTime()
				break
			}
		}
		if stoppedAt.IsZero() {
			s.config.Logger.Warn("cleanup: STOPPED sandbox missing SANDBOX_STOPPED event, skipping",
				slog.String("sandbox_id", sandboxID))
			continue
		}
		if time.Since(stoppedAt) < s.config.CleanupTTL {
			continue
		}
		started, err := s.beginSandboxDeleteLocked(record, "cleanup_ttl")
		if err != nil {
			s.config.Logger.Warn("cleanup: begin delete for stopped sandbox failed",
				slog.String("sandbox_id", sandboxID), slog.Any("error", err))
			continue
		}
		if !started {
			continue
		}
		deleteSandboxIDs = append(deleteSandboxIDs, sandboxID)
	}
	s.mu.Unlock()

	for _, sandboxID := range deleteSandboxIDs {
		go s.completeSandboxDelete(sandboxID, "cleanup_ttl")
	}
}

func (s *Service) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.idleScanAndStop()
			s.cleanupStoppedSandboxes()
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

func drainAvailableCompanionContainerStatuses(statuses <-chan companionContainerStatus) ([]companionContainerStatus, bool) {
	if statuses == nil {
		return nil, false
	}
	drained := make([]companionContainerStatus, 0)
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

func (s *Service) completeCompanionContainerStartup(sandboxID string, statuses <-chan companionContainerStatus) {
	for containerStatus := range statuses {
		s.mu.Lock()
		record, ok := s.boxes[sandboxID]
		if !ok || record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
			s.mu.Unlock()
			continue
		}
		if err := s.appendCompanionContainerEventsLocked(record, []companionContainerStatus{containerStatus}, agboxv1.SandboxState_SANDBOX_STATE_READY); err != nil {
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Service) appendCompanionContainerEventsLocked(record *sandboxRecord, statuses []companionContainerStatus, sandboxState agboxv1.SandboxState) error {
	for _, containerStatus := range statuses {
		if containerStatus.Ready {
			if err := s.appendEventLocked(record, agboxv1.EventType_COMPANION_CONTAINER_READY, eventMutation{
				companionContainerName: containerStatus.Name,
				sandboxState:           sandboxState,
			}); err != nil {
				return err
			}
			continue
		}
		if err := s.appendEventLocked(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED, eventMutation{
			companionContainerName: containerStatus.Name,
			errorCode:              "COMPANION_CONTAINER_FAILED",
			errorMessage:           containerStatus.Message,
			sandboxState:           sandboxState,
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
	case mutation.companionContainerName != "":
		event.Details = &agboxv1.SandboxEvent_CompanionContainer{
			CompanionContainer: &agboxv1.CompanionContainerEventDetails{
				Name:         mutation.companionContainerName,
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

	// Update state_changed_at when the sandbox state actually transitions
	// or when it is set for the first time (e.g. CreateSandbox: UNSPECIFIED->PENDING).
	if mutation.sandboxState != agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED {
		if mutation.sandboxState != record.handle.GetState() || record.handle.StateChangedAt == nil {
			record.handle.StateChangedAt = event.GetOccurredAt()
		}
	}

	// Populate error fields when transitioning INTO FAILED.
	if mutation.sandboxState == agboxv1.SandboxState_SANDBOX_STATE_FAILED &&
		mutation.sandboxState != record.handle.GetState() {
		record.handle.ErrorCode = mutation.errorCode
		record.handle.ErrorMessage = mutation.errorMessage
	}

	// Clear error fields when transitioning OUT OF FAILED.
	if record.handle.GetState() == agboxv1.SandboxState_SANDBOX_STATE_FAILED &&
		mutation.sandboxState != agboxv1.SandboxState_SANDBOX_STATE_UNSPECIFIED &&
		mutation.sandboxState != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		record.handle.ErrorCode = ""
		record.handle.ErrorMessage = ""
	}

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
				SandboxId:           sandboxID,
				State:               persistedState,
				LastEventSequence:   events[len(events)-1].GetSequence(),
				Labels:              cloneStringMap(createSpec.GetLabels()),
				CompanionContainers: cloneCompanionContainerSpecs(createSpec.GetCompanionContainers()),
				CreatedAt:           createdAt,
				Image:               createSpec.GetImage(),
			},
			createSpec:                cloneCreateSpec(createSpec),
			companionContainers:       cloneCompanionContainerSpecs(createSpec.GetCompanionContainers()),
			events:                    events,
			execs:                     make(map[string]*agboxv1.ExecStatus),
			execCancel:                make(map[string]context.CancelFunc),
			nextSequence:              maxSequence,
			lastTerminalRunFinishedAt: lastTerminalRunFinishedAt,
			deletedAtRecorded:         deletedRecorded,
		}

		// Recover state_changed_at from persisted events.
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].GetSandboxState() == persistedState {
				record.handle.StateChangedAt = events[i].GetOccurredAt()
				break
			}
		}
		// Recover error fields for FAILED sandboxes from the last SANDBOX_FAILED event.
		if persistedState == agboxv1.SandboxState_SANDBOX_STATE_FAILED {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].GetEventType() == agboxv1.EventType_SANDBOX_FAILED {
					if phase := events[i].GetSandboxPhase(); phase != nil {
						record.handle.ErrorCode = phase.GetErrorCode()
						record.handle.ErrorMessage = phase.GetErrorMessage()
					}
					break
				}
			}
		}

		// Build runtime state for non-deleted sandboxes.
		// FAILED sandboxes also get a deterministic runtimeState so DeleteSandbox can clean up
		// containers and networks retained for post-mortem diagnosis (design §5).
		if persistedState != agboxv1.SandboxState_SANDBOX_STATE_DELETED {
			companionContainers := make([]runtimeCompanionContainer, 0, len(createSpec.GetCompanionContainers()))
			for _, cc := range createSpec.GetCompanionContainers() {
				companionContainers = append(companionContainers, runtimeCompanionContainer{
					Name:           cc.GetName(),
					ContainerName:  dockerCompanionContainerName(sandboxID, cc.GetName()),
					CrashloopState: &crashloopState{},
				})
			}
			record.runtimeState = &sandboxRuntimeState{
				NetworkName:           dockerNetworkName(sandboxID),
				PrimaryContainerName:  dockerPrimaryContainerName(sandboxID),
				CompanionContainers:   companionContainers,
				PrimaryCrashloopState: &crashloopState{},
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
			if !inspectResult.Exists {
				// Container is gone — fail immediately; no window applies.
				reconciledState = agboxv1.SandboxState_SANDBOX_STATE_FAILED
				if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
					errorCode:    containerNotRunning,
					errorMessage: "primary container not running after daemon restart",
					sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
				}); err != nil {
					return fmt.Errorf("append SANDBOX_FAILED for sandbox %s: %w", sandboxID, err)
				}
				// runtimeState is preserved for DeleteSandbox (even though the container is gone,
				// keeping the state enables best-effort cleanup of the network resource).
			} else {
				// Container exists (running or exited). Always re-apply nftables rules.
				if err := s.config.runtimeBackend.ReapplyNetworkIsolation(ctx, record); err != nil {
					return fmt.Errorf("reapply network isolation for sandbox %s: %w", sandboxID, err)
				}
				if !inspectResult.Running {
					// Container is exited but exists — start the 5-minute non-Running window.
					// notRunningSince is set to now so the first reconcile tick (15s later) does not
					// immediately fail; the window gives unless-stopped time to restart.
					now := s.config.NowFunc()
					record.runtimeState.PrimaryCrashloopState.notRunningSince = &now
				}
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
					errorCode:    containerNotRunning,
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
			// runtimeState was already built above using deterministic naming.
			// Keep it so DeleteSandbox can clean up containers and networks retained for diagnosis.
			// Do NOT call ReapplyNetworkIsolation; nftables rules are not meaningful for FAILED sandboxes.
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

		// Idle stop for restored READY sandboxes is handled by cleanupLoop's
		// periodic idleScanAndStop(), no per-sandbox goroutine needed.
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

// reconcileSandboxSlices runs once at daemon startup, immediately after
// restorePersistedSandboxes. It walks restored records to rebuild the slice
// for every still-active sandbox with cpu/memory limits, then removes any
// lingering agbox-*.slice units whose sandbox id is no longer tracked. Both
// operations are idempotent: missing slices are recreated by EnsureSandboxSlice
// and slices that never existed are silently ignored by RemoveSandboxSlice.
func (s *Service) reconcileSandboxSlices(ctx context.Context) error {
	slice := s.config.sliceManager
	if slice == nil {
		return nil
	}
	s.mu.RLock()
	liveIDs := make(map[string]struct{}, len(s.boxes))
	type rebuildTarget struct {
		sandboxID string
		cpu       int64
		mem       int64
	}
	var rebuilds []rebuildTarget
	var removes []string
	for sandboxID, record := range s.boxes {
		state := record.handle.GetState()
		switch state {
		case agboxv1.SandboxState_SANDBOX_STATE_READY,
			agboxv1.SandboxState_SANDBOX_STATE_PENDING:
			limits, err := buildLimits(record.createSpec)
			if err != nil {
				s.mu.RUnlock()
				return fmt.Errorf("reconcile parse limits for %s: %w", sandboxID, err)
			}
			if limits.CPUMillicores > 0 || limits.MemoryBytes > 0 {
				rebuilds = append(rebuilds, rebuildTarget{sandboxID, limits.CPUMillicores, limits.MemoryBytes})
				liveIDs[sandboxID] = struct{}{}
			}
		case agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
			agboxv1.SandboxState_SANDBOX_STATE_FAILED,
			agboxv1.SandboxState_SANDBOX_STATE_DELETED:
			// Only schedule a Remove if the sandbox spec requested a slice.
			// Calling StopUnit on systemd for every terminal sandbox would hit
			// polkit on non-root daemons and surface "Authentication required".
			limits, err := buildLimits(record.createSpec)
			if err != nil {
				s.mu.RUnlock()
				return fmt.Errorf("reconcile parse limits for %s: %w", sandboxID, err)
			}
			if limits.CPUMillicores > 0 || limits.MemoryBytes > 0 {
				removes = append(removes, sandboxID)
			}
		}
	}
	s.mu.RUnlock()

	for _, target := range rebuilds {
		if err := slice.EnsureSandboxSlice(ctx, target.sandboxID, target.cpu, target.mem); err != nil {
			return fmt.Errorf("reconcile ensure slice for %s: %w", target.sandboxID, err)
		}
	}
	for _, sandboxID := range removes {
		if err := slice.RemoveSandboxSlice(ctx, sandboxID); err != nil {
			return fmt.Errorf("reconcile remove slice for %s: %w", sandboxID, err)
		}
	}
	existing, err := slice.ListSandboxSlices(ctx)
	if err != nil {
		return fmt.Errorf("reconcile list slices: %w", err)
	}
	for _, unitName := range existing {
		sandboxID := sandboxIDFromSliceUnit(unitName)
		if sandboxID == "" {
			continue
		}
		if _, live := liveIDs[sandboxID]; live {
			continue
		}
		if err := slice.RemoveSandboxSlice(ctx, sandboxID); err != nil {
			return fmt.Errorf("reconcile remove orphan slice %s: %w", unitName, err)
		}
	}
	return nil
}

// sandboxIDFromSliceUnit reverses the "agbox-<escaped>.slice" naming to the
// sandbox id used as the key for RemoveSandboxSlice. Returns "" when the
// unit name does not match the expected prefix/suffix.
func sandboxIDFromSliceUnit(unitName string) string {
	const prefix = "agbox-"
	const suffix = ".slice"
	if !strings.HasPrefix(unitName, prefix) || !strings.HasSuffix(unitName, suffix) {
		return ""
	}
	inner := unitName[len(prefix) : len(unitName)-len(suffix)]
	if inner == "" {
		return ""
	}
	return unit.UnitNameUnescape(inner)
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
