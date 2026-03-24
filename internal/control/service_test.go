package control

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/proto/agboxv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestSandboxLifecycleAndExecStream(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ReplayLimit:     16,
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-1",
		},
		CreateSpec: &agboxv1.CreateSpec{
			Dependencies: []*agboxv1.DependencySpec{
				{DependencyName: "db", Image: "postgres:16"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		return len(items) >= 4 && items[len(items)-1].GetEventType() == agboxv1.EventType_SANDBOX_READY
	})
	wantLifecycle := []agboxv1.EventType{
		agboxv1.EventType_SANDBOX_ACCEPTED,
		agboxv1.EventType_SANDBOX_PREPARING,
		agboxv1.EventType_SANDBOX_DEPENDENCY_READY,
		agboxv1.EventType_SANDBOX_READY,
	}
	for index, eventType := range wantLifecycle {
		if events[index].GetEventType() != eventType {
			t.Fatalf("unexpected lifecycle event at %d: got %s want %s", index, events[index].GetEventType(), eventType)
		}
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo", "hello"},
		Cwd:       "/workspace",
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.StartExec(context.Background(), &agboxv1.StartExecRequest{ExecId: execResp.GetExecId()}); err != nil {
		t.Fatalf("StartExec failed: %v", err)
	}

	events = collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_EXEC_FINISHED {
				return true
			}
		}
		return false
	})
	var createEvent *agboxv1.SandboxEvent
	var startEvent *agboxv1.SandboxEvent
	last := events[len(events)-1]
	for _, event := range events {
		switch event.GetEventType() {
		case agboxv1.EventType_EXEC_CREATED:
			createEvent = event
		case agboxv1.EventType_EXEC_STARTED:
			startEvent = event
		}
	}
	if createEvent == nil || createEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN || createEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_CREATE_RUN_EXEC {
		t.Fatalf("unexpected exec create action metadata: %#v", createEvent)
	}
	if startEvent == nil || startEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN || startEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_START_RUN_EXEC {
		t.Fatalf("unexpected exec start action metadata: %#v", startEvent)
	}
	if last.GetEventType() != agboxv1.EventType_EXEC_FINISHED || last.GetExecId() != execResp.GetExecId() || last.GetExitCode() != 0 {
		t.Fatalf("unexpected exec terminal event: %#v", last)
	}
	if last.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN || last.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_START_RUN_EXEC {
		t.Fatalf("unexpected exec finish action metadata: %#v", last)
	}
}

func TestConfiguredArtifactOutputPathIsCreatedForExecs(t *testing.T) {
	artifactRoot := t.TempDir()
	client := newBufconnClient(t, ServiceConfig{
		ReplayLimit:            16,
		TransitionDelay:        5 * time.Millisecond,
		PollInterval:           2 * time.Millisecond,
		ArtifactOutputRoot:     artifactRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.jsonl",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "consumer",
			OwnerType: "workspace",
			OwnerId:   "workspace-1",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.StartExec(context.Background(), &agboxv1.StartExecRequest{ExecId: execResp.GetExecId()}); err != nil {
		t.Fatalf("StartExec failed: %v", err)
	}
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	artifactPath := filepath.Join(artifactRoot, createResp.GetSandboxId(), execResp.GetExecId()+".jsonl")
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != "{\"state\":\"finished\"}\n" {
		t.Fatalf("unexpected artifact content: %q", string(content))
	}
}

func TestCreateExecFailsFastWhenArtifactTemplateEscapesRoot(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ArtifactOutputRoot:     t.TempDir(),
		ArtifactOutputTemplate: "../escape.jsonl",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "consumer",
			OwnerType: "workspace",
			OwnerId:   "workspace-escape",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected failed precondition, got %v", err)
	}
}

func TestExplicitErrorSemantics(t *testing.T) {
	client := newBufconnClient(t, DefaultServiceConfig())

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-1",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	if _, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-1",
		},
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected conflict error, got %v", err)
	}

	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo"},
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected not-ready error, got %v", err)
	}

	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.StartExec(context.Background(), &agboxv1.StartExecRequest{ExecId: execResp.GetExecId()}); err != nil {
		t.Fatalf("StartExec failed: %v", err)
	}
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	if _, err := client.CancelExec(context.Background(), &agboxv1.CancelExecRequest{ExecId: execResp.GetExecId()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected terminal error, got %v", err)
	}
	if _, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestCursorReplayAndExpiration(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ReplayLimit:     4,
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-2",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	firstEvent, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}

	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	for range 4 {
		if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{
			SandboxId: createResp.GetSandboxId(),
			Reason:    "cleanup_idle_session",
		}); err != nil {
			t.Fatalf("StopSandbox failed: %v", err)
		}
		waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
		if _, err := client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{
			SandboxId: createResp.GetSandboxId(),
		}); err != nil {
			t.Fatalf("ResumeSandbox failed: %v", err)
		}
		waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	}

	expired, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:  createResp.GetSandboxId(),
		FromCursor: firstEvent.GetCursor(),
	})
	if err == nil {
		_, err = expired.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected cursor expiration, got %v", err)
	}

	sandboxResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	replay, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:              createResp.GetSandboxId(),
		FromCursor:             sandboxResp.GetSandbox().GetLastEventCursor(),
		IncludeCurrentSnapshot: true,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents(snapshot) failed: %v", err)
	}
	snapshotEvent, err := replay.Recv()
	if err != nil {
		t.Fatalf("snapshot Recv failed: %v", err)
	}
	if !snapshotEvent.GetReplay() || !snapshotEvent.GetSnapshot() {
		t.Fatalf("expected replay snapshot event, got %#v", snapshotEvent)
	}
}

func TestStructuredActionMetadataForStopDeleteAndCancel(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ReplayLimit:     16,
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-structured-actions",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"sleep", "1"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.CancelExec(context.Background(), &agboxv1.CancelExecRequest{
		ExecId: execResp.GetExecId(),
	}); err != nil {
		t.Fatalf("CancelExec failed: %v", err)
	}
	cancelEvents := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_EXEC_CANCELLED {
				return true
			}
		}
		return false
	})
	cancelEvent := cancelEvents[len(cancelEvents)-1]
	if cancelEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_EXECUTE_RUN || cancelEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_CANCEL_RUN_EXEC {
		t.Fatalf("unexpected cancel action metadata: %#v", cancelEvent)
	}

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{
		SandboxId:      createResp.GetSandboxId(),
		ActionReason:   agboxv1.ActionReason_ACTION_REASON_CLEANUP_IDLE_SESSION,
		ActionStrategy: agboxv1.ActionStrategy_ACTION_STRATEGY_IDLE_SESSION_STOP,
	}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	stopEvents := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_SANDBOX_STOPPED {
				return true
			}
		}
		return false
	})
	stopEvent := stopEvents[len(stopEvents)-1]
	if stopEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_CLEANUP_IDLE_SESSION || stopEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_IDLE_SESSION_STOP {
		t.Fatalf("unexpected stop action metadata: %#v", stopEvent)
	}

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{
		SandboxId:      createResp.GetSandboxId(),
		ActionReason:   agboxv1.ActionReason_ACTION_REASON_CLEANUP_LEAKED_SESSION_RESOURCES,
		ActionStrategy: agboxv1.ActionStrategy_ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME,
	}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	deleteEvents := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETED {
				return true
			}
		}
		return false
	})
	deleteEvent := deleteEvents[len(deleteEvents)-1]
	if deleteEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_CLEANUP_LEAKED_SESSION_RESOURCES || deleteEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_DELETE_SANDBOX_RUNTIME {
		t.Fatalf("unexpected delete action metadata: %#v", deleteEvent)
	}
}

func TestInvalidLegacyActionReasonIsRejected(t *testing.T) {
	client := newBufconnClient(t, DefaultServiceConfig())

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-invalid-action",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{
		SandboxId: createResp.GetSandboxId(),
		Reason:    "invalid_reason",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestIdleTTLStopsReadySandboxAfterTerminalExec(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ReplayLimit:     16,
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         20 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxOwner: &agboxv1.SandboxOwner{
			Product:   "aihub",
			OwnerType: "session",
			OwnerId:   "session-idle-ttl",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo", "idle"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.StartExec(context.Background(), &agboxv1.StartExecRequest{
		ExecId: execResp.GetExecId(),
	}); err != nil {
		t.Fatalf("StartExec failed: %v", err)
	}

	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_SANDBOX_STOPPED {
				return true
			}
		}
		return false
	})
	lastEvent := events[len(events)-1]
	if lastEvent.GetEventType() != agboxv1.EventType_SANDBOX_STOPPED {
		t.Fatalf("unexpected idle-stop terminal event: %#v", lastEvent)
	}
	if lastEvent.GetReason() != "cleanup_idle_session" {
		t.Fatalf("unexpected idle-stop reason: %#v", lastEvent)
	}
	if lastEvent.GetActionReason() != agboxv1.ActionReason_ACTION_REASON_CLEANUP_IDLE_SESSION {
		t.Fatalf("unexpected idle-stop action reason: %#v", lastEvent)
	}
	if lastEvent.GetActionStrategy() != agboxv1.ActionStrategy_ACTION_STRATEGY_IDLE_SESSION_STOP {
		t.Fatalf("unexpected idle-stop action strategy: %#v", lastEvent)
	}
}

func newBufconnClient(t *testing.T, config ServiceConfig) agboxv1.SandboxServiceClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	agboxv1.RegisterSandboxServiceServer(server, NewService(config))
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agboxv1.NewSandboxServiceClient(conn)
}

func collectEventsUntil(t *testing.T, stream agboxv1.SandboxService_SubscribeSandboxEventsClient, done func([]*agboxv1.SandboxEvent) bool) []*agboxv1.SandboxEvent {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	var events []*agboxv1.SandboxEvent
	for time.Now().Before(deadline) {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Recv failed: %v", err)
		}
		events = append(events, event)
		if done(events) {
			return events
		}
	}
	t.Fatalf("timed out waiting for events: %#v", events)
	return nil
}

func waitForSandboxState(t *testing.T, client agboxv1.SandboxServiceClient, sandboxID string, expected agboxv1.SandboxState) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
		if err != nil {
			t.Fatalf("GetSandbox failed: %v", err)
		}
		if resp.GetSandbox().GetState() == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sandbox %s did not reach state %s", sandboxID, expected)
}

func waitForExecState(t *testing.T, client agboxv1.SandboxServiceClient, execID string, expected agboxv1.ExecState) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execID})
		if err != nil {
			t.Fatalf("GetExec failed: %v", err)
		}
		if resp.GetExec().GetState() == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("exec %s did not reach state %s", execID, expected)
}
