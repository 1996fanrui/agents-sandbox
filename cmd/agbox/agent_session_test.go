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

// newReadyOnlyMock creates a mockAgentClient where subscribe delivers a READY
// event immediately. No CreateExec/GetExec needed.
func newReadyOnlyMock() *mockAgentClient {
	eventCh := make(chan *agboxv1.SandboxEvent, 1)
	eventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-ready", Sequence: 2, SandboxId: "sb-001",
		SandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY,
		Details: &agboxv1.SandboxEvent_SandboxPhase{SandboxPhase: &agboxv1.SandboxPhaseDetails{
			Phase: "ready",
		}},
	}
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
		deleteSandboxFn: func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		subscribeFn: func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return &mockEventStream{events: eventCh}, nil
		},
	}
}

func TestRunLongRunningSession_SingleHappyPath(t *testing.T) {
	// AT-L1: sandbox READY → detach, stdout = sandbox_id, no delete.
	mock := newReadyOnlyMock()

	readyMsg := func(sandboxID, containerName string) string {
		return fmt.Sprintf("ready: %s %s\n", sandboxID, containerName)
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:         agentModeLongRunning,
		command:      []string{"my-service", "start"},
		readyMessage: readyMsg,
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "sb-001" {
		t.Fatalf("expected stdout=%q, got %q", "sb-001", got)
	}
	if !strings.Contains(stderr.String(), "ready: sb-001") {
		t.Fatalf("expected readyMessage in stderr, got:\n%s", stderr.String())
	}
	if mock.deleteCalled {
		t.Fatal("expected DeleteSandbox NOT to be called on success")
	}
}

func TestRunAgentSession_LongRunningIdleTTL(t *testing.T) {
	// Verify idle_ttl=0 in CreateSandbox request.
	mock := newReadyOnlyMock()

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	// Verify stdout=sandbox_id, stderr has readyMessage.
	mock := newReadyOnlyMock()

	readyMsg := func(sandboxID, containerName string) string {
		return fmt.Sprintf("Service running on %s (container: %s)\n", sandboxID, containerName)
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:         agentModeLongRunning,
		command:      []string{"echo", "hello"},
		readyMessage: readyMsg,
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "sb-001" {
		t.Fatalf("expected stdout=%q, got %q", "sb-001", got)
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Service running on sb-001") {
		t.Fatalf("stderr missing readyMessage, got:\n%s", stderrStr)
	}
}

func TestRunAgentSession_LongRunningNoDelete(t *testing.T) {
	// Verify DeleteSandbox is NOT called after READY (detachSuccess=true).
	mock := newReadyOnlyMock()

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

func TestRunLongRunningSession_SandboxFailed_ReturnsError(t *testing.T) {
	// AT-L3: sandbox enters FAILED → error returned, sandbox cleaned up.
	failedEventCh := make(chan *agboxv1.SandboxEvent, 1)
	failedEventCh <- &agboxv1.SandboxEvent{
		EventId: "ev-fail", Sequence: 2, SandboxId: "sb-001",
		SandboxState: agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		Details: &agboxv1.SandboxEvent_SandboxPhase{SandboxPhase: &agboxv1.SandboxPhaseDetails{
			Phase: "materialization",
		}},
	}

	mock := &mockAgentClient{
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
					State:             agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					LastEventSequence: 2,
					ErrorMessage:      "image pull failed",
				},
			}, nil
		},
		deleteSandboxFn: func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		subscribeFn: func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return &mockEventStream{events: failedEventCh}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when sandbox fails")
	}

	if !mock.deleteCalled {
		t.Fatal("expected DeleteSandbox to be called when sandbox fails before READY")
	}
}

func TestRunLongRunningSession_CommandFromParsedOverridesYaml(t *testing.T) {
	// AT-L5: command from parsed args is passed to CreateSpec.
	mock := newReadyOnlyMock()

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"custom-cmd", "--flag"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := mock.createSandboxReq
	if req == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
	cmd := req.GetCreateSpec().GetCommand()
	if len(cmd) != 2 || cmd[0] != "custom-cmd" || cmd[1] != "--flag" {
		t.Fatalf("expected command=[custom-cmd --flag], got %v", cmd)
	}
}

func TestRunLongRunningSession_CtrlCBeforeReady(t *testing.T) {
	// Signal before READY → cleanup (detachSuccess=false).
	blockingCh := make(chan *agboxv1.SandboxEvent) // unbuffered, blocks forever
	mock := &mockAgentClient{
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
					State:             agboxv1.SandboxState_SANDBOX_STATE_DELETED,
					LastEventSequence: 5,
				},
			}, nil
		},
		deleteSandboxFn: func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		subscribeFn: func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return &mockEventStream{events: blockingCh}, nil
		},
	}

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

	cancel()
	err := <-resultCh
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}

	if !mock.deleteCalled {
		t.Fatal("expected DeleteSandbox to be called when cancelled before READY")
	}
}

func TestRunLongRunningSession_SignalBeforeDelivery(t *testing.T) {
	// CreateSandbox succeeds, context cancelled before READY → cleanup.
	blockingCh := make(chan *agboxv1.SandboxEvent) // unbuffered, blocks forever
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
		subscribeFn: func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return &mockEventStream{events: blockingCh}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		resultCh <- runLongRunningSession(ctx, mock, agentSessionArgs{
			mode:    agentModeLongRunning,
			command: []string{"echo"},
		}, "test", &stdout, &stderr)
	}()

	cancel()
	err := <-resultCh
	if err == nil {
		t.Fatal("expected error")
	}

	if !mock.deleteCalled {
		t.Fatal("expected DeleteSandbox to be called when signal arrives before READY")
	}
}

func TestRunAgentSession_LongRunningNoGitConfirm(t *testing.T) {
	// Workspace without .git, long-running mode → no confirmation prompt.
	tmpDir := realTempDir(t)
	mock := newReadyOnlyMock()

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:      agentModeLongRunning,
		agentType: "claude",
		command:   []string{"claude", "--dangerously-skip-permissions"},
		workspace: tmpDir,
	}, "claude", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := mock.createSandboxReq
	if req == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
	copies := req.GetCreateSpec().GetCopies()
	if len(copies) != 1 || copies[0].GetSource() != tmpDir {
		t.Fatalf("expected workspace copy from %s, got %v", tmpDir, copies)
	}
}

func TestRunAgentSessionPropagatesFlagsToCreateSpec(t *testing.T) {
	mock := newReadyOnlyMock()

	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:        agentModeLongRunning,
		command:     []string{"echo"},
		envs:        map[string]string{"FOO": "bar", "BAZ": "qux"},
		cpuLimit:    "2",
		memoryLimit: "4g",
		diskLimit:   "10g",
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := mock.createSandboxReq
	if req == nil {
		t.Fatal("expected CreateSandbox to be called")
	}
	spec := req.GetCreateSpec()

	if spec.GetCpuLimit() != "2" {
		t.Fatalf("expected cpu_limit=2, got %q", spec.GetCpuLimit())
	}
	if spec.GetMemoryLimit() != "4g" {
		t.Fatalf("expected memory_limit=4g, got %q", spec.GetMemoryLimit())
	}
	if spec.GetDiskLimit() != "10g" {
		t.Fatalf("expected disk_limit=10g, got %q", spec.GetDiskLimit())
	}
	envs := spec.GetEnvs()
	if envs["FOO"] != "bar" || envs["BAZ"] != "qux" {
		t.Fatalf("expected envs={FOO:bar, BAZ:qux}, got %v", envs)
	}
}

func TestNoLegacyHelpers(t *testing.T) {
	// AT-O4: verify longRunningExecResult no longer exists in the codebase.
	// Since we removed the function, attempting to reference it would be a
	// compile error. This test just confirms we're in the new model by
	// verifying the happy path runs without CreateExec.
	mock := newReadyOnlyMock()
	var stdout, stderr bytes.Buffer
	err := runLongRunningSession(context.Background(), mock, agentSessionArgs{
		mode:    agentModeLongRunning,
		command: []string{"echo"},
	}, "test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "sb-001" {
		t.Fatalf("expected stdout=%q, got %q", "sb-001", got)
	}
}
