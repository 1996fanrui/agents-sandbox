package client

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConversions(t *testing.T) {
	t.Parallel()

	handle, err := toSandboxHandle(&agboxv1.SandboxHandle{
		SandboxId:       "sandbox-1",
		State:           agboxv1.SandboxState_SANDBOX_STATE_READY,
		LastEventCursor: "sandbox-1:1",
		RequiredServices: []*agboxv1.ServiceSpec{
			{
				Name:  "postgres",
				Image: "postgres:16",
				Environment: []*agboxv1.KeyValue{
					{Key: "POSTGRES_DB", Value: "agents"},
				},
				Healthcheck: &agboxv1.HealthcheckConfig{
					Test:     []string{"CMD-SHELL", "pg_isready -U postgres"},
					Interval: "5s",
					Retries:  3,
				},
				PostStartOnPrimary: []string{"python", "-c", "print('seeded')"},
			},
		},
		OptionalServices: []*agboxv1.ServiceSpec{
			{Name: "redis", Image: "redis:7"},
		},
		Labels: map[string]string{"team": "sdk"},
	})
	if err != nil {
		t.Fatalf("toSandboxHandle failed: %v", err)
	}
	if handle.State != SandboxStateReady {
		t.Fatalf("unexpected sandbox state: %v", handle.State)
	}
	if handle.Labels["team"] != "sdk" {
		t.Fatalf("unexpected labels: %#v", handle.Labels)
	}
	if len(handle.RequiredServices) != 1 || handle.RequiredServices[0].Name != "postgres" {
		t.Fatalf("unexpected required services: %#v", handle.RequiredServices)
	}

	running := toExecHandle(&agboxv1.ExecStatus{
		ExecId:    "exec-running",
		SandboxId: "sandbox-1",
		State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
		Command:   []string{"echo", "hello"},
		Cwd:       "/workspace",
		Stdout:    "partial",
		Stderr:    "warn",
		ExitCode:  0,
	})
	if running.ExitCode != nil {
		t.Fatalf("running exec should not expose exit code: %#v", running)
	}
	if running.Stdout == nil || *running.Stdout != "partial" {
		t.Fatalf("unexpected running stdout: %#v", running.Stdout)
	}

	finished := toExecHandle(&agboxv1.ExecStatus{
		ExecId:    "exec-finished",
		SandboxId: "sandbox-1",
		State:     agboxv1.ExecState_EXEC_STATE_FINISHED,
		Command:   []string{"echo", "hello"},
		Cwd:       "/workspace",
		Stdout:    "done",
		ExitCode:  7,
	})
	if finished.ExitCode == nil || *finished.ExitCode != 7 {
		t.Fatalf("unexpected finished exit code: %#v", finished.ExitCode)
	}

	event, err := toSandboxEvent(eventPB("sandbox-1", 11, "sandbox-1:11", agboxv1.EventType_EXEC_FINISHED))
	if err != nil {
		t.Fatalf("toSandboxEvent failed: %v", err)
	}
	if event.ExitCode == nil || *event.ExitCode != 0 {
		t.Fatalf("exec finished event should expose exit code: %#v", event.ExitCode)
	}

	_, err = toSandboxEvent(&agboxv1.SandboxEvent{
		EventId:   "event-missing",
		SandboxId: "sandbox-1",
		Cursor:    "sandbox-1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "missing occurred_at") {
		t.Fatalf("expected missing occurred_at error, got %v", err)
	}

	_, err = toSandboxHandle(&agboxv1.SandboxHandle{
		SandboxId:       "sandbox-1",
		LastEventCursor: "sandbox-2:1",
	})
	if err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("expected invalid cursor error, got %v", err)
	}
}

func TestSandboxLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("create_wait_paths", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if request.GetCreateSpec().GetImage() != "python:3.12-slim" {
				t.Fatalf("unexpected image: %q", request.GetCreateSpec().GetImage())
			}
			if request.GetSandboxId() != "sandbox-1" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			return &agboxv1.CreateSandboxResponse{SandboxId: "sandbox-1"}, nil
		}
		getCalls := 0
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			getCalls++
			state := agboxv1.SandboxState_SANDBOX_STATE_PENDING
			cursor := "sandbox-1:5"
			if getCalls > 1 {
				state = agboxv1.SandboxState_SANDBOX_STATE_READY
				cursor = "sandbox-1:6"
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           state,
					LastEventCursor: cursor,
				},
			}, nil
		}

		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
			if sandboxID != "sandbox-1" || fromCursor != "sandbox-1:5" || includeCurrentSnapshot {
				t.Fatalf("unexpected subscribe args: %q %q %v", sandboxID, fromCursor, includeCurrentSnapshot)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB("sandbox-1", 4, "sandbox-1:4", agboxv1.EventType_SANDBOX_READY),
				eventPB("sandbox-1", 5, "sandbox-1:5", agboxv1.EventType_SANDBOX_READY),
				eventPB("sandbox-1", 6, "sandbox-1:6", agboxv1.EventType_SANDBOX_READY),
			}, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		handle, err := client.CreateSandbox(context.Background(), "python:3.12-slim", WithSandboxID("sandbox-1"))
		if err != nil {
			t.Fatalf("CreateSandbox(wait=true) failed: %v", err)
		}
		if handle.State != SandboxStateReady {
			t.Fatalf("unexpected final state: %v", handle.State)
		}

		getCalls = 0
		handle, err = client.CreateSandbox(context.Background(), "python:3.12-slim", WithSandboxID("sandbox-1"), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox(wait=false) failed: %v", err)
		}
		if handle.State != SandboxStatePending {
			t.Fatalf("unexpected non-wait state: %v", handle.State)
		}
	})

	t.Run("resume_stop_delete_wait_paths", func(t *testing.T) {
		base := &fakeRPCClient{}
		mode := ""
		resumeCalls := 0
		stopCalls := 0
		deleteCalls := 0
		responses := map[string][]*agboxv1.SandboxHandle{
			"resume-false": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_PENDING, LastEventCursor: "sandbox-1:1"},
			},
			"resume-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_PENDING, LastEventCursor: "sandbox-1:2"},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventCursor: "sandbox-1:3"},
			},
			"stop-false": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventCursor: "sandbox-1:4"},
			},
			"stop-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventCursor: "sandbox-1:5"},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_STOPPED, LastEventCursor: "sandbox-1:6"},
			},
			"delete-false": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETING, LastEventCursor: "sandbox-1:7"},
			},
			"delete-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETING, LastEventCursor: "sandbox-1:8"},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETED, LastEventCursor: "sandbox-1:9"},
			},
		}
		base.resumeSandboxFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			if resumeCalls == 0 {
				mode = "resume-false"
			} else {
				mode = "resume-true"
			}
			resumeCalls++
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		base.stopSandboxFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			if stopCalls == 0 {
				mode = "stop-false"
			} else {
				mode = "stop-true"
			}
			stopCalls++
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		base.deleteSandboxFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			if deleteCalls == 0 {
				mode = "delete-false"
			} else {
				mode = "delete-true"
			}
			deleteCalls++
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		base.getSandboxFn = func(_ context.Context, _ string) (*agboxv1.GetSandboxResponse, error) {
			queue := responses[mode]
			current := queue[0]
			responses[mode] = queue[1:]
			return &agboxv1.GetSandboxResponse{Sandbox: current}, nil
		}

		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, _ bool) (rawclient.SandboxEventStream, error) {
			var events []*agboxv1.SandboxEvent
			switch mode {
			case "resume-true":
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 2, "sandbox-1:2", agboxv1.EventType_SANDBOX_READY),
					eventPB(sandboxID, 3, "sandbox-1:3", agboxv1.EventType_SANDBOX_READY),
				}
			case "stop-true":
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 5, "sandbox-1:5", agboxv1.EventType_SANDBOX_STOPPED),
					eventPB(sandboxID, 6, "sandbox-1:6", agboxv1.EventType_SANDBOX_STOPPED),
				}
			default:
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 8, "sandbox-1:8", agboxv1.EventType_SANDBOX_DELETED),
					eventPB(sandboxID, 9, "sandbox-1:9", agboxv1.EventType_SANDBOX_DELETED),
				}
			}
			return streamFromEvents(events, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		resumePending, err := client.ResumeSandbox(context.Background(), "sandbox-1", WithWait(false))
		if err != nil || resumePending.State != SandboxStatePending {
			t.Fatalf("ResumeSandbox(wait=false) = %#v, %v", resumePending, err)
		}
		resumeReady, err := client.ResumeSandbox(context.Background(), "sandbox-1")
		if err != nil || resumeReady.State != SandboxStateReady {
			t.Fatalf("ResumeSandbox(wait=true) = %#v, %v", resumeReady, err)
		}
		stopReady, err := client.StopSandbox(context.Background(), "sandbox-1", WithWait(false))
		if err != nil || stopReady.State != SandboxStateReady {
			t.Fatalf("StopSandbox(wait=false) = %#v, %v", stopReady, err)
		}
		stopStopped, err := client.StopSandbox(context.Background(), "sandbox-1")
		if err != nil || stopStopped.State != SandboxStateStopped {
			t.Fatalf("StopSandbox(wait=true) = %#v, %v", stopStopped, err)
		}
		deleteDeleting, err := client.DeleteSandbox(context.Background(), "sandbox-1", WithWait(false))
		if err != nil || deleteDeleting.State != SandboxStateDeleting {
			t.Fatalf("DeleteSandbox(wait=false) = %#v, %v", deleteDeleting, err)
		}
		deleteDeleted, err := client.DeleteSandbox(context.Background(), "sandbox-1")
		if err != nil || deleteDeleted.State != SandboxStateDeleted {
			t.Fatalf("DeleteSandbox(wait=true) = %#v, %v", deleteDeleted, err)
		}
	})

	t.Run("delete_sandboxes_waits_for_deleted", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.deleteSandboxesFn = func(_ context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
			if request.GetLabelSelector()["team"] != "sdk" {
				t.Fatalf("unexpected label selector: %#v", request.GetLabelSelector())
			}
			return &agboxv1.DeleteSandboxesResponse{
				DeletedSandboxIds: []string{"sandbox-1", "sandbox-2"},
				DeletedCount:      2,
			}, nil
		}
		var mu sync.Mutex
		sandboxReads := map[string]int{}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			mu.Lock()
			sandboxReads[sandboxID]++
			readCount := sandboxReads[sandboxID]
			mu.Unlock()
			state := agboxv1.SandboxState_SANDBOX_STATE_DELETING
			cursor := sandboxID + ":1"
			if readCount >= 2 {
				state = agboxv1.SandboxState_SANDBOX_STATE_DELETED
				cursor = sandboxID + ":2"
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           state,
					LastEventCursor: cursor,
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, _ bool) (rawclient.SandboxEventStream, error) {
			if fromCursor != sandboxID+":1" {
				t.Fatalf("unexpected from_cursor: %q", fromCursor)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB(sandboxID, 2, sandboxID+":2", agboxv1.EventType_SANDBOX_DELETED),
			}, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		result, err := client.DeleteSandboxes(context.Background(), map[string]string{"team": "sdk"})
		if err != nil {
			t.Fatalf("DeleteSandboxes(wait=true) failed: %v", err)
		}
		if result.DeletedCount != 2 || len(result.DeletedSandboxIDs) != 2 {
			t.Fatalf("unexpected delete result: %#v", result)
		}
	})

	t.Run("delete_sandboxes_waits_concurrently", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.deleteSandboxesFn = func(_ context.Context, _ *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
			return &agboxv1.DeleteSandboxesResponse{
				DeletedSandboxIds: []string{"sandbox-1", "sandbox-2"},
				DeletedCount:      2,
			}, nil
		}

		var mu sync.Mutex
		sandboxReads := map[string]int{}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			mu.Lock()
			sandboxReads[sandboxID]++
			readCount := sandboxReads[sandboxID]
			mu.Unlock()

			state := agboxv1.SandboxState_SANDBOX_STATE_DELETING
			cursor := sandboxID + ":1"
			if readCount >= 2 {
				state = agboxv1.SandboxState_SANDBOX_STATE_DELETED
				cursor = sandboxID + ":2"
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           state,
					LastEventCursor: cursor,
				},
			}, nil
		}

		ready := make(chan string, 2)
		release := make(chan struct{})
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, _ bool) (rawclient.SandboxEventStream, error) {
			ready <- sandboxID
			delivered := false
			return &fakeStream{
				recvFn: func() (*agboxv1.SandboxEvent, error) {
					<-release
					if delivered {
						return nil, io.EOF
					}
					delivered = true
					return eventPB(sandboxID, 2, sandboxID+":2", agboxv1.EventType_SANDBOX_DELETED), nil
				},
			}, nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		done := make(chan error, 1)
		go func() {
			_, err := client.DeleteSandboxes(context.Background(), map[string]string{"team": "sdk"})
			done <- err
		}()

		first := <-ready
		second := <-ready
		if first == second {
			t.Fatalf("expected concurrent waits for different sandboxes, got %q twice", first)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("DeleteSandboxes concurrent wait failed: %v", err)
		}
	})
}

func TestExecOperations(t *testing.T) {
	t.Parallel()

	t.Run("create_exec_default_wait_false", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createExecFn = func(_ context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			if request.GetCwd() != "/workspace" {
				t.Fatalf("unexpected default cwd: %q", request.GetCwd())
			}
			if request.GetExecId() != "" {
				t.Fatalf("unexpected default exec id: %q", request.GetExecId())
			}
			if len(request.GetEnvOverrides()) != 0 {
				t.Fatalf("unexpected env overrides: %#v", request.GetEnvOverrides())
			}
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    execID,
					SandboxId: "sandbox-1",
					State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:   []string{"echo", "hello"},
					Cwd:       "/workspace",
				},
			}, nil
		}

		client := newTestClient(base, nil)
		handle, err := client.CreateExec(context.Background(), "sandbox-1", []string{"echo", "hello"})
		if err != nil {
			t.Fatalf("CreateExec(wait=false) failed: %v", err)
		}
		if handle.State != ExecStateRunning {
			t.Fatalf("unexpected exec state: %v", handle.State)
		}
	})

	t.Run("create_exec_wait_and_run", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createExecFn = func(_ context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		getExecCalls := 0
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			getExecCalls++
			state := agboxv1.ExecState_EXEC_STATE_RUNNING
			stdout := ""
			if getExecCalls >= 3 {
				state = agboxv1.ExecState_EXEC_STATE_FINISHED
				stdout = "hello"
			}
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    execID,
					SandboxId: "sandbox-1",
					State:     state,
					Command:   []string{"echo", "hello"},
					Cwd:       "/workspace",
					Stdout:    stdout,
					ExitCode:  0,
				},
			}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventCursor: "sandbox-1:10",
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, _ bool) (rawclient.SandboxEventStream, error) {
			if fromCursor != "sandbox-1:10" {
				t.Fatalf("unexpected from_cursor: %q", fromCursor)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB(sandboxID, 10, "sandbox-1:10", agboxv1.EventType_EXEC_FINISHED),
				eventPB(sandboxID, 11, "sandbox-1:11", agboxv1.EventType_EXEC_FINISHED),
			}, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		handle, err := client.CreateExec(context.Background(), "sandbox-1", []string{"echo", "hello"}, WithWait(true))
		if err != nil {
			t.Fatalf("CreateExec(wait=true) failed: %v", err)
		}
		if handle.State != ExecStateFinished {
			t.Fatalf("unexpected exec state: %v", handle.State)
		}

		getExecCalls = 0
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    execID,
					SandboxId: "sandbox-1",
					State:     agboxv1.ExecState_EXEC_STATE_FINISHED,
					Command:   []string{"echo", "hello"},
					Cwd:       "/workspace",
					Stdout:    "hello",
					ExitCode:  0,
				},
			}, nil
		}
		runHandle, err := client.Run(context.Background(), "sandbox-1", []string{"echo", "hello"})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if runHandle.Stdout == nil || *runHandle.Stdout != "hello" {
			t.Fatalf("unexpected run stdout: %#v", runHandle.Stdout)
		}
	})

	t.Run("cancel_exec_paths", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.cancelExecFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			return nil, &rawclient.SandboxInvalidStateError{
				SandboxClientError: &rawclient.SandboxClientError{},
			}
		}
		client := newTestClient(base, nil)
		_, err := client.CancelExec(context.Background(), "exec-1")
		var notRunning *rawclient.ExecNotRunningError
		if !errors.As(err, &notRunning) {
			t.Fatalf("expected ExecNotRunningError, got %T", err)
		}
		if notRunning.ExecID != "exec-1" {
			t.Fatalf("unexpected exec id: %q", notRunning.ExecID)
		}
		if err.Error() != "Exec exec-1 is not running." {
			t.Fatalf("unexpected canonical message: %q", err.Error())
		}

		mode := "cancel-false"
		trueExecCalls := 0
		base.cancelExecFn = func(_ context.Context, _ string) (*agboxv1.AcceptedResponse, error) {
			if mode == "cancel-false" {
				mode = "cancel-true"
			}
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			state := agboxv1.ExecState_EXEC_STATE_RUNNING
			if mode == "cancel-true" {
				trueExecCalls++
				if trueExecCalls >= 3 {
					state = agboxv1.ExecState_EXEC_STATE_FINISHED
				}
			}
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    execID,
					SandboxId: "sandbox-1",
					State:     state,
					Command:   []string{"echo", "hello"},
					Cwd:       "/workspace",
					ExitCode:  0,
				},
			}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventCursor: "sandbox-1:12",
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, _ bool) (rawclient.SandboxEventStream, error) {
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB(sandboxID, 12, "sandbox-1:12", agboxv1.EventType_EXEC_FINISHED),
			}, nil), nil
		}
		client = newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		running, err := client.CancelExec(context.Background(), "exec-1", WithWait(false))
		if err != nil || running.State != ExecStateRunning {
			t.Fatalf("CancelExec(wait=false) = %#v, %v", running, err)
		}
		finished, err := client.CancelExec(context.Background(), "exec-1")
		if err != nil || finished.State != ExecStateFinished {
			t.Fatalf("CancelExec(wait=true) = %#v, %v", finished, err)
		}
	})
}

func TestWaitErrorSemantics(t *testing.T) {
	t.Parallel()

	t.Run("sandbox_failed_returns_base_sdk_error", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, _ *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{SandboxId: "sandbox-1"}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       "sandbox-1",
					State:           agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					LastEventCursor: "sandbox-1:1",
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(), "python:3.12-slim")
		var clientErr *rawclient.SandboxClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("expected SandboxClientError, got %T", err)
		}
		if !strings.Contains(err.Error(), "FAILED") {
			t.Fatalf("unexpected failed sandbox message: %q", err.Error())
		}
	})

	t.Run("sandbox_stream_end_returns_base_sdk_error", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, _ *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{SandboxId: "sandbox-1"}, nil
		}
		getCalls := 0
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			getCalls++
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventCursor: "sandbox-1:1",
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ string, _ bool) (rawclient.SandboxEventStream, error) {
			return streamFromEvents(nil, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		_, err := client.CreateSandbox(context.Background(), "python:3.12-slim")
		var clientErr *rawclient.SandboxClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("expected SandboxClientError, got %T", err)
		}
		if !strings.Contains(err.Error(), "ended before sandbox") {
			t.Fatalf("unexpected stream-end message: %q", err.Error())
		}
	})

	t.Run("exec_stream_end_returns_base_sdk_error", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    execID,
					SandboxId: "sandbox-1",
					State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:   []string{"echo", "hello"},
					Cwd:       "/workspace",
				},
			}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:       sandboxID,
					State:           agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventCursor: "sandbox-1:10",
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ string, _ bool) (rawclient.SandboxEventStream, error) {
			return streamFromEvents(nil, nil), nil
		}

		client := newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		_, err := client.CreateExec(context.Background(), "sandbox-1", []string{"echo", "hello"}, WithWait(true))
		var clientErr *rawclient.SandboxClientError
		if !errors.As(err, &clientErr) {
			t.Fatalf("expected SandboxClientError, got %T", err)
		}
		if !strings.Contains(err.Error(), "event stream ended before exec") {
			t.Fatalf("unexpected exec stream-end message: %q", err.Error())
		}
	})
}

func TestSubscribeChannel(t *testing.T) {
	t.Parallel()

	t.Run("events_and_defaults", func(t *testing.T) {
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromCursor string, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
			if sandboxID != "sandbox-1" || fromCursor != "0" || includeCurrentSnapshot {
				t.Fatalf("unexpected subscribe args: %q %q %v", sandboxID, fromCursor, includeCurrentSnapshot)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB("sandbox-1", 1, "sandbox-1:1", agboxv1.EventType_SANDBOX_SERVICE_READY),
				eventPB("sandbox-1", 2, "sandbox-1:2", agboxv1.EventType_EXEC_FINISHED),
			}, nil), nil
		}
		client := newTestClient(&fakeRPCClient{}, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		var got []SandboxEventType
		for item := range client.SubscribeSandboxEvents(context.Background(), "sandbox-1") {
			if item.Err != nil {
				t.Fatalf("unexpected subscription error: %v", item.Err)
			}
			got = append(got, item.Event.EventType)
		}
		if len(got) != 2 || got[0] != SandboxEventTypeSandboxServiceReady || got[1] != SandboxEventTypeExecFinished {
			t.Fatalf("unexpected events: %#v", got)
		}
	})

	t.Run("invalid_cursor_reports_error", func(t *testing.T) {
		client := newTestClient(&fakeRPCClient{}, nil)
		ch := client.SubscribeSandboxEvents(context.Background(), "sandbox-1", WithFromCursor("sandbox-2:1"))
		item, ok := <-ch
		if !ok || item.Err == nil {
			t.Fatalf("expected immediate cursor error, got %#v %v", item, ok)
		}
	})

	t.Run("stream_error_is_forwarded", func(t *testing.T) {
		wantErr := errors.New("stream failed")
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ string, _ bool) (rawclient.SandboxEventStream, error) {
			return streamFromEvents(nil, wantErr), nil
		}
		client := newTestClient(&fakeRPCClient{}, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		item := <-client.SubscribeSandboxEvents(context.Background(), "sandbox-1")
		if !errors.Is(item.Err, wantErr) {
			t.Fatalf("expected forwarded error, got %#v", item.Err)
		}
	})

	t.Run("context_cancel_closes_channel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(ctx context.Context, _ string, _ string, _ bool) (rawclient.SandboxEventStream, error) {
			return blockingStream(ctx), nil
		}
		client := newTestClient(&fakeRPCClient{}, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		ch := client.SubscribeSandboxEvents(ctx, "sandbox-1")
		cancel()
		for range ch {
		}
	})
}

type fakeRPCClient struct {
	pingFn                   func(context.Context) (*agboxv1.PingResponse, error)
	createSandboxFn          func(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	getSandboxFn             func(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	listSandboxesFn          func(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	resumeSandboxFn          func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	stopSandboxFn            func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	deleteSandboxFn          func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	deleteSandboxesFn        func(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
	subscribeSandboxEventsFn func(context.Context, string, string, bool) (rawclient.SandboxEventStream, error)
	createExecFn             func(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	cancelExecFn             func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	getExecFn                func(context.Context, string) (*agboxv1.GetExecResponse, error)
	listActiveExecsFn        func(context.Context, string) (*agboxv1.ListActiveExecsResponse, error)
	closeFn                  func() error
}

func (f *fakeRPCClient) Ping(ctx context.Context) (*agboxv1.PingResponse, error) {
	if f.pingFn != nil {
		return f.pingFn(ctx)
	}
	return &agboxv1.PingResponse{}, nil
}

func (f *fakeRPCClient) CreateSandbox(ctx context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	if f.createSandboxFn != nil {
		return f.createSandboxFn(ctx, request)
	}
	return &agboxv1.CreateSandboxResponse{}, nil
}

func (f *fakeRPCClient) GetSandbox(ctx context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
	if f.getSandboxFn != nil {
		return f.getSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.GetSandboxResponse{}, nil
}

func (f *fakeRPCClient) ListSandboxes(ctx context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	if f.listSandboxesFn != nil {
		return f.listSandboxesFn(ctx, request)
	}
	return &agboxv1.ListSandboxesResponse{}, nil
}

func (f *fakeRPCClient) ResumeSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.resumeSandboxFn != nil {
		return f.resumeSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) StopSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.stopSandboxFn != nil {
		return f.stopSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) DeleteSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.deleteSandboxFn != nil {
		return f.deleteSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) DeleteSandboxes(ctx context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	if f.deleteSandboxesFn != nil {
		return f.deleteSandboxesFn(ctx, request)
	}
	return &agboxv1.DeleteSandboxesResponse{}, nil
}

func (f *fakeRPCClient) SubscribeSandboxEvents(ctx context.Context, sandboxID string, fromCursor string, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
	if f.subscribeSandboxEventsFn != nil {
		return f.subscribeSandboxEventsFn(ctx, sandboxID, fromCursor, includeCurrentSnapshot)
	}
	return streamFromEvents(nil, nil), nil
}

func (f *fakeRPCClient) CreateExec(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	if f.createExecFn != nil {
		return f.createExecFn(ctx, request)
	}
	return &agboxv1.CreateExecResponse{}, nil
}

func (f *fakeRPCClient) CancelExec(ctx context.Context, execID string) (*agboxv1.AcceptedResponse, error) {
	if f.cancelExecFn != nil {
		return f.cancelExecFn(ctx, execID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) GetExec(ctx context.Context, execID string) (*agboxv1.GetExecResponse, error) {
	if f.getExecFn != nil {
		return f.getExecFn(ctx, execID)
	}
	return &agboxv1.GetExecResponse{}, nil
}

func (f *fakeRPCClient) ListActiveExecs(ctx context.Context, sandboxID string) (*agboxv1.ListActiveExecsResponse, error) {
	if f.listActiveExecsFn != nil {
		return f.listActiveExecsFn(ctx, sandboxID)
	}
	return &agboxv1.ListActiveExecsResponse{}, nil
}

func (f *fakeRPCClient) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

type fakeStream struct {
	recvFn  func() (*agboxv1.SandboxEvent, error)
	closeFn func() error
	closeMu sync.Mutex
	closed  bool
}

func (s *fakeStream) Recv() (*agboxv1.SandboxEvent, error) {
	if s.recvFn == nil {
		return nil, io.EOF
	}
	return s.recvFn()
}

func (s *fakeStream) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.closed = true
	if s.closeFn != nil {
		return s.closeFn()
	}
	return nil
}

func newTestClient(base rpcClient, streamFactory rawClientFactory) *Client {
	if streamFactory == nil {
		streamFactory = func(time.Duration) (rpcClient, error) {
			return &fakeRPCClient{}, nil
		}
	}
	return &Client{
		rpcClient:        base,
		newStreamClient:  streamFactory,
		streamTimeout:    time.Second,
		operationTimeout: 50 * time.Millisecond,
		execPollInterval: time.Millisecond,
	}
}

func streamFromEvents(events []*agboxv1.SandboxEvent, finalErr error) rawclient.SandboxEventStream {
	index := 0
	return &fakeStream{
		recvFn: func() (*agboxv1.SandboxEvent, error) {
			if index < len(events) {
				event := events[index]
				index++
				return event, nil
			}
			if finalErr == nil {
				return nil, io.EOF
			}
			return nil, finalErr
		},
	}
}

func blockingStream(ctx context.Context) rawclient.SandboxEventStream {
	return &fakeStream{
		recvFn: func() (*agboxv1.SandboxEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

func eventPB(sandboxID string, sequence uint64, cursor string, eventType agboxv1.EventType) *agboxv1.SandboxEvent {
	return &agboxv1.SandboxEvent{
		EventId:    "event",
		Sequence:   sequence,
		Cursor:     cursor,
		SandboxId:  sandboxID,
		EventType:  eventType,
		OccurredAt: timestamppb.New(time.Now()),
		ExecState:  agboxv1.ExecState_EXEC_STATE_FINISHED,
	}
}
