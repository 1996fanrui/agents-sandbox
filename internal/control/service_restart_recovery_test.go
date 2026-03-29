package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

func TestRestoredSandboxFullOperations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Phase 1: Create sandbox to READY with the first service.
	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("restored-ops", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Phase 2: Restart with inspect returning container running.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: true},
		},
		EventRetentionTTL: time.Hour,
	}, dbPath)

	// Verify sandbox is READY (not recoveredOnly).
	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY, got %s", resp.GetSandbox().GetState())
	}

	// Verify CreateExec works on restored sandbox.
	_, err = second.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "restored-exec",
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec on restored sandbox failed: %v", err)
	}
	waitForExecState(t, second.client, "restored-exec", agboxv1.ExecState_EXEC_STATE_FINISHED)

	// Verify StopSandbox works on restored sandbox.
	_, err = second.client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("StopSandbox on restored sandbox failed: %v", err)
	}
	waitForSandboxState(t, second.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
}

func TestRestoreReadySandboxContainerRunning(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("ready-running", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: true},
		},
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected READY, got %s", resp.GetSandbox().GetState())
	}
}

func TestRestoreReadySandboxContainerExited(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("ready-exited", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// Container exited or missing → should become FAILED.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: false},
		},
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("expected FAILED, got %s", resp.GetSandbox().GetState())
	}
}

func TestRestoreStoppedSandbox(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("stopped-restore", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	_, err = first.client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
	first.close()

	// Container exited but exists is expected for STOPPED.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: false},
		},
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_STOPPED {
		t.Fatalf("expected STOPPED, got %s", resp.GetSandbox().GetState())
	}

	// Verify ResumeSandbox works.
	_, err = second.client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("ResumeSandbox on restored stopped sandbox failed: %v", err)
	}
	waitForSandboxState(t, second.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
}

func TestRestoreExecState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("exec-restore", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Create an exec that finishes.
	_, err = first.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "finished-exec",
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, first.client, "finished-exec", agboxv1.ExecState_EXEC_STATE_FINISHED)
	first.close()

	// Restart with container running.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: true},
		},
	}, dbPath)

	// Verify finished exec is restored.
	execResp, err := second.client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: "finished-exec"})
	if err != nil {
		t.Fatalf("GetExec for finished exec failed: %v", err)
	}
	if execResp.GetExec().GetState() != agboxv1.ExecState_EXEC_STATE_FINISHED {
		t.Fatalf("expected FINISHED, got %s", execResp.GetExec().GetState())
	}
}

func TestRestoreIdleTTLReschedule(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         50 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("idle-ttl-restore", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Create and finish an exec to set lastTerminalRunFinishedAt.
	_, err = first.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "ttl-exec",
		Command:   []string{"echo"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, first.client, "ttl-exec", agboxv1.ExecState_EXEC_STATE_FINISHED)
	first.close()

	// Restart with very short idle TTL. The restored sandbox should have
	// lastTerminalRunFinishedAt set, triggering idle stop scheduling.
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         50 * time.Millisecond,
		runtimeBackend: &scriptedRuntimeBackend{
			inspectResult: ContainerInspectResult{Exists: true, Running: true},
		},
	}, dbPath)

	// The sandbox should eventually be stopped by idle TTL.
	waitForSandboxState(t, second.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
}

func TestDockerEventPrimaryContainerDie(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh := make(chan ContainerEvent, 10)
	errCh := make(chan error, 1)
	backend := fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{},
		eventCh:        eventCh,
		errCh:          errCh,
	}

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	}, dbPath)

	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("event-die", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Inject a "die" event for the primary container.
	eventCh <- ContainerEvent{
		SandboxID:     createResp.GetSandbox().GetSandboxId(),
		ContainerName: "fake-primary-" + createResp.GetSandbox().GetSandboxId(),
		Action:        "die",
	}

	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_FAILED)

	// Verify SANDBOX_FAILED event is present with correct error code.
	stream, err := harness.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, e := range items {
			if e.GetEventType() == agboxv1.EventType_SANDBOX_FAILED {
				return true
			}
		}
		return false
	})
	var found bool
	for _, e := range events {
		if e.GetEventType() == agboxv1.EventType_SANDBOX_FAILED {
			if eventErrorCode(e) != "CONTAINER_DIED" {
				t.Fatalf("expected error_code CONTAINER_DIED, got %s", eventErrorCode(e))
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected SANDBOX_FAILED event")
	}
}

func TestDockerEventOOM(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh := make(chan ContainerEvent, 10)
	errCh := make(chan error, 1)
	backend := fakeRuntimeBackend{
		inspectResults: map[string]ContainerInspectResult{},
		eventCh:        eventCh,
		errCh:          errCh,
	}

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	}, dbPath)

	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("event-oom", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Inject an "oom" event for the primary container.
	eventCh <- ContainerEvent{
		SandboxID:     createResp.GetSandbox().GetSandboxId(),
		ContainerName: "fake-primary-" + createResp.GetSandbox().GetSandboxId(),
		Action:        "oom",
	}

	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_FAILED)

	// Verify SANDBOX_FAILED event with OOM error code.
	stream, err := harness.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, e := range items {
			if e.GetEventType() == agboxv1.EventType_SANDBOX_FAILED {
				return true
			}
		}
		return false
	})
	var found bool
	for _, e := range events {
		if e.GetEventType() == agboxv1.EventType_SANDBOX_FAILED {
			if eventErrorCode(e) != "CONTAINER_OOM" {
				t.Fatalf("expected error_code CONTAINER_OOM, got %s", eventErrorCode(e))
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected SANDBOX_FAILED event with OOM error code")
	}
}

func TestDockerEventReconnectReconcile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventCh := make(chan ContainerEvent, 10)
	errCh := make(chan error, 10)
	// Initial inspect returns running so reconcileAll at startup is a no-op.
	inspectResults := map[string]ContainerInspectResult{}
	backend := fakeRuntimeBackend{
		inspectResults: inspectResults,
		eventCh:        eventCh,
		errCh:          errCh,
	}

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	}, dbPath)

	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("reconnect-reconcile", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Simulate container exited and event stream disconnected.
	primaryName := "fake-primary-" + createResp.GetSandbox().GetSandboxId()
	inspectResults[primaryName] = ContainerInspectResult{Exists: true, Running: false}
	errCh <- errors.New("connection lost")

	// The watcher will reconnect and reconcileAll will detect the exited container.
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_FAILED)
}

// TestEndToEndRestartRecoveryWithMockDocker verifies the full restart recovery
// pipeline: real bbolt persistence → mock Docker inspect → correct state
// reconciliation → restored sandbox fully operational (including exec).
//
// Unlike unit tests that use fakeRuntimeBackend (fake container names like
// "fake-primary-*"), this test uses dockerRuntimeBackend backed by a mock HTTP
// server. Container names are computed deterministically by the real naming
// functions ("agbox-primary-*", "agbox-net-*"), the persistence layer stores
// real CreateSpec via proto.Marshal, the recovery path calls real
// ContainerInspect via Docker SDK, and the event watcher subscribes via real
// client.Events() API.
func TestEndToEndRestartRecoveryWithMockDocker(t *testing.T) {
	const sandboxID = "e2e-restart"
	const image = "ghcr.io/agents-sandbox/coding-runtime:test"
	primaryContainer := dockerPrimaryContainerName(sandboxID)
	networkName := dockerNetworkName(sandboxID)

	dbPath := filepath.Join(t.TempDir(), "e2e-restart.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- Phase 1: Create sandbox with first service using mock Docker ----
	phase1Backend := newDockerRuntimeBackendForTest(t, newPhase1Handler(t, sandboxID, primaryContainer, networkName, image))

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  phase1Backend,
	}, dbPath)

	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest(sandboxID, image))
	if err != nil {
		t.Fatalf("Phase 1: CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	// ---- Phase 2: Restart with container still running ----
	phase2Backend := newDockerRuntimeBackendForTest(t, newRecoveryHandler(t, primaryContainer, true, 0))

	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  phase2Backend,
	}, dbPath)

	resp, err := second.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("Phase 2: GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("Phase 2: expected READY, got %s", resp.GetSandbox().GetState())
	}

	// Verify CreateExec works on the restored sandbox.
	_, err = second.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: sandboxID,
		ExecId:    "e2e-exec",
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Phase 2: CreateExec failed: %v", err)
	}
	waitForExecState(t, second.client, "e2e-exec", agboxv1.ExecState_EXEC_STATE_FINISHED)
	second.close()

	// ---- Phase 3: Restart with container exited → FAILED ----
	phase3Backend := newDockerRuntimeBackendForTest(t, newRecoveryHandler(t, primaryContainer, false, 137))

	third := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  phase3Backend,
	}, dbPath)

	resp, err = third.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if err != nil {
		t.Fatalf("Phase 3: GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_FAILED {
		t.Fatalf("Phase 3: expected FAILED, got %s", resp.GetSandbox().GetState())
	}
}

// newPhase1Handler returns a mock Docker HTTP handler that supports the full
// sandbox creation flow: image inspect, network create, container create,
// container start, container inspect (wait running), and events long-poll.
func newPhase1Handler(t *testing.T, sandboxID, primaryContainer, networkName, image string) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		// ensureDockerImage: image already present locally
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			writeDockerJSON(t, w, map[string]string{"Id": "sha256:test"})

		// dockerNetworkCreate
		case r.Method == http.MethodPost && path == "/networks/create":
			writeDockerJSON(t, w, map[string]string{"Id": "net-1"})

		// dockerContainerCreate
		case r.Method == http.MethodPost && path == "/containers/create":
			name := r.URL.Query().Get("name")
			writeDockerJSON(t, w, map[string]string{"Id": name})

		// dockerContainerStart
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)

		// dockerWaitContainerRunning / InspectContainer
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/json") && strings.Contains(path, "/containers/"):
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: true, Status: "running"},
				},
			})

		// Docker events: long-poll until client disconnects
		case r.Method == http.MethodGet && path == "/events":
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer does not support flushing")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			<-r.Context().Done()

		default:
			t.Logf("Phase 1: unhandled Docker API request: %s %s (ignored)", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// newRecoveryHandler returns a mock Docker HTTP handler for the restart
// recovery phase. It handles:
//   - ContainerInspect (recovery path + exec pre-checks)
//   - Events long-poll
//   - Exec create/start/inspect (when container is running)
func newRecoveryHandler(t *testing.T, primaryContainer string, running bool, exitCode int) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		// ContainerInspect: used by InspectContainer during recovery,
		// dockerContainerMustExist, and dockerContainerEnsureRunning for exec.
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/json") && strings.Contains(path, "/containers/"):
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{
						Running:  running,
						Status:   boolToStatus(running),
						ExitCode: exitCode,
					},
				},
			})

		// Docker events: long-poll until client disconnects
		case r.Method == http.MethodGet && path == "/events":
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer does not support flushing")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			<-r.Context().Done()

		// ContainerExecCreate: for exec on the restored sandbox
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec") && strings.Contains(path, "/containers/"):
			writeDockerJSON(t, w, map[string]string{"Id": "mock-exec-1"})

		// ContainerExecAttach (start): return a hijacked stream with output
		case r.Method == http.MethodPost && path == "/exec/mock-exec-1/start":
			var req container.ExecStartOptions
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode exec start: %v", err)
			}
			writeHijackedDockerStream(t, w, func(writer io.Writer) {
				if _, err := stdcopy.NewStdWriter(writer, stdcopy.Stdout).Write([]byte("hello\n")); err != nil {
					t.Fatalf("write stdout: %v", err)
				}
			})

		// ContainerExecInspect: return exit code 0
		case r.Method == http.MethodGet && path == "/exec/mock-exec-1/json":
			writeDockerJSON(t, w, container.ExecInspect{
				ExecID:  "mock-exec-1",
				Running: false,
			})

		// Label-based filtering queries (used by WatchContainerEvents filter args)
		case r.Method == http.MethodGet && strings.Contains(path, "/containers/json"):
			writeDockerJSON(t, w, []struct{}{})

		default:
			t.Logf("Recovery handler: unhandled Docker API request: %s %s (ignored)", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func boolToStatus(running bool) string {
	if running {
		return "running"
	}
	return "exited"
}
