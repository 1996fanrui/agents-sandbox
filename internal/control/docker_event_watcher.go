package control

import (
	"context"
	"log/slog"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// Hardcoded thresholds for crashloop detection.
// These are intentionally not configurable; see design §2 for rationale.
const (
	// crashloopNonRunningWindow is how long a container must remain continuously
	// non-Running before the sandbox is declared Failed.
	crashloopNonRunningWindow = 5 * time.Minute
	// crashloopRunningGuard is how long a container must remain continuously Running
	// before its notRunningSince window is reset. Prevents "Running 10s then crash"
	// cycles from clearing the window on every pass.
	crashloopRunningGuard = 30 * time.Second
	// reconcileTickInterval is how often the 15s periodic reconcile ticker fires.
	reconcileTickInterval = 15 * time.Second
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

// evaluateContainer applies the 5-rule crashloop detection logic to a single container.
// It mutates cs in place (notRunningSince, runningSince) and returns a decision.
// nowFunc provides the current time (injectable for tests).
//
// Rules (verbatim from design §3):
//  1. inspect err → caller logs warn, returns zero decision (conservative, no state change).
//  2. !Exists → immediate fail; window fields not updated.
//  3. Paused → clear both notRunningSince and runningSince; return zero decision.
//  4. Running:
//     - set runningSince if nil.
//     - if now - runningSince >= 30s → clear both timers (stable running, window reset).
//     - else → keep notRunningSince unchanged.
//  5. Non-Running (Exists && !Running && !Paused):
//     - clear runningSince.
//     - set notRunningSince if nil.
//     - if now - notRunningSince > 5min → fail decision (OOM or crashloop error code).
//     - else → return zero decision (within grace period).
func evaluateContainer(result ContainerInspectResult, cs *crashloopState, isPrimary bool, nowFunc func() time.Time) containerEvalDecision {
	now := nowFunc()

	// Rule 2: container does not exist — immediate fail regardless of window.
	if !result.Exists {
		errCode := containerNotRunning
		var errMsg string
		if isPrimary {
			errMsg = "primary container not running (detected during reconciliation)"
		} else {
			errMsg = "companion container not running (detected during reconciliation)"
		}
		if isPrimary {
			return containerEvalDecision{shouldFail: true, errorCode: errCode, errorMessage: errMsg}
		}
		return containerEvalDecision{companionFailed: true, errorCode: errCode, errorMessage: errMsg}
	}

	// Rule 3: paused — clear both timers, no crashloop counting.
	if result.Paused {
		cs.notRunningSince = nil
		cs.runningSince = nil
		return containerEvalDecision{}
	}

	// Rule 4: running.
	if result.Running {
		if cs.runningSince == nil {
			cs.runningSince = &now
		}
		if now.Sub(*cs.runningSince) >= crashloopRunningGuard {
			// Stable running — reset both timers.
			cs.notRunningSince = nil
			cs.runningSince = nil
		}
		// Else: container just started running; keep notRunningSince to guard fast crashloops.
		return containerEvalDecision{}
	}

	// Rule 5: non-Running (Exists && !Running && !Paused).
	cs.runningSince = nil
	if cs.notRunningSince == nil {
		cs.notRunningSince = &now
	}
	if now.Sub(*cs.notRunningSince) <= crashloopNonRunningWindow {
		// Within grace window; no action yet.
		return containerEvalDecision{}
	}

	// 5-minute window expired — determine error code.
	errCode := containerCrashloop
	var errMsg string
	if result.OOMKilled {
		errCode = containerOOM
		if isPrimary {
			errMsg = "primary container OOM killed"
		} else {
			errMsg = "companion container OOM killed"
		}
	} else if isPrimary {
		errMsg = "primary container exited"
	} else {
		errMsg = "companion container exited"
	}

	if isPrimary {
		return containerEvalDecision{shouldFail: true, errorCode: errCode, errorMessage: errMsg}
	}
	return containerEvalDecision{companionFailed: true, errorCode: errCode, errorMessage: errMsg}
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

	// The ticker is created once for the lifetime of run() and shared across reconnects.
	ticker := time.NewTicker(reconcileTickInterval)
	defer ticker.Stop()

	for {
		// Run full reconciliation before subscribing to close the startup/reconnect gap.
		w.reconcileAll(ctx)

		eventCh, errCh := w.service.config.runtimeBackend.WatchContainerEvents(ctx)
		backoff = time.Second // Reset backoff on successful subscription.

		if !w.processEvents(ctx, eventCh, errCh, ticker.C) {
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

// processEvents drains the event channel and ticker until the event channel closes or ctx is cancelled.
// Returns false if ctx was cancelled (caller should exit), true if reconnect is needed.
func (w *dockerEventWatcher) processEvents(ctx context.Context, eventCh <-chan ContainerEvent, errCh <-chan error, tickCh <-chan time.Time) bool {
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
		case <-tickCh:
			w.reconcileAll(ctx)
		}
	}
}

// handleEvent handles a container lifecycle event by calling InspectContainer on
// the affected container and running evaluateContainer, mirroring the reconcileAll path.
// This is level-triggered: the event is just a hint to re-evaluate sooner; all state
// decisions come from real inspect results and the crashloopState timers.
func (w *dockerEventWatcher) handleEvent(ctx context.Context, event ContainerEvent) {
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

	if event.IsCompanionContainer {
		// Find the matching companion and re-evaluate it.
		for i := range record.runtimeState.CompanionContainers {
			cc := &record.runtimeState.CompanionContainers[i]
			if cc.Name != event.CompanionContainerName {
				continue
			}
			result, err := w.service.config.runtimeBackend.InspectContainer(ctx, cc.ContainerName)
			if err != nil {
				w.logger.Warn("handleEvent companion inspect failed",
					slog.String("sandbox_id", event.SandboxID),
					slog.String("container", cc.ContainerName),
					slog.Any("error", err),
				)
				return
			}
			decision := evaluateContainer(result, cc.CrashloopState, false, w.service.config.NowFunc)
			if !decision.companionFailed {
				return
			}
			if err := w.service.appendEventLocked(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED, eventMutation{
				companionContainerName: cc.Name,
				errorCode:              decision.errorCode,
				errorMessage:           decision.errorMessage,
				sandboxState:           agboxv1.SandboxState_SANDBOX_STATE_READY,
			}); err != nil {
				w.logger.Error("handleEvent append companion failed event",
					slog.String("sandbox_id", event.SandboxID),
					slog.Any("error", err),
				)
			}
			return
		}
		return
	}

	// Primary container event — inspect and evaluate.
	result, err := w.service.config.runtimeBackend.InspectContainer(ctx, record.runtimeState.PrimaryContainerName)
	if err != nil {
		w.logger.Warn("handleEvent primary inspect failed",
			slog.String("sandbox_id", event.SandboxID),
			slog.Any("error", err),
		)
		return
	}
	decision := evaluateContainer(result, record.runtimeState.PrimaryCrashloopState, true, w.service.config.NowFunc)
	if !decision.shouldFail {
		return
	}
	w.applyPrimaryFailed(ctx, event.SandboxID, record, decision)
}

// applyPrimaryFailed transitions the sandbox to FAILED under the lock, then kicks off
// a best-effort StopSandbox in a goroutine. Callers must hold s.mu.Lock.
func (w *dockerEventWatcher) applyPrimaryFailed(ctx context.Context, sandboxID string, record *sandboxRecord, decision containerEvalDecision) {
	if err := w.service.appendEventLocked(record, agboxv1.EventType_SANDBOX_FAILED, eventMutation{
		errorCode:    decision.errorCode,
		errorMessage: decision.errorMessage,
		sandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
	}); err != nil {
		w.logger.Error("applyPrimaryFailed append sandbox failed event",
			slog.String("sandbox_id", sandboxID),
			slog.Any("error", err),
		)
		return
	}
	record.handle.State = agboxv1.SandboxState_SANDBOX_STATE_FAILED
	w.logger.Info("sandbox failed during reconciliation",
		slog.String("sandbox_id", sandboxID),
		slog.String("error_code", decision.errorCode),
	)

	// Snapshot the record for the goroutine (avoid holding the lock during docker stop).
	recordSnapshot := record
	go func() {
		if err := w.service.config.runtimeBackend.StopSandbox(ctx, recordSnapshot); err != nil {
			// StopSandbox failure is non-fatal: sandbox is already FAILED. Log and move on.
			// Do NOT emit SANDBOX_STOP_FAILED; the sandbox is already in a terminal FAILED state.
			w.logger.Warn("StopSandbox after sandbox failed: stop containers failed",
				slog.String("sandbox_id", sandboxID),
				slog.Any("error", err),
			)
		}
	}()
}

// reconcileAll inspects all READY sandboxes and applies crashloop window decisions.
// Only READY sandboxes are processed; FAILED/STOPPED/DELETING/DELETED are explicitly skipped.
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
		decision := evaluateContainer(result, record.runtimeState.PrimaryCrashloopState, true, w.service.config.NowFunc)
		if decision.shouldFail {
			w.applyPrimaryFailed(ctx, sandboxID, record, decision)
			continue // Primary failed; skip companion evaluation for this sandbox.
		}

		// Evaluate companion containers.
		for i := range record.runtimeState.CompanionContainers {
			cc := &record.runtimeState.CompanionContainers[i]
			ccResult, err := w.service.config.runtimeBackend.InspectContainer(ctx, cc.ContainerName)
			if err != nil {
				w.logger.Warn("reconcile companion inspect failed",
					slog.String("sandbox_id", sandboxID),
					slog.String("container", cc.ContainerName),
					slog.Any("error", err),
				)
				continue
			}
			ccDecision := evaluateContainer(ccResult, cc.CrashloopState, false, w.service.config.NowFunc)
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
