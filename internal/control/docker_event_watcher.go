package control

import (
	"context"
	"log/slog"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// dockerEventWatcher subscribes to container lifecycle events and updates sandbox state accordingly.
// It runs a reconnect loop: on each (re)connection it first reconciles all READY sandboxes against
// Docker inspect, then processes the live event stream until it breaks.
type dockerEventWatcher struct {
	service *Service
	logger  *slog.Logger
}

func newDockerEventWatcher(service *Service, logger *slog.Logger) *dockerEventWatcher {
	return &dockerEventWatcher{
		service: service,
		logger:  logger,
	}
}

func (w *dockerEventWatcher) run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		// Run full reconciliation before subscribing to close the startup/reconnect gap.
		w.reconcileAll(ctx)

		eventCh, errCh := w.service.config.runtimeBackend.WatchContainerEvents(ctx)
		backoff = time.Second // Reset backoff on successful subscription.

		if !w.processEvents(ctx, eventCh, errCh) {
			return // Context cancelled.
		}

		// Connection lost, apply backoff before retry.
		w.logger.Warn("container event subscription lost, reconnecting", slog.Duration("backoff", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// processEvents drains the event channel until it closes or ctx is cancelled.
// Returns false if ctx was cancelled (caller should exit), true if reconnect is needed.
func (w *dockerEventWatcher) processEvents(ctx context.Context, eventCh <-chan ContainerEvent, errCh <-chan error) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err, ok := <-errCh:
			if !ok {
				return true // Error channel closed, reconnect.
			}
			w.logger.Warn("container event stream error", slog.Any("error", err))
			return true
		case event, ok := <-eventCh:
			if !ok {
				return true // Event channel closed, reconnect.
			}
			w.handleEvent(event)
		}
	}
}

func (w *dockerEventWatcher) handleEvent(event ContainerEvent) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()

	record, ok := w.service.boxes[event.SandboxID]
	if !ok {
		return
	}

	state := record.handle.GetState()

	if event.IsService {
		// Service container event: emit SANDBOX_SERVICE_FAILED but do not change sandbox state.
		if state == agboxv1.SandboxState_SANDBOX_STATE_READY {
			if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_SERVICE_FAILED, eventMutation{
				serviceName:  event.ServiceName,
				errorCode:    containerEventErrorCode(event.Action),
				errorMessage: "service container " + event.Action,
				sandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
			}); err != nil {
				w.logger.Error("append service failed event", slog.String("sandbox_id", event.SandboxID), slog.Any("error", err))
			}
		}
		return
	}

	// Primary container event.
	if state != agboxv1.SandboxState_SANDBOX_STATE_READY {
		return // Expected for STOPPED/DELETING/DELETED/FAILED/PENDING.
	}

	errorCode := containerEventErrorCode(event.Action)
	if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
		errorCode:    errorCode,
		errorMessage: "primary container " + event.Action,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
	}); err != nil {
		w.logger.Error("append sandbox failed event", slog.String("sandbox_id", event.SandboxID), slog.Any("error", err))
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
	w.logger.Info("sandbox failed due to container event",
		slog.String("sandbox_id", event.SandboxID),
		slog.String("action", event.Action),
		slog.String("error_code", errorCode),
	)
}

func containerEventErrorCode(action string) string {
	switch action {
	case "oom":
		return "CONTAINER_OOM"
	default:
		return "CONTAINER_DIED"
	}
}

// reconcileAll inspects all READY sandboxes and fails those whose primary container is no longer running.
func (w *dockerEventWatcher) reconcileAll(ctx context.Context) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()

	for sandboxID, record := range w.service.boxes {
		if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
			continue
		}
		if record.runtimeState == nil {
			continue
		}
		result, err := w.service.config.runtimeBackend.InspectContainer(ctx, record.runtimeState.PrimaryContainerName)
		if err != nil {
			w.logger.Warn("reconcile inspect failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
			continue
		}
		if result.Exists && result.Running {
			continue
		}
		errorCode := "CONTAINER_NOT_RUNNING"
		if result.OOMKilled {
			errorCode = "CONTAINER_OOM"
		}
		if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
			errorCode:    errorCode,
			errorMessage: "primary container not running (detected during reconciliation)",
			sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		}); err != nil {
			w.logger.Error("reconcile append failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
			continue
		}
		record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
		w.logger.Info("sandbox failed during reconciliation", slog.String("sandbox_id", sandboxID), slog.String("error_code", errorCode))
	}
}
