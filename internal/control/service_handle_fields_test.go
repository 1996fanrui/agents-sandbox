package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// TestSandboxHandleErrorFields verifies that error_code, error_message, and
// state_changed_at are populated when a sandbox transitions to FAILED via
// restart reconciliation (READY container not running).
func TestSandboxHandleErrorFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "error-fields.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Phase 1: Create sandbox and reach READY.
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("err-fields", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	sandboxID := createResp.GetSandbox().GetSandboxId()
	waitForSandboxState(t, first.client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Phase 2: Restart with container not running → FAILED.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: false, Running: false},
		},
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	handle := resp.GetSandbox()
	if handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", handle.GetState())
	}
	if handle.GetErrorCode() != "CONTAINER_NOT_RUNNING" {
		t.Fatalf("expected error_code CONTAINER_NOT_RUNNING, got %q", handle.GetErrorCode())
	}
	if handle.GetErrorMessage() == "" {
		t.Fatal("expected non-empty error_message")
	}
	if handle.GetStateChangedAt() == nil {
		t.Fatal("expected state_changed_at to be set")
	}
	if !handle.GetStateChangedAt().AsTime().After(handle.GetCreatedAt().AsTime()) {
		t.Fatalf("state_changed_at (%v) should be after created_at (%v)",
			handle.GetStateChangedAt().AsTime(), handle.GetCreatedAt().AsTime())
	}
}

// TestRestoreFailedSandboxErrorFields verifies that error fields survive a
// second daemon restart (restored from persisted FAILED state).
func TestRestoreFailedSandboxErrorFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restore-failed.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Phase 1: Create sandbox → READY.
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("restore-fail", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	sandboxID := createResp.GetSandbox().GetSandboxId()
	waitForSandboxState(t, first.client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Phase 2: Restart with container missing → FAILED.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: false, Running: false},
		},
	}, dbPath)
	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", resp.GetSandbox().GetState())
	}
	second.close()

	// Phase 3: Restart again (sandbox already FAILED in persisted state).
	third := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: false, Running: false},
		},
	}, dbPath)

	resp, err = third.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	handle := resp.GetSandbox()
	if handle.GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", handle.GetState())
	}
	if handle.GetErrorCode() != "CONTAINER_NOT_RUNNING" {
		t.Fatalf("expected error_code CONTAINER_NOT_RUNNING, got %q", handle.GetErrorCode())
	}
	if handle.GetErrorMessage() == "" {
		t.Fatal("expected non-empty error_message after second restart")
	}
	if handle.GetStateChangedAt() == nil {
		t.Fatal("expected state_changed_at to be set after second restart")
	}
}

// TestStateChangedAtUpdatesOnTransition verifies that state_changed_at updates
// on each state transition: PENDING → READY → STOPPED.
func TestStateChangedAtUpdatesOnTransition(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	sandboxID := createResp.GetSandbox().GetSandboxId()

	// state_changed_at should be set on create (PENDING).
	createHandle := createResp.GetSandbox()
	if createHandle.GetStateChangedAt() == nil {
		t.Fatal("expected state_changed_at to be set on create")
	}
	pendingChangedAt := createHandle.GetStateChangedAt().AsTime()

	// Wait for READY.
	waitForSandboxState(t, client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	readyHandle := resp.GetSandbox()
	if readyHandle.GetStateChangedAt() == nil {
		t.Fatal("expected state_changed_at to be set at READY")
	}
	readyChangedAt := readyHandle.GetStateChangedAt().AsTime()
	if readyChangedAt.Before(pendingChangedAt) {
		t.Fatalf("state_changed_at at READY (%v) should not be before PENDING (%v)",
			readyChangedAt, pendingChangedAt)
	}

	// Stop the sandbox.
	_, err = client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
	resp, err = client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	stoppedHandle := resp.GetSandbox()
	if stoppedHandle.GetStateChangedAt() == nil {
		t.Fatal("expected state_changed_at to be set at STOPPED")
	}
	stoppedChangedAt := stoppedHandle.GetStateChangedAt().AsTime()
	if stoppedChangedAt.Before(readyChangedAt) {
		t.Fatalf("state_changed_at at STOPPED (%v) should not be before READY (%v)",
			stoppedChangedAt, readyChangedAt)
	}
}

// TestErrorFieldsClearedOnStateTransition verifies that error_code and
// error_message are cleared when a FAILED sandbox transitions to DELETING.
func TestErrorFieldsClearedOnStateTransition(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "clear-error.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Phase 1: Create sandbox → READY.
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("clear-err", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	sandboxID := createResp.GetSandbox().GetSandboxId()
	waitForSandboxState(t, first.client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Phase 2: Restart with container not running → FAILED.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: false, Running: false},
		},
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetErrorCode() == "" {
		t.Fatal("expected error_code to be set while FAILED")
	}

	// Delete the sandbox → transitions from FAILED to DELETING.
	_, err = second.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}

	// Wait for DELETED (passes through DELETING).
	waitForSandboxState(t, second.client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	resp, err = second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	handle := resp.GetSandbox()
	if handle.GetErrorCode() != "" {
		t.Fatalf("expected error_code to be cleared after delete, got %q", handle.GetErrorCode())
	}
	if handle.GetErrorMessage() != "" {
		t.Fatalf("expected error_message to be cleared after delete, got %q", handle.GetErrorMessage())
	}
}
