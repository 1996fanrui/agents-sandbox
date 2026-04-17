package control

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// ---- AT-GS6I: Short exited (<5min) does not trigger Failed ----

func TestReconcile_ShortExitedWithinWindow(t *testing.T) {
	const sandboxID = "gs6i"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: true},
		},
	}

	// Start: container running, then at T+1m it exits, then at T+3m it runs again.
	// All within the 5-minute window — no FAILED event should ever be emitted.
	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: running → runningSince set.
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+0: unexpected SANDBOX_FAILED")
	}

	// T+31s: still running → runningSince >= 30s → reset both timers.
	now = epoch.Add(31 * time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+31s: unexpected SANDBOX_FAILED")
	}

	// T+1m: container exits.
	now = epoch.Add(1 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false}
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+1m: unexpected SANDBOX_FAILED (window started)")
	}

	// T+4m: still exited, 3 minutes in window — no fail yet.
	now = epoch.Add(4 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+4m: unexpected SANDBOX_FAILED (still within 5min window)")
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("T+4m: expected READY, got %s", record.handle.GetState())
	}
}

// ---- AT-KEWW: Sustained non-Running ≥ 5min triggers Failed + StopSandbox ----

func TestReconcile_SustainedNonRunningTriggersFailed(t *testing.T) {
	const sandboxID = "keww"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false},
		},
	}

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: first exited observation → sets notRunningSince = T+0.
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+0: should not fail immediately")
	}

	// T+5m15s: window exceeded.
	now = epoch.Add(5*time.Minute + 15*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())

	// Wait for async StopSandbox goroutine to complete.
	time.Sleep(20 * time.Millisecond)

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", record.handle.GetState())
	}
	if !hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("expected SANDBOX_FAILED event")
	}
	failedEvents := collectSandboxEvents(record, agboxv1.EventType_SANDBOX_FAILED)
	if got := eventErrorCode(failedEvents[0]); got != containerCrashloop {
		t.Fatalf("expected error_code %s, got %s", containerCrashloop, got)
	}
	if backend.stopCallCount == 0 {
		t.Fatal("expected StopSandbox to be called at least once")
	}
	if backend.stopCalls[0] != sandboxID {
		t.Fatalf("expected StopSandbox called with %s, got %s", sandboxID, backend.stopCalls[0])
	}
}

// ---- AT-X2PB: Fast crashloop (Running <30s) triggers Failed ----

func TestReconcile_FastCrashloopTriggersFailed(t *testing.T) {
	const sandboxID = "x2pb"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false},
		},
	}

	// Simulate: exited at T+0, running 10s at T+1m, exited again at T+1m10s,
	// running 10s at T+2m, exited again at T+2m10s, …
	// notRunningSince is set at T+0 and never cleared because Running guard (30s) is never met.
	// After 5m total elapsed time of continuous non-Running window, it should fail.

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: exited.
	w.reconcileAll(context.Background())

	// T+1m: running briefly (10s only).
	now = epoch.Add(1 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: true}
	w.reconcileAll(context.Background())

	// T+1m10s: exited again.
	now = epoch.Add(1*time.Minute + 10*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false}
	w.reconcileAll(context.Background())

	// T+2m: running again (10s only).
	now = epoch.Add(2 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: true}
	w.reconcileAll(context.Background())

	// T+2m10s: exited again.
	now = epoch.Add(2*time.Minute + 10*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false}
	w.reconcileAll(context.Background())

	// Should not yet be failed (window started at T+0 but Running at T+1m didn't clear it because <30s).
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+2m10s: should not fail yet")
	}

	// T+5m15s: window expires.
	now = epoch.Add(5*time.Minute + 15*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	time.Sleep(20 * time.Millisecond)

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", record.handle.GetState())
	}
	if backend.stopCallCount == 0 {
		t.Fatal("expected StopSandbox to be called")
	}
}

// ---- AT-4TH3: Running ≥ 30s resets the window ----

func TestReconcile_SustainedRunningResetsWindow(t *testing.T) {
	const sandboxID = "4th3"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false},
		},
	}

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: exited → notRunningSince = T+0.
	w.reconcileAll(context.Background())

	// T+2m: still exited (2m into window).
	now = epoch.Add(2 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+2m: unexpected SANDBOX_FAILED")
	}

	// T+2m10s: running starts.
	now = epoch.Add(2*time.Minute + 10*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: true}
	w.reconcileAll(context.Background())

	// T+2m55s: running for 45s (≥ 30s) → both timers cleared.
	now = epoch.Add(2*time.Minute + 55*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	// After stable running, notRunningSince should be nil.
	if record.runtimeState.PrimaryCrashloopState.notRunningSince != nil {
		t.Fatal("T+2m55s: expected notRunningSince to be cleared after 45s running")
	}
	if record.runtimeState.PrimaryCrashloopState.runningSince != nil {
		t.Fatal("T+2m55s: expected runningSince to be cleared after 45s running")
	}

	// T+3m: exited again — new window starts from T+3m.
	now = epoch.Add(3 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false}
	w.reconcileAll(context.Background())

	// T+7m: 4 minutes since new window started — still < 5min.
	now = epoch.Add(7 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+7m: unexpected SANDBOX_FAILED (only 4min in new window)")
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY, got %s", record.handle.GetState())
	}
}

// ---- AT-VEZB: Companion crashloop keeps sandbox READY ----

func TestReconcile_CompanionCrashloopKeepsSandboxReady(t *testing.T) {
	const sandboxID = "vezb"
	const companionName = "sidecar"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID, companionName)
	primaryContainer := record.runtimeState.PrimaryContainerName
	companionContainer := record.runtimeState.CompanionContainers[0].ContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer:   {Exists: true, Running: true},
			companionContainer: {Exists: true, Running: false},
		},
	}

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: companion exits → notRunningSince set for companion.
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+0: unexpected SANDBOX_FAILED (primary still running)")
	}

	// T+5m15s: companion window expired.
	now = epoch.Add(5*time.Minute + 15*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY (companion crash should not fail sandbox), got %s", record.handle.GetState())
	}
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("unexpected SANDBOX_FAILED after companion crash")
	}
	ccEvents := collectSandboxEvents(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED)
	if len(ccEvents) == 0 {
		t.Fatal("expected COMPANION_CONTAINER_FAILED event")
	}
	if got := eventCompanionContainerName(ccEvents[0]); got != companionName {
		t.Fatalf("expected companion name %q, got %q", companionName, got)
	}
	if backend.stopCallCount != 0 {
		t.Fatal("StopSandbox must not be called when only a companion fails")
	}
}

// ---- AT-GLEU: Primary !Exists triggers immediate Failed ----

func TestReconcile_PrimaryMissingImmediatelyFailed(t *testing.T) {
	const sandboxID = "gleu"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: false},
		},
	}

	w := newWatcherForTest(t, record, backend, fixedClock(epoch))
	w.reconcileAll(context.Background())
	time.Sleep(20 * time.Millisecond)

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", record.handle.GetState())
	}
	failedEvents := collectSandboxEvents(record, agboxv1.EventType_SANDBOX_FAILED)
	if len(failedEvents) == 0 {
		t.Fatal("expected SANDBOX_FAILED event")
	}
	if got := eventErrorCode(failedEvents[0]); got != containerNotRunning {
		t.Fatalf("expected error_code %s, got %s", containerNotRunning, got)
	}
	if backend.stopCallCount == 0 {
		t.Fatal("expected StopSandbox to be called")
	}
	// !Exists path must not touch window fields.
	cs := record.runtimeState.PrimaryCrashloopState
	if cs.notRunningSince != nil || cs.runningSince != nil {
		t.Fatal("!Exists path must not set notRunningSince or runningSince")
	}
}

// ---- AT-6RZ1: Companion !Exists triggers immediate COMPANION_CONTAINER_FAILED, sandbox stays READY ----

func TestReconcile_CompanionMissingImmediatelyFailsCompanion(t *testing.T) {
	const sandboxID = "6rz1"
	const companionName = "sidecar"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID, companionName)
	primaryContainer := record.runtimeState.PrimaryContainerName
	companionContainer := record.runtimeState.CompanionContainers[0].ContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer:   {Exists: true, Running: true},
			companionContainer: {Exists: false},
		},
	}

	w := newWatcherForTest(t, record, backend, fixedClock(epoch))
	w.reconcileAll(context.Background())

	// sandbox stays READY.
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY, got %s", record.handle.GetState())
	}
	// COMPANION_CONTAINER_FAILED emitted immediately.
	ccEvents := collectSandboxEvents(record, agboxv1.EventType_COMPANION_CONTAINER_FAILED)
	if len(ccEvents) == 0 {
		t.Fatal("expected COMPANION_CONTAINER_FAILED event")
	}
	if got := eventCompanionContainerName(ccEvents[0]); got != companionName {
		t.Fatalf("expected companion name %q, got %q", companionName, got)
	}
	if got := eventCompanionErrorCode(ccEvents[0]); got != containerNotRunning {
		t.Fatalf("expected error_code %s, got %s", containerNotRunning, got)
	}
	// Companion !Exists must not touch window fields.
	cs := record.runtimeState.CompanionContainers[0].CrashloopState
	if cs.notRunningSince != nil || cs.runningSince != nil {
		t.Fatal("!Exists path must not set notRunningSince or runningSince on companion")
	}
	// No SANDBOX_FAILED, no StopSandbox.
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("unexpected SANDBOX_FAILED")
	}
	if backend.stopCallCount != 0 {
		t.Fatal("StopSandbox must not be called")
	}
}

// ---- AT-PRUX: Paused clears both timers ----

func TestReconcile_PausedClearsBothTimers(t *testing.T) {
	const sandboxID = "prux"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName
	cs := record.runtimeState.PrimaryCrashloopState

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false},
		},
	}

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: exited → notRunningSince = T+0.
	w.reconcileAll(context.Background())
	if cs.notRunningSince == nil {
		t.Fatal("notRunningSince should be set after first exited observation")
	}

	// T+2m: switch to Paused.
	now = epoch.Add(2 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false, Paused: true}
	w.reconcileAll(context.Background())

	if cs.notRunningSince != nil {
		t.Fatal("Paused must clear notRunningSince")
	}
	if cs.runningSince != nil {
		t.Fatal("Paused must clear runningSince")
	}
	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("Paused must not change sandbox state, got %s", record.handle.GetState())
	}
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("Paused must not produce SANDBOX_FAILED")
	}

	// T+3m: Paused → non-Running (exited again). notRunningSince restarts from T+3m.
	now = epoch.Add(3 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	backend.inspectResults[primaryContainer] = ContainerInspectResult{Exists: true, Running: false}
	w.reconcileAll(context.Background())

	if cs.notRunningSince == nil {
		t.Fatal("notRunningSince should be set again after pause ended")
	}
	expected := epoch.Add(3 * time.Minute)
	if !cs.notRunningSince.Equal(expected) {
		t.Fatalf("notRunningSince should be T+3m (=%v), got %v", expected, *cs.notRunningSince)
	}
}

// ---- AT-STV3: StopSandbox failure does not add SANDBOX_STOP_FAILED event ----

func TestReconcile_StopSandboxFailureDoesNotEmitStopFailedEvent(t *testing.T) {
	const sandboxID = "stv3"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	// Capture log output to verify warn log is produced.
	var warnMu sync.Mutex
	var warnMessages []string
	handler := &capturingLogHandler{warnFn: func(msg string) {
		warnMu.Lock()
		warnMessages = append(warnMessages, msg)
		warnMu.Unlock()
	}}
	logger := slog.New(handler)

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false},
		},
		stopSandboxErr: errStopFailed,
	}

	w := newWatcherForTest(t, record, backend, fixedClock(epoch))
	w.logger = logger

	// T+0: start window.
	w.reconcileAll(context.Background())

	// T+5m15s: window expires, triggers Failed + async StopSandbox (which fails).
	now := epoch.Add(5*time.Minute + 15*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	time.Sleep(50 * time.Millisecond) // wait for async goroutine.

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", record.handle.GetState())
	}

	// Exactly one SANDBOX_FAILED event with CONTAINER_CRASHLOOP.
	failedEvents := collectSandboxEvents(record, agboxv1.EventType_SANDBOX_FAILED)
	if len(failedEvents) != 1 {
		t.Fatalf("expected exactly 1 SANDBOX_FAILED event, got %d", len(failedEvents))
	}
	if got := eventErrorCode(failedEvents[0]); got != containerCrashloop {
		t.Fatalf("expected error_code %s, got %s", containerCrashloop, got)
	}

	// No event with error_code = SANDBOX_STOP_FAILED.
	for _, e := range record.events {
		if eventErrorCode(e) == "SANDBOX_STOP_FAILED" {
			t.Fatal("must not have event with error_code SANDBOX_STOP_FAILED")
		}
	}

	// A warn log about StopSandbox failure must have been produced.
	warnMu.Lock()
	msgs := append([]string(nil), warnMessages...)
	warnMu.Unlock()
	found := false
	for _, m := range msgs {
		if m == "StopSandbox after sandbox failed: stop containers failed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warn log about StopSandbox failure, got: %v", msgs)
	}
}

// ---- AT-EI2G: OOMKilled triggers CONTAINER_OOM error code via 5-minute window ----

func TestReconcile_OOMKilledTriggersOOMErrorCode(t *testing.T) {
	const sandboxID = "ei2g"
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	record := sandboxRecordForTest(sandboxID)
	primaryContainer := record.runtimeState.PrimaryContainerName

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			primaryContainer: {Exists: true, Running: false, OOMKilled: true},
		},
	}

	now := epoch
	w := newWatcherForTest(t, record, backend, fixedClock(now))

	// T+0: OOM exited → start window.
	w.reconcileAll(context.Background())

	// T+2m: still OOM-killed, 2min in window → must NOT fail yet.
	now = epoch.Add(2 * time.Minute)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	if hasEvent(record, agboxv1.EventType_SANDBOX_FAILED) {
		t.Fatal("T+2m: unexpected SANDBOX_FAILED (OOM must also respect 5-min window)")
	}
	if backend.stopCallCount != 0 {
		t.Fatal("T+2m: StopSandbox must not be called within window")
	}

	// T+5m15s: window expired.
	now = epoch.Add(5*time.Minute + 15*time.Second)
	w.service.config.NowFunc = fixedClock(now)
	w.reconcileAll(context.Background())
	time.Sleep(20 * time.Millisecond)

	if record.handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", record.handle.GetState())
	}
	failedEvents := collectSandboxEvents(record, agboxv1.EventType_SANDBOX_FAILED)
	if len(failedEvents) == 0 {
		t.Fatal("expected SANDBOX_FAILED event")
	}
	if got := eventErrorCode(failedEvents[0]); got != containerOOM {
		t.Fatalf("expected error_code %s, got %s", containerOOM, got)
	}
	if backend.stopCallCount == 0 {
		t.Fatal("expected StopSandbox to be called")
	}
}
