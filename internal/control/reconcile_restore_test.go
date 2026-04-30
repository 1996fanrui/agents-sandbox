package control

import (
	"context"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// ---- AT-N551: Restore recovery does not single-frame fail an exited primary ----

func TestRestoreRecovery_DoesNotFailOnExitedPrimary(t *testing.T) {
	dbPath := t.TempDir() + "/ids.db"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := epoch

	// Phase 1: create sandbox via normal flow.
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		NowFunc:         fixedClock(t1),
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "n551",
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Phase 2: restart with primary container exited (Exists=true, Running=false).
	// NowFunc returns T1 so notRunningSince will be T1.
	backend2 := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			dockerPrimaryContainerName("n551"): {Exists: true, Running: false},
		},
	}
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		NowFunc:         fixedClock(t1),
		runtimeBackend:  backend2,
	}, dbPath)

	// (5) Immediately after restore, sandbox must still be READY.
	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "n551"})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY immediately after restore, got %s", resp.GetSandbox().GetState())
	}
	second.service.mu.RLock()
	rec2 := second.service.boxes["n551"]
	notRunningSinceBefore := rec2.runtimeState.PrimaryCrashloopState.notRunningSince
	second.service.mu.RUnlock()
	if notRunningSinceBefore == nil {
		t.Fatal("expected notRunningSince to be set after restore with exited primary")
	}
	if !notRunningSinceBefore.Equal(t1) {
		t.Fatalf("expected notRunningSince = T1 (%v), got %v", t1, notRunningSinceBefore)
	}
	second.close()

	// (6) Simulate daemon restart at T2 = T1 + 10min (well past the 5-min window).
	t2 := t1.Add(10 * time.Minute)
	backend3 := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			dockerPrimaryContainerName("n551"): {Exists: true, Running: false},
		},
	}
	third := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		NowFunc:         fixedClock(t2),
		runtimeBackend:  backend3,
	}, dbPath)

	third.service.mu.RLock()
	rec3 := third.service.boxes["n551"]
	notRunningSinceAfter := rec3.runtimeState.PrimaryCrashloopState.notRunningSince
	third.service.mu.RUnlock()

	// (7) notRunningSince should be T2 (reset by new restore), not T1 (not accumulated across restarts).
	if notRunningSinceAfter == nil {
		t.Fatal("expected notRunningSince to be set after second restore")
	}
	if !notRunningSinceAfter.Equal(t2) {
		t.Fatalf("expected notRunningSince = T2 (%v), got %v", t2, notRunningSinceAfter)
	}
	if notRunningSinceAfter.Equal(*notRunningSinceBefore) {
		t.Fatal("notRunningSince must be reset on daemon restart (not accumulated)")
	}

	// (8) No SANDBOX_FAILED event immediately after second restore (window reset to T2).
	third.service.mu.RLock()
	hasFailed := hasEvent(third.service.boxes["n551"], agboxv1.EventType_SANDBOX_FAILED)
	third.service.mu.RUnlock()
	if hasFailed {
		t.Fatal("expected no SANDBOX_FAILED immediately after second restore (window reset to T2)")
	}
}

func TestRestorePersistedSandboxesReappliesNetworkIsolation(t *testing.T) {
	dbPath := t.TempDir() + "/ids.db"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	_, err := first.client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "ready-reapply",
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, "ready-reapply", agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	backend := &fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{
			dockerPrimaryContainerName("ready-reapply"): {Exists: true, Running: true},
		},
	}
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	}, dbPath)
	defer second.close()

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "ready-reapply"})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY after restore, got %s", resp.GetSandbox().GetState())
	}
	if got, want := backend.reapplyNetworkCalls, []string{"ready-reapply"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("expected ReapplyNetworkIsolation calls %v, got %v", want, got)
	}
}

// ---- AT-MQ1S: FAILED sandbox keeps runtimeState for DeleteSandbox ----

func TestRestoreRecovery_FailedSandboxKeepsRuntimeStateForDelete(t *testing.T) {
	dbPath := t.TempDir() + "/ids.db"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Phase 1: create a sandbox with primary container that returns Exists:false
	// so reconcileAll at watcher startup immediately fails it.
	// We use a special backend with an eventCh so that the watcher can process the initial
	// reconcileAll call. We also ensure the sandbox reaches READY first by having the
	// initial inspect return running, then flip it to Exists:false.
	//
	// Actually the simplest approach: normal create path with default fakeRuntimeBackend,
	// then once READY, call reconcileAll manually with Exists:false inspect.
	// For a cleaner test we use the scriptedRuntimeBackend approach with an eventCh.
	epoch := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// The fake backend for phase 1: primary returns running initially so sandbox reaches READY,
	// then we fail it via inject. We use the errCh approach: send an error to trigger
	// reconnect, which calls reconcileAll again with new inspect results.
	eventCh1 := make(chan ContainerEvent, 10)
	errCh1 := make(chan error, 1)
	inspectResults1 := map[string]ContainerInspectResult{
		"fake-primary-mq1s": {Exists: true, Running: true},
	}
	backend1 := &fakeRuntimeBackend{
		inspectResults: inspectResults1,
		eventCh:        eventCh1,
		errCh:          errCh1,
	}
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		NowFunc:         fixedClock(epoch),
		runtimeBackend:  backend1,
	}, dbPath)
	_, err := first.client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "mq1s",
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, "mq1s", agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Flip inspect to Exists:false, then trigger reconnect → reconcileAll detects missing container.
	inspectResults1["fake-primary-mq1s"] = ContainerInspectResult{Exists: false}
	errCh1 <- errStopFailed // any error triggers reconnect + reconcileAll

	waitForSandboxState(t, first.client, "mq1s", agboxv1.SandboxState_SANDBOX_STATE_FAILED)
	first.close()

	// Phase 2: restart — FAILED sandbox must have runtimeState rebuilt for DeleteSandbox.
	backend2 := &fakeRuntimeBackend{}
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		NowFunc:         fixedClock(epoch),
		runtimeBackend:  backend2,
	}, dbPath)

	second.service.mu.RLock()
	rec := second.service.boxes["mq1s"]
	hasRuntimeState := rec != nil && rec.runtimeState != nil
	var primaryContainerName, networkName string
	if hasRuntimeState {
		primaryContainerName = rec.runtimeState.PrimaryContainerName
		networkName = rec.runtimeState.NetworkName
	}
	second.service.mu.RUnlock()

	if !hasRuntimeState {
		t.Fatal("FAILED sandbox must have non-nil runtimeState after restore")
	}
	// The FAILED sandbox was created with fakeRuntimeBackend so naming is "fake-primary-mq1s",
	// but restorePersistedSandboxes uses the deterministic naming convention.
	if want := dockerPrimaryContainerName("mq1s"); primaryContainerName != want {
		t.Fatalf("expected primaryContainerName %q, got %q", want, primaryContainerName)
	}
	if want := dockerNetworkName("mq1s"); networkName != want {
		t.Fatalf("expected networkName %q, got %q", want, networkName)
	}

	// ReapplyNetworkIsolation must NOT be called for FAILED restore.
	if len(backend2.reapplyNetworkCalls) != 0 {
		t.Fatalf("ReapplyNetworkIsolation must not be called for FAILED sandbox, got %d calls", len(backend2.reapplyNetworkCalls))
	}

	// DeleteSandbox must be callable and invoke backend.DeleteSandbox.
	_, err = second.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: "mq1s"})
	if err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(backend2.deleteCalls) == 0 {
		t.Fatal("expected DeleteSandbox to be called on the backend")
	}
	if backend2.deleteCalls[0] != "mq1s" {
		t.Fatalf("expected DeleteSandbox called with mq1s, got %s", backend2.deleteCalls[0])
	}
}
