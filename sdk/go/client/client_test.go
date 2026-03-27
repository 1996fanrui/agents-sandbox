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
)

func TestConversions(t *testing.T) {
	t.Parallel()

	handle, err := toSandboxHandle(&agboxv1.SandboxHandle{
		SandboxId:         "sandbox-1",
		State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
		LastEventSequence: 1,
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
		ExecId:            "exec-running",
		SandboxId:         "sandbox-1",
		State:             agboxv1.ExecState_EXEC_STATE_RUNNING,
		Command:           []string{"echo", "hello"},
		Cwd:               "/workspace",
		ExitCode:          0,
		LastEventSequence: 3,
	})
	if running.ExitCode != nil {
		t.Fatalf("running exec should not expose exit code: %#v", running)
	}
	if running.LastEventSequence != 3 {
		t.Fatalf("unexpected running last event sequence: %#v", running.LastEventSequence)
	}

	finished := toExecHandle(&agboxv1.ExecStatus{
		ExecId:            "exec-finished",
		SandboxId:         "sandbox-1",
		State:             agboxv1.ExecState_EXEC_STATE_FINISHED,
		Command:           []string{"echo", "hello"},
		Cwd:               "/workspace",
		ExitCode:          7,
		LastEventSequence: 7,
	})
	if finished.ExitCode == nil || *finished.ExitCode != 7 {
		t.Fatalf("unexpected finished exit code: %#v", finished.ExitCode)
	}
	if finished.LastEventSequence != 7 {
		t.Fatalf("unexpected finished last event sequence: %#v", finished.LastEventSequence)
	}

	event, err := toSandboxEvent(eventPB("sandbox-1", 11, "", agboxv1.EventType_EXEC_FINISHED))
	if err != nil {
		t.Fatalf("toSandboxEvent failed: %v", err)
	}
	if event.ExitCode == nil || *event.ExitCode != 0 {
		t.Fatalf("exec finished event should expose exit code: %#v", event.ExitCode)
	}

	_, err = toSandboxEvent(&agboxv1.SandboxEvent{
		EventId:   "event-missing",
		SandboxId: "sandbox-1",
	})
	if err == nil || !strings.Contains(err.Error(), "missing occurred_at") {
		t.Fatalf("expected missing occurred_at error, got %v", err)
	}

	_, err = toExecSnapshot(&agboxv1.GetExecResponse{
		Exec: &agboxv1.ExecStatus{
			ExecId:    "exec-1",
			SandboxId: "sandbox-1",
			State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing last_event_sequence") {
		t.Fatalf("expected missing last_event_sequence error, got %v", err)
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
			sequence := uint64(5)
			if getCalls > 1 {
				state = agboxv1.SandboxState_SANDBOX_STATE_READY
				sequence = 6
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         sandboxID,
					State:             state,
					LastEventSequence: sequence,
				},
			}, nil
		}

		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
			if sandboxID != "sandbox-1" || fromSequence != 5 || includeCurrentSnapshot {
				t.Fatalf("unexpected subscribe args: %q %d %v", sandboxID, fromSequence, includeCurrentSnapshot)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB("sandbox-1", 4, "", agboxv1.EventType_SANDBOX_READY),
				eventPB("sandbox-1", 5, "", agboxv1.EventType_SANDBOX_READY),
				eventPB("sandbox-1", 6, "", agboxv1.EventType_SANDBOX_READY),
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
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_PENDING, LastEventSequence: 1},
			},
			"resume-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_PENDING, LastEventSequence: 2},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 3},
			},
			"stop-false": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 4},
			},
			"stop-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 5},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_STOPPED, LastEventSequence: 6},
			},
			"delete-false": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETING, LastEventSequence: 7},
			},
			"delete-true": {
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETING, LastEventSequence: 8},
				{SandboxId: "sandbox-1", State: agboxv1.SandboxState_SANDBOX_STATE_DELETED, LastEventSequence: 9},
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
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, _ bool) (rawclient.SandboxEventStream, error) {
			var events []*agboxv1.SandboxEvent
			switch mode {
			case "resume-true":
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 2, "", agboxv1.EventType_SANDBOX_READY),
					eventPB(sandboxID, 3, "", agboxv1.EventType_SANDBOX_READY),
				}
			case "stop-true":
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 5, "", agboxv1.EventType_SANDBOX_STOPPED),
					eventPB(sandboxID, 6, "", agboxv1.EventType_SANDBOX_STOPPED),
				}
			default:
				events = []*agboxv1.SandboxEvent{
					eventPB(sandboxID, 8, "", agboxv1.EventType_SANDBOX_DELETED),
					eventPB(sandboxID, 9, "", agboxv1.EventType_SANDBOX_DELETED),
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
			sequence := uint64(1)
			if readCount >= 2 {
				state = agboxv1.SandboxState_SANDBOX_STATE_DELETED
				sequence = 2
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         sandboxID,
					State:             state,
					LastEventSequence: sequence,
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, _ bool) (rawclient.SandboxEventStream, error) {
			if fromSequence != 1 {
				t.Fatalf("unexpected from_sequence: %d", fromSequence)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB(sandboxID, 2, "", agboxv1.EventType_SANDBOX_DELETED),
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
			sequence := uint64(1)
			if readCount >= 2 {
				state = agboxv1.SandboxState_SANDBOX_STATE_DELETED
				sequence = 2
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         sandboxID,
					State:             state,
					LastEventSequence: sequence,
				},
			}, nil
		}

		ready := make(chan string, 2)
		release := make(chan struct{})
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, _ bool) (rawclient.SandboxEventStream, error) {
			ready <- sandboxID
			delivered := false
			return &fakeStream{
				recvFn: func() (*agboxv1.SandboxEvent, error) {
					<-release
					if delivered {
						return nil, io.EOF
					}
					delivered = true
					return eventPB(sandboxID, 2, "", agboxv1.EventType_SANDBOX_DELETED), nil
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
					ExecId:            execID,
					SandboxId:         "sandbox-1",
					State:             agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:           []string{"echo", "hello"},
					Cwd:               "/workspace",
					LastEventSequence: 1,
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
		if handle.LastEventSequence != 1 {
			t.Fatalf("unexpected exec sequence: %d", handle.LastEventSequence)
		}
	})

	t.Run("create_exec_wait_and_run", func(t *testing.T) {
		base := &fakeRPCClient{}
		base.createExecFn = func(_ context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			t.Fatalf("waitForExecTerminal must not call GetSandbox, got %q", sandboxID)
			return nil, nil
		}
		getExecCalls := 0
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			getExecCalls++
			state := agboxv1.ExecState_EXEC_STATE_RUNNING
			sequence := uint64(10)
			if getExecCalls >= 2 {
				state = agboxv1.ExecState_EXEC_STATE_FINISHED
				sequence = 12
			}
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            execID,
					SandboxId:         "sandbox-1",
					State:             state,
					Command:           []string{"echo", "hello"},
					Cwd:               "/workspace",
					ExitCode:          0,
					LastEventSequence: sequence,
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, _ bool) (rawclient.SandboxEventStream, error) {
			if fromSequence != 10 {
				t.Fatalf("unexpected from_sequence: %d", fromSequence)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				withExecEvent(eventPB(sandboxID, 11, "", agboxv1.EventType_EXEC_FINISHED), "exec-2"),
				withExecEvent(eventPB(sandboxID, 12, "", agboxv1.EventType_EXEC_FINISHED), "exec-1"),
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
		if handle.LastEventSequence != 12 {
			t.Fatalf("unexpected exec sequence: %d", handle.LastEventSequence)
		}

		getExecCalls = 0
		base.getExecFn = func(_ context.Context, execID string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            execID,
					SandboxId:         "sandbox-1",
					State:             agboxv1.ExecState_EXEC_STATE_FINISHED,
					Command:           []string{"echo", "hello"},
					Cwd:               "/workspace",
					ExitCode:          0,
					LastEventSequence: 12,
				},
			}, nil
		}
		runHandle, err := client.Run(context.Background(), "sandbox-1", []string{"echo", "hello"})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if runHandle.LastEventSequence != 12 {
			t.Fatalf("unexpected run sequence: %d", runHandle.LastEventSequence)
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
			sequence := uint64(12)
			if mode == "cancel-true" {
				trueExecCalls++
				if trueExecCalls >= 2 {
					state = agboxv1.ExecState_EXEC_STATE_CANCELLED
					sequence = 13
				}
			}
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            execID,
					SandboxId:         "sandbox-1",
					State:             state,
					Command:           []string{"echo", "hello"},
					Cwd:               "/workspace",
					ExitCode:          0,
					LastEventSequence: sequence,
				},
			}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			t.Fatalf("waitForExecTerminal must not call GetSandbox, got %q", sandboxID)
			return nil, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, _ bool) (rawclient.SandboxEventStream, error) {
			if fromSequence != 12 {
				t.Fatalf("unexpected from_sequence: %d", fromSequence)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				withExecEvent(eventPB(sandboxID, 12, "", agboxv1.EventType_EXEC_CANCELLED), "exec-1"),
				withExecEvent(eventPB(sandboxID, 13, "", agboxv1.EventType_EXEC_CANCELLED), "exec-1"),
			}, nil), nil
		}
		client = newTestClient(base, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		running, err := client.CancelExec(context.Background(), "exec-1", WithWait(false))
		if err != nil || running.State != ExecStateRunning {
			t.Fatalf("CancelExec(wait=false) = %#v, %v", running, err)
		}
		finished, err := client.CancelExec(context.Background(), "exec-1")
		if err != nil || finished.State != ExecStateCancelled {
			t.Fatalf("CancelExec(wait=true) = %#v, %v", finished, err)
		}
	})

	t.Run("exec_wait_requires_snapshot_sequence", func(t *testing.T) {
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

		client := newTestClient(base, nil)
		_, err := client.CreateExec(context.Background(), "sandbox-1", []string{"echo", "hello"}, WithWait(true))
		if err == nil || !strings.Contains(err.Error(), "missing last_event_sequence") {
			t.Fatalf("expected missing sequence error, got %v", err)
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
					SandboxId:         "sandbox-1",
					State:             agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					LastEventSequence: 1,
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
					SandboxId:         sandboxID,
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
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
					ExecId:            execID,
					SandboxId:         "sandbox-1",
					State:             agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:           []string{"echo", "hello"},
					Cwd:               "/workspace",
					LastEventSequence: 10,
				},
			}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			t.Fatalf("waitForExecTerminal must not call GetSandbox, got %q", sandboxID)
			return nil, nil
		}
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
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
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, sandboxID string, fromSequence uint64, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
			if sandboxID != "sandbox-1" || fromSequence != 0 || includeCurrentSnapshot {
				t.Fatalf("unexpected subscribe args: %q %d %v", sandboxID, fromSequence, includeCurrentSnapshot)
			}
			return streamFromEvents([]*agboxv1.SandboxEvent{
				eventPB("sandbox-1", 1, "", agboxv1.EventType_SANDBOX_SERVICE_READY),
				eventPB("sandbox-1", 2, "", agboxv1.EventType_EXEC_FINISHED),
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

	t.Run("stream_error_is_forwarded", func(t *testing.T) {
		wantErr := errors.New("stream failed")
		streamClient := &fakeRPCClient{}
		streamClient.subscribeSandboxEventsFn = func(_ context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
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
		streamClient.subscribeSandboxEventsFn = func(ctx context.Context, _ string, _ uint64, _ bool) (rawclient.SandboxEventStream, error) {
			return blockingStream(ctx), nil
		}
		client := newTestClient(&fakeRPCClient{}, func(time.Duration) (rpcClient, error) { return streamClient, nil })
		ch := client.SubscribeSandboxEvents(ctx, "sandbox-1")
		cancel()
		for range ch {
		}
	})
}
