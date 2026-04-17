package control

import (
	"context"
	"log/slog"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// Error codes used in sandbox failed events.
const (
	// containerCrashloop is emitted when the primary container dies (non-OOM).
	containerCrashloop = "CONTAINER_CRASHLOOP"
	// containerOOM is emitted when the primary container is killed by OOM.
	containerOOM = "CONTAINER_OOM"
	// containerNotRunning is emitted when the container does not exist at inspect time.
	containerNotRunning = "CONTAINER_NOT_RUNNING"
)

// containerEvalDecision captures the outcome of evaluateContainer for a single container.
type containerEvalDecision struct {
	// shouldFail is true when the sandbox should transition to FAILED.
	// Only applicable to primary container evaluation.
	shouldFail bool
	// companionFailed is true when a companion container should emit COMPANION_CONTAINER_FAILED.
	companionFailed bool
	// errorCode is the error code to include in the failed event.
	errorCode string
	// errorMessage is the human-readable message for the failed event.
	errorMessage string
}

// evaluateContainer inspects the given result and returns a decision about whether the
// container (primary or companion) should be considered failed.
//
// Stage 1 semantics: any non-Running state (including Paused) → failed decision.
// Stage 2 will replace this with the 5-minute non-Running window + 30s Running guard.
func evaluateContainer(result ContainerInspectResult, isPrimary bool) containerEvalDecision {
	if result.Running {
		// Container is healthy; no action needed.
		return containerEvalDecision{}
	}

	// Non-running: determine error code.
	errorCode := containerNotRunning
	errorMessage := "primary container not running (detected during reconciliation)"
	if !isPrimary {
		errorMessage = "companion container not running (detected during reconciliation)"
	}
	if result.OOMKilled {
		errorCode = containerOOM
		if isPrimary {
			errorMessage = "primary container OOM killed"
		} else {
			errorMessage = "companion container OOM killed"
		}
	} else if result.Exists {
		// Container exists but is not running (exited, paused, restarting, etc.).
		errorCode = containerCrashloop
		if isPrimary {
			errorMessage = "primary container exited"
		} else {
			errorMessage = "companion container exited"
		}
	}

	if isPrimary {
		return containerEvalDecision{
			shouldFail:   true,
			errorCode:    errorCode,
			errorMessage: errorMessage,
		}
	}
	return containerEvalDecision{
		companionFailed: true,
		errorCode:       errorCode,
		errorMessage:    errorMessage,
	}
}

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
			w.handleEvent(ctx, event)
		}
	}
}

func (w *dockerEventWatcher) handleEvent(_ context.Context, event ContainerEvent) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()

	record, ok := w.service.boxes[event.SandboxID]
	if !ok {
		return
	}

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		return // Expected for STOPPED/DELETING/DELETED/FAILED/PENDING.
	}
	if record.runtimeState == nil {
		return
	}

	// Synthesize a ContainerInspectResult from the event action.
	// Stage 1 preserves the pre-refactor semantics: any die/oom event on a container that was
	// previously running is treated as a failed container (Exists=true, Running=false).
	// Stage 2 will replace this with a real InspectContainer call inside the window evaluation.
	result := containerEventToInspectResult(event)
	if event.IsCompanionContainer {
		decision := evaluateContainer(result, false)
		if !decision.companionFailed {
			return
		}
		if err := w.service.appendEventLocked(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED, eventMutation{
			companionContainerName: event.CompanionContainerName,
			errorCode:              decision.errorCode,
			errorMessage:           decision.errorMessage,
			sandboxState:           agboxv1.SandboxState_SANDBOX_STATE_READY,
		}); err != nil {
			w.logger.Error("append companion container failed event",
				slog.String("sandbox_id", event.SandboxID),
				slog.Any("error", err),
			)
		}
		return
	}

	// Primary container event.
	decision := evaluateContainer(result, true)
	if !decision.shouldFail {
		return
	}
	if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
		errorCode:    decision.errorCode,
		errorMessage: decision.errorMessage,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
	}); err != nil {
		w.logger.Error("append sandbox failed event",
			slog.String("sandbox_id", event.SandboxID),
			slog.Any("error", err),
		)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
	w.logger.Info("sandbox failed due to container event",
		slog.String("sandbox_id", event.SandboxID),
		slog.String("action", event.Action),
		slog.String("error_code", decision.errorCode),
	)
}

// containerEventToInspectResult synthesizes a ContainerInspectResult from a container event.
// This is used in Stage 1 handleEvent to preserve the pre-refactor event-based semantics
// without performing a live InspectContainer call. Stage 2 replaces this with real inspect.
func containerEventToInspectResult(event ContainerEvent) ContainerInspectResult {
	return ContainerInspectResult{
		Exists:    true,
		Running:   false,
		OOMKilled: event.Action == "oom",
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

		// Evaluate primary container.
		result, err := w.service.config.runtimeBackend.InspectContainer(ctx, record.runtimeState.PrimaryContainerName)
		if err != nil {
			w.logger.Warn("reconcile inspect failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
			continue
		}
		decision := evaluateContainer(result, true)
		if decision.shouldFail {
			if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
				errorCode:    decision.errorCode,
				errorMessage: decision.errorMessage,
				sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
			}); err != nil {
				w.logger.Error("reconcile append failed", slog.String("sandbox_id", sandboxID), slog.Any("error", err))
				continue
			}
			record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
			w.logger.Info("sandbox failed during reconciliation",
				slog.String("sandbox_id", sandboxID),
				slog.String("error_code", decision.errorCode),
			)
			continue // Primary failed; skip companion evaluation for this sandbox.
		}

		// Evaluate companion containers.
		for _, cc := range record.runtimeState.CompanionContainers {
			ccResult, err := w.service.config.runtimeBackend.InspectContainer(ctx, cc.ContainerName)
			if err != nil {
				w.logger.Warn("reconcile companion inspect failed",
					slog.String("sandbox_id", sandboxID),
					slog.String("container", cc.ContainerName),
					slog.Any("error", err),
				)
				continue
			}
			ccDecision := evaluateContainer(ccResult, false)
			if !ccDecision.companionFailed {
				continue
			}
			if err := w.service.appendEventLocked(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED, eventMutation{
				companionContainerName: cc.Name,
				errorCode:              ccDecision.errorCode,
				errorMessage:           ccDecision.errorMessage,
				sandboxState:           agboxv1.SandboxState_SANDBOX_STATE_READY,
			}); err != nil {
				w.logger.Error("reconcile companion append failed",
					slog.String("sandbox_id", sandboxID),
					slog.String("container", cc.Name),
					slog.Any("error", err),
				)
			}
		}
	}
}
