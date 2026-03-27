package control

import (
	"context"
	"log/slog"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

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
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_PREPARING, err)
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
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.config.Logger.Warn("sandbox failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	record.runtimeState = result.RuntimeState
	if err := s.appendServiceEventsLocked(record, result.ServiceStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	optionalStatuses, optionalStatusesOpen := drainAvailableRuntimeServiceStatuses(result.OptionalServiceStatuses)
	if err := s.appendServiceEventsLocked(record, optionalStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	}); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_READY, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_READY
	s.config.Logger.Info("sandbox ready", slog.String("sandbox_id", sandboxID))
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
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.config.Logger.Warn("sandbox failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	if err := s.appendServiceEventsLocked(record, result.ServiceStatuses, agboxv1.SandboxState_SANDBOX_STATE_PENDING); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EVENT_TYPE_UNSPECIFIED, err)
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_READY, eventMutation{
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
	}); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_READY, err)
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
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.config.Logger.Warn("sandbox failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_STOPPED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
	}); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_STOPPED, err)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_STOPPED
	s.config.Logger.Info("sandbox stopped", slog.String("sandbox_id", sandboxID))
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
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_FAILED, appendErr)
			return
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		s.config.Logger.Warn("sandbox failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_SANDBOX_DELETED, eventMutation{
		reason:       reason,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_DELETED,
	}); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_SANDBOX_DELETED, err)
		return
	}
	if err := s.config.eventStore.MarkDeleted(sandboxID, time.Now()); err != nil {
		s.config.Logger.Warn("mark sandbox deleted failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_DELETED
	record.deletedAtRecorded = true
	s.config.Logger.Info("sandbox deleted", slog.String("sandbox_id", sandboxID))
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
		execErrorMessage := runErr.Error()
		if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_FAILED, eventMutation{
			execID:       execID,
			execState:    agboxv1.ExecState_EXEC_STATE_FAILED,
			exitCode:     result.ExitCode,
			errorCode:    "EXEC_RUN_FAILED",
			errorMessage: execErrorMessage,
		}); err != nil {
			logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EXEC_FAILED, err)
			return
		}
		delete(record.execCancel, execID)
		execRecord.State = agboxv1.ExecState_EXEC_STATE_FAILED
		execRecord.ExitCode = result.ExitCode
		execRecord.Error = execErrorMessage
		s.config.Logger.Warn("exec failed", slog.String("sandbox_id", sandboxID), slog.String("exec_id", execID), slog.Int("exit_code", int(result.ExitCode)), slog.Any("error", runErr))
		return
	}
	if err := s.appendEventLocked(record, agboxv1.EventType_EXEC_FINISHED, eventMutation{
		execID:    execID,
		execState: agboxv1.ExecState_EXEC_STATE_FINISHED,
		exitCode:  result.ExitCode,
	}); err != nil {
		logAsyncEventAppendFailure(s.config.Logger, sandboxID, agboxv1.EventType_EXEC_FINISHED, err)
		return
	}
	delete(record.execCancel, execID)
	execRecord.State = agboxv1.ExecState_EXEC_STATE_FINISHED
	execRecord.ExitCode = result.ExitCode
	s.config.Logger.Info("exec finished", slog.String("sandbox_id", sandboxID), slog.String("exec_id", execID), slog.Int("exit_code", int(result.ExitCode)))
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
