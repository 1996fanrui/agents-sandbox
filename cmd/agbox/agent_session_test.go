package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/protobuf/types/known/durationpb"
)

// --- Mock types for unit-testing runLongRunningSession and runInteractiveSession ---

// mockEventStream implements rawclient.SandboxEventStream for tests.
type mockEventStream struct {
	events chan *agboxv1.SandboxEvent
	err    error
}

func (m *mockEventStream) Recv() (*agboxv1.SandboxEvent, error) {
	event, ok := <-m.events
	if !ok {
		if m.err != nil {
			return nil, m.err
		}
		return nil, io.EOF
	}
	return event, nil
}

func (m *mockEventStream) Close() error { return nil }

// mockAgentClient implements sandboxExecClient for unit tests of agent session functions.
type mockAgentClient struct {
	createSandboxFn func(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	getSandboxFn    func(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	deleteSandboxFn func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	createExecFn    func(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	getExecFn       func(context.Context, string) (*agboxv1.GetExecResponse, error)
	cancelExecFn    func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	subscribeFn     func(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)

	// Track calls for assertions.
	createSandboxReq *agboxv1.CreateSandboxRequest
	deleteCalled     bool
	cancelCalled     bool
}

func (m *mockAgentClient) CreateSandbox(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	m.createSandboxReq = req
	if m.createSandboxFn != nil {
		return m.createSandboxFn(context.Background(), req)
	}
	return &agboxv1.CreateSandboxResponse{}, nil
}

func (m *mockAgentClient) GetSandbox(_ context.Context, id string) (*agboxv1.GetSandboxResponse, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(context.Background(), id)
	}
	return &agboxv1.GetSandboxResponse{}, nil
}

func (m *mockAgentClient) DeleteSandbox(_ context.Context, id string) (*agboxv1.AcceptedResponse, error) {
	m.deleteCalled = true
	if m.deleteSandboxFn != nil {
		return m.deleteSandboxFn(context.Background(), id)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (m *mockAgentClient) ListSandboxes(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAgentClient) DeleteSandboxes(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAgentClient) CreateExec(_ context.Context, req *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	if m.createExecFn != nil {
		return m.createExecFn(context.Background(), req)
	}
	return &agboxv1.CreateExecResponse{}, nil
}

func (m *mockAgentClient) GetExec(_ context.Context, id string) (*agboxv1.GetExecResponse, error) {
	if m.getExecFn != nil {
		return m.getExecFn(context.Background(), id)
	}
	return &agboxv1.GetExecResponse{}, nil
}

func (m *mockAgentClient) CancelExec(_ context.Context, id string) (*agboxv1.AcceptedResponse, error) {
	m.cancelCalled = true
	if m.cancelExecFn != nil {
		return m.cancelExecFn(context.Background(), id)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (m *mockAgentClient) SubscribeSandboxEvents(_ context.Context, sandboxID string, seq uint64, snapshot bool) (rawclient.SandboxEventStream, error) {
	if m.subscribeFn != nil {
		return m.subscribeFn(context.Background(), sandboxID, seq, snapshot)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAgentClient) StopSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAgentClient) ResumeSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAgentClient) Close() error { return nil }

// newReadyMock creates a mockAgentClient that returns a READY sandbox for
// CreateSandbox and GetSandbox. The returned eventCh can be used to inject
// exec events into the subscribe stream.
func newReadyMock(eventCh chan *agboxv1.SandboxEvent) *mockAgentClient {
	return &mockAgentClient{
		createSandboxFn: func(_ context.Context, _ *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sb-001",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
		getSandboxFn: func(_ context.Context, id string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         id,
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 2,
				},
			}, nil
		},
		// Default DeleteSandbox returns success (for deleteAndWait in cleanup).
		deleteSandboxFn: func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		subscribeFn: func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return &mockEventStream{events: eventCh}, nil
		},
	}
}

// --- Tests ---

func TestRunAgentSession_InteractiveTTYCheck(t *testing.T) {
	// In test environment, stdin is not a TTY, so interactive mode should fail
	// before using the client at all.
	var stderr bytes.Buffer
	err := runInteractiveSession(context.Background(), nil, agentSessionArgs{
		mode:    agentModeInteractive,
		command: []string{"echo"},
	}, "test", &stderr)
	if err == nil {
		t.Fatal("expected TTY error")
	}
	if !strings.Contains(err.Error(), "stdin is not a TTY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentSession_LongRunningNoTTY(t *testing.T) {
	// Long-running mode should NOT fail with TTY error; it proceeds to CreateSandbox.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	getExecCalls := 0
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1", StdoutLogPath: "/logs/stdout", StderrLogPath: "/logs/stderr"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		getExecCalls++
		if getExecCalls == 1 {
			// Baseline: running.
			return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		}
		// Terminal: finished.
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 4, 0), nil
	}

	// Send an event to unblock the wait loop.
	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-1", Sequence: 4, SandboxId: "sb-001",
		Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"sleep", "infinity"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify no TTY error occurred (we reached sandbox creation).
	if mock.createSandboxReq == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
}

func TestRunAgentSession_LongRunningIdleTTL(t *testing.T) {
	// Verify idle_ttl=0 in CreateSandbox request.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		// Return terminal immediately so the test finishes.
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 3, 0), nil
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify idle_ttl was set to 0.
	req := mock.createSandboxReq
	if req == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
	idleTTL := req.GetCreateSpec().GetIdleTtl()
	if idleTTL == nil {
		t.Fatal("expected idle_ttl to be set")
	}
	expected := durationpb.New(0)
	if idleTTL.GetSeconds() != expected.GetSeconds() || idleTTL.GetNanos() != expected.GetNanos() {
		t.Fatalf("expected idle_ttl=0, got %v", idleTTL)
	}
}

func TestRunAgentSession_LongRunningOutput(t *testing.T) {
	// Verify stdout=sandbox_id, stderr has status info.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1", StdoutLogPath: "/logs/exec-1.stdout.log", StderrLogPath: "/logs/exec-1.stderr.log"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 3, 0), nil
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo", "hello"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// stdout should contain only the sandbox_id.
	if got := strings.TrimSpace(stdout.String()); got != "sb-001" {
		t.Fatalf("expected stdout=%q, got %q", "sb-001", got)
	}

	// stderr should contain key access info.
	stderrStr := stderr.String()
	for _, want := range []string{
		"Sandbox ID: sb-001",
		"Exec ID:    exec-1",
		"echo hello",
		"/logs/exec-1.stdout.log",
		"/logs/exec-1.stderr.log",
		"Exec finished (exit_code=0)",
	} {
		if !strings.Contains(stderrStr, want) {
			t.Fatalf("stderr missing %q, got:\n%s", want, stderrStr)
		}
	}
}

func TestRunAgentSession_LongRunningNoDelete(t *testing.T) {
	// Verify DeleteSandbox is NOT called on successful exec delivery + completion.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 3, 0), nil
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.deleteCalled {
		t.Fatal("expected DeleteSandbox NOT to be called on success")
	}
}

func TestRunAgentSession_LongRunningExecFailCleanup(t *testing.T) {
	// Verify DeleteSandbox IS called when CreateExec fails.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	// Make GetSandbox return DELETED to satisfy deleteAndWait.
	origGetSandbox := mock.getSandboxFn
	deletePhase := false
	mock.getSandboxFn = func(ctx context.Context, id string) (*agboxv1.GetSandboxResponse, error) {
		if deletePhase {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId: id,
					State:     agboxv1.SandboxState_SANDBOX_STATE_DELETED,
				},
			}, nil
		}
		return origGetSandbox(ctx, id)
	}
	mock.deleteSandboxFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
		deletePhase = true
		return &agboxv1.AcceptedResponse{Accepted: true}, nil
	}
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return nil, fmt.Errorf("exec creation failed")
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error from CreateExec failure")
	}

	if !mock.deleteCalled {
		t.Fatal("expected DeleteSandbox to be called on CreateExec failure")
	}
}

func TestRunLongRunningSession_ExecSuccess(t *testing.T) {
	// Exec FINISHED with exit_code=0.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	getExecCalls := 0
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		getExecCalls++
		if getExecCalls == 1 {
			return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		}
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 4, 0), nil
	}

	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-1", Sequence: 4, SandboxId: "sb-001",
		Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected exit code 0, got error: %v", err)
	}
	if !strings.Contains(stderr.String(), "Exec finished (exit_code=0)") {
		t.Fatalf("expected success message in stderr, got:\n%s", stderr.String())
	}
}

func TestRunLongRunningSession_ExecFailed(t *testing.T) {
	// Exec FAILED with exit_code=0 and an error message → exit 125.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	getExecCalls := 0
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		getExecCalls++
		if getExecCalls == 1 {
			return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		}
		return &agboxv1.GetExecResponse{
			Exec: &agboxv1.ExecStatus{
				ExecId:            "exec-1",
				SandboxId:         "sb-001",
				State:             agboxv1.ExecState_EXEC_STATE_FAILED,
				ExitCode:          0,
				Error:             "container OOM",
				LastEventSequence: 4,
			},
		}, nil
	}

	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-1", Sequence: 4, SandboxId: "sb-001",
		Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if exitCodeForError(err) != 125 {
		t.Fatalf("expected exit code 125, got %d (err=%v)", exitCodeForError(err), err)
	}
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Exec failed (exit_code=0)") {
		t.Fatalf("expected failure message, got:\n%s", stderrStr)
	}
	if !strings.Contains(stderrStr, "Error: container OOM") {
		t.Fatalf("expected error detail, got:\n%s", stderrStr)
	}
}

func TestRunLongRunningSession_ExecNonZeroExit(t *testing.T) {
	// Exec FINISHED with exit_code=42 → exit 42.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	getExecCalls := 0
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		getExecCalls++
		if getExecCalls == 1 {
			return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		}
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 4, 42), nil
	}

	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-1", Sequence: 4, SandboxId: "sb-001",
		Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"false"},
	}, "test", &stdout, &stderr)
	if exitCodeForError(err) != 42 {
		t.Fatalf("expected exit code 42, got %d (err=%v)", exitCodeForError(err), err)
	}
	if !strings.Contains(stderr.String(), "Exec finished (exit_code=42)") {
		t.Fatalf("expected exit code message, got:\n%s", stderr.String())
	}
}

func TestRunLongRunningSession_ExecCancelled(t *testing.T) {
	// Exec CANCELLED → exit 125.
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	getExecCalls := 0
	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		getExecCalls++
		if getExecCalls == 1 {
			return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		}
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_CANCELLED, 4, 0), nil
	}

	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-1", Sequence: 4, SandboxId: "sb-001",
		Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if exitCodeForError(err) != 125 {
		t.Fatalf("expected exit code 125, got %d (err=%v)", exitCodeForError(err), err)
	}
	if !strings.Contains(stderr.String(), "Exec cancelled") {
		t.Fatalf("expected cancel message, got:\n%s", stderr.String())
	}
}

func TestRunLongRunningSession_CtrlCDetach(t *testing.T) {
	// Signal after exec delivery → detach, no delete, no cancel.
	eventCh := make(chan *agboxv1.SandboxEvent) // unbuffered; blocks until signal
	mock := newReadyMock(eventCh)

	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
	}

	// Run in goroutine so we can inject a signal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		resultCh <- runLongRunningSession(ctx, mock, agentSessionArgs{
			mode:    agentModeLongRunning,
			command: []string{"sleep", "infinity"},
		}, "test", &stdout, &stderr)
	}()

	// Wait for "Waiting for exec" to appear, then cancel via context to simulate detach.
	cancel()

	err := <-resultCh
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}

	// DeleteSandbox should NOT be called (detachSuccess=true after exec delivery).
	if mock.deleteCalled {
		t.Fatal("expected DeleteSandbox NOT to be called on detach")
	}
	if mock.cancelCalled {
		t.Fatal("expected CancelExec NOT to be called on detach")
	}
}

func TestRunLongRunningSession_SignalBeforeDelivery(t *testing.T) {
	// CreateSandbox succeeds, but signal arrives before CreateExec → cleanup.
	mock := &mockAgentClient{
		createSandboxFn: func(_ context.Context, _ *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sb-002",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
		getSandboxFn: func(_ context.Context, id string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         id,
					State:             agboxv1.SandboxState_SANDBOX_STATE_DELETED,
					LastEventSequence: 5,
				},
			}, nil
		},
		// CreateExec: make it fail as if the signal interrupts before exec.
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return nil, fmt.Errorf("context cancelled")
		},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error")
	}

	// Sandbox should be cleaned up since exec was never delivered.
	if !mock.deleteCalled {
		t.Fatal("expected DeleteSandbox to be called when exec delivery fails")
	}
}

func TestRunAgentSession_LongRunningNoGitConfirm(t *testing.T) {
	// Workspace without .git, long-running mode → no confirmation prompt.
	// If confirmation were triggered, it would fail (no stdin TTY in tests).
	tmpDir := realTempDir(t)

	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	mock := newReadyMock(eventCh)

	mock.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	mock.getExecFn = func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
		return execResponse("exec-1", "sb-001", agboxv1.ExecState_EXEC_STATE_FINISHED, 3, 0), nil
	}

	var stdout, stderr bytes.Buffer
	// Call runLongRunningSession directly; no .git in tmpDir, yet no prompt.
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:      agentModeLongRunning,
		agentType: "claude",
		command:   []string{"claude", "--dangerously-skip-permissions"},
		workspace: tmpDir,
	}, "claude", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify workspace was included in copies.
	req := mock.createSandboxReq
	if req == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
	copies := req.GetCreateSpec().GetCopies()
	if len(copies) != 1 || copies[0].GetSource() != tmpDir {
		t.Fatalf("expected workspace copy from %s, got %v", tmpDir, copies)
	}
}
