package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestDefaultServiceConfig(t *testing.T) {
	cfg := DefaultServiceConfig()
	if cfg.IdleTTL != 10*time.Minute {
		t.Fatalf("expected IdleTTL 10m, got %s", cfg.IdleTTL)
	}
	if cfg.CleanupTTL != 360*time.Hour {
		t.Fatalf("expected CleanupTTL 360h, got %s", cfg.CleanupTTL)
	}
	if cfg.CleanupInterval != 2*time.Minute {
		t.Fatalf("expected CleanupInterval 2m, got %s", cfg.CleanupInterval)
	}
}

func TestSubscribeSandboxEventsReplayFromZeroSequence(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-2", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("ResumeSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	replay, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	fullHistory := collectEventsUntil(t, replay, func(items []*agboxv1.SandboxEvent) bool {
		return len(items) == 7
	})
	wantReplay := []agboxv1.EventType{
		agboxv1.EventType_SANDBOX_ACCEPTED,
		agboxv1.EventType_SANDBOX_PREPARING,
		agboxv1.EventType_SANDBOX_READY,
		agboxv1.EventType_SANDBOX_STOP_REQUESTED,
		agboxv1.EventType_SANDBOX_STOPPED,
		agboxv1.EventType_SANDBOX_ACCEPTED,
		agboxv1.EventType_SANDBOX_READY,
	}
	for index, event := range fullHistory {
		if event.GetEventType() != wantReplay[index] {
			t.Fatalf("unexpected replay event at %d: got %s want %s", index, event.GetEventType(), wantReplay[index])
		}
		if !event.GetReplay() {
			t.Fatalf("expected replay flag at %d", index)
		}
	}

	incremental, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		FromSequence: fullHistory[2].GetSequence(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents(incremental) failed: %v", err)
	}
	incrementalEvents := collectEventsUntil(t, incremental, func(items []*agboxv1.SandboxEvent) bool {
		return len(items) == 4
	})
	for _, event := range incrementalEvents {
		if event.GetSequence() <= fullHistory[2].GetSequence() {
			t.Fatalf("expected only incremental events, got sequence %d", event.GetSequence())
		}
	}
}

func TestExpiredSequenceReturnsOutOfRangeError(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("expired-sequence", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		FromSequence: 99,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected expired sequence error")
	}
	assertStatusErrorReason(t, err, codes.OutOfRange, ReasonSandboxEventSequenceExpired)
}

func TestCreateSandboxFailsWhenAcceptedEventAppendFails(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		eventStore: scriptedEventStore{
			appendFn: func(_ string, event *agboxv1.SandboxEvent) error {
				if event.GetEventType() == agboxv1.EventType_SANDBOX_ACCEPTED {
					return errors.New("append accepted failed")
				}
				return nil
			},
		},
	})

	_, err := client.CreateSandbox(context.Background(), createSandboxRequest("append-create-fails", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err == nil {
		t.Fatal("expected CreateSandbox to fail")
	}
	assertStatusCode(t, err, codes.Internal)

	_, getErr := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "append-create-fails"})
	if getErr == nil {
		t.Fatal("expected sandbox to be absent after append failure")
	}
	assertStatusErrorReason(t, getErr, codes.NotFound, ReasonSandboxNotFound)
}

func TestCreateSandboxAcceptFailureReleasesSandboxID(t *testing.T) {
	registry := newMemoryIDRegistry()
	client := newBufconnClient(t, ServiceConfig{
		idRegistry: registry,
		eventStore: scriptedEventStore{
			appendFn: func(_ string, event *agboxv1.SandboxEvent) error {
				if event.GetEventType() == agboxv1.EventType_SANDBOX_ACCEPTED {
					return errors.New("append accepted failed")
				}
				return nil
			},
		},
	})

	_, err := client.CreateSandbox(context.Background(), createSandboxRequest("reusable-sandbox", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err == nil {
		t.Fatal("expected first CreateSandbox to fail")
	}
	assertStatusCode(t, err, codes.Internal)

	client = newBufconnClient(t, ServiceConfig{idRegistry: registry})
	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("reusable-sandbox", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox retry failed: %v", err)
	}
	if createResp.GetSandbox().GetSandboxId() != "reusable-sandbox" {
		t.Fatalf("unexpected sandbox id: %s", createResp.GetSandbox().GetSandboxId())
	}
}

func TestSandboxStaysPendingWhenReadyEventAppendFails(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		eventStore: scriptedEventStore{
			appendFn: func(_ string, event *agboxv1.SandboxEvent) error {
				if event.GetEventType() == agboxv1.EventType_SANDBOX_READY {
					return errors.New("append ready failed")
				}
				return nil
			},
		},
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("append-ready-fails", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		resp, getErr := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
		if getErr != nil {
			t.Fatalf("GetSandbox failed: %v", getErr)
		}
		if resp.GetSandbox().GetState() == agboxv1.SandboxState_SANDBOX_STATE_PENDING {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	t.Fatalf("expected sandbox to remain pending, got %s", resp.GetSandbox().GetState())
}

func TestDeleteSandboxesFailsWhenDeleteRequestedEventAppendFails(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		eventStore: scriptedEventStore{
			appendFn: func(_ string, event *agboxv1.SandboxEvent) error {
				if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETE_REQUESTED {
					return errors.New("append delete requested failed")
				}
				return nil
			},
		},
	})

	request := createSandboxRequest("delete-by-label-append-fails", "ghcr.io/agents-sandbox/coding-runtime:test")
	request.CreateSpec.Labels = map[string]string{"team": "a"}
	if _, err := client.CreateSandbox(context.Background(), request); err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, "delete-by-label-append-fails", agboxv1.SandboxState_SANDBOX_STATE_READY)

	_, err := client.DeleteSandboxes(context.Background(), &agboxv1.DeleteSandboxesRequest{
		LabelSelector: map[string]string{"team": "a"},
	})
	if err == nil {
		t.Fatal("expected DeleteSandboxes to fail")
	}
	assertStatusCode(t, err, codes.Internal)

	getResp, getErr := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{
		SandboxId: "delete-by-label-append-fails",
	})
	if getErr != nil {
		t.Fatalf("GetSandbox failed: %v", getErr)
	}
	if getResp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected sandbox to stay ready after append failure, got %s", getResp.GetSandbox().GetState())
	}
}

func TestCreateExecStartFailureReleasesExecIDAndArtifactPath(t *testing.T) {
	registry := newMemoryIDRegistry()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	client := newBufconnClient(t, ServiceConfig{
		idRegistry:             registry,
		ArtifactOutputRoot:     outputRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.log",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("exec-retry-sandbox", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	client = newBufconnClient(t, ServiceConfig{
		idRegistry:             registry,
		ArtifactOutputRoot:     outputRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.log",
		eventStore: scriptedEventStore{
			appendFn: func(_ string, event *agboxv1.SandboxEvent) error {
				if event.GetEventType() == agboxv1.EventType_EXEC_STARTED {
					return errors.New("append exec started failed")
				}
				return nil
			},
		},
	})

	createResp, err = client.CreateSandbox(context.Background(), createSandboxRequest("exec-retry-sandbox-2", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "retry-exec",
		Command:   []string{"echo", "hello"},
	})
	if err == nil {
		t.Fatal("expected CreateExec to fail")
	}
	assertStatusCode(t, err, codes.Internal)

	artifactPath := filepath.Join(outputRoot, createResp.GetSandbox().GetSandboxId(), "retry-exec.log")
	if _, statErr := os.Stat(artifactPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected artifact path to be removed, got %v", statErr)
	}

	client = newBufconnClient(t, ServiceConfig{
		idRegistry:             registry,
		ArtifactOutputRoot:     outputRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.log",
	})
	createResp, err = client.CreateSandbox(context.Background(), createSandboxRequest("exec-retry-sandbox-3", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "retry-exec",
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec retry failed: %v", err)
	}
	if execResp.GetExecId() != "retry-exec" {
		t.Fatalf("unexpected exec id: %s", execResp.GetExecId())
	}
}

func TestCancelExecEmitsCancelledEvent(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-cancel", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	if cancelEvent.GetEventType() != agboxv1.EventType_EXEC_CANCELLED || eventExecID(cancelEvent) != execResp.GetExecId() {
		t.Fatalf("unexpected cancel event: %#v", cancelEvent)
	}
}

func TestStopAndDeleteSandboxEmitRequestAndTerminalEvents(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-delete-flow", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	if eventReason(stopEvents[len(stopEvents)-1]) != "stop_requested" {
		t.Fatalf("unexpected stop reason: %#v", stopEvents[len(stopEvents)-1])
	}

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	if eventReason(deleteEvents[len(deleteEvents)-1]) != "delete_requested" {
		t.Fatalf("unexpected delete reason: %#v", deleteEvents[len(deleteEvents)-1])
	}
}

func TestIdleTTLStopsReadySandboxAfterTerminalExec(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         20 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-idle-ttl", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo", "idle"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

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
	if eventReason(lastEvent) != "idle_ttl" {
		t.Fatalf("unexpected idle-stop reason: %#v", lastEvent)
	}
}

func TestCleanupTTLDeletesStoppedSandbox(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		CleanupTTL:      1 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("cleanup-stop-delete", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Subscribe before stopping so we can observe the full event sequence.
	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}

	// Wait for cleanup_ttl to trigger deletion via event stream.
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETED {
				return true
			}
		}
		return false
	})
	var foundDeleteRequested bool
	for _, event := range events {
		if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETE_REQUESTED && eventReason(event) == "cleanup_ttl" {
			foundDeleteRequested = true
		}
	}
	if !foundDeleteRequested {
		t.Fatal("expected SANDBOX_DELETE_REQUESTED event with reason=cleanup_ttl")
	}
}

func TestCleanupTTLPurgesDeletedSandbox(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		CleanupTTL:      1 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("cleanup-purge", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}

	// Wait for cleanup_ttl to purge from memory. Poll ListSandboxes until it disappears.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{IncludeDeleted: true})
		if err != nil {
			t.Fatalf("ListSandboxes failed: %v", err)
		}
		found := false
		for _, h := range resp.GetSandboxes() {
			if h.GetSandboxId() == createResp.GetSandbox().GetSandboxId() {
				found = true
				break
			}
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sandbox was not purged from ListSandboxes(include_deleted=true) after cleanup_ttl")
}

func TestIdleTTLStopsReadySandboxWithoutExec(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         20 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-no-exec-idle", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
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
		t.Fatalf("unexpected terminal event: %#v", lastEvent)
	}
	if eventReason(lastEvent) != "idle_ttl" {
		t.Fatalf("unexpected stop reason: %#v", lastEvent)
	}
}

func TestPerSandboxIdleTTLOverridesGlobal(t *testing.T) {
	// Global idle TTL is 10s; per-sandbox override is 1ms so the sandbox
	// should be idle-stopped well before the global threshold.
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         10 * time.Second,
		CleanupInterval: 10 * time.Millisecond,
	})

	req := createSandboxRequest("per-sandbox-idle-override", "ghcr.io/agents-sandbox/coding-runtime:test")
	req.CreateSpec.IdleTtl = durationpb.New(1 * time.Millisecond)

	createResp, err := client.CreateSandbox(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
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
		t.Fatalf("expected SANDBOX_STOPPED via per-sandbox idle_ttl, got: %v", lastEvent.GetEventType())
	}
	if eventReason(lastEvent) != "idle_ttl" {
		t.Fatalf("unexpected idle-stop reason: %v", lastEvent)
	}
}

func TestPerSandboxIdleTTLZeroDisablesIdleStop(t *testing.T) {
	// Global idle TTL is short (20ms); per-sandbox override is 0 (disable).
	// The sandbox must NOT be stopped by the idle scanner.
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         20 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
	})

	req := createSandboxRequest("per-sandbox-idle-disabled", "ghcr.io/agents-sandbox/coding-runtime:test")
	req.CreateSpec.IdleTtl = durationpb.New(0)

	createResp, err := client.CreateSandbox(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Wait several cleanup cycles to confirm the sandbox is NOT idle-stopped.
	time.Sleep(200 * time.Millisecond)

	resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected sandbox to remain READY when idle_ttl=0, got %v", resp.GetSandbox().GetState())
	}
}

func TestPerSandboxIdleTTLWithGlobalDisabled(t *testing.T) {
	// Global idle TTL is 0 (disabled); per-sandbox override is 1ms.
	// The sandbox should be stopped by its per-sandbox idle TTL.
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         0, // global idle TTL disabled
		CleanupInterval: 10 * time.Millisecond,
	})

	req := createSandboxRequest("per-sandbox-idle-global-off", "ghcr.io/agents-sandbox/coding-runtime:test")
	req.CreateSpec.IdleTtl = durationpb.New(1 * time.Millisecond)

	createResp, err := client.CreateSandbox(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
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
		t.Fatalf("expected SANDBOX_STOPPED via per-sandbox idle_ttl (global disabled), got: %v", lastEvent.GetEventType())
	}
	if eventReason(lastEvent) != "idle_ttl" {
		t.Fatalf("unexpected idle-stop reason: %v", lastEvent)
	}
}

func TestValidateCreateSpecNegativeIdleTTL(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	req := createSandboxRequest("negative-idle-ttl", "ghcr.io/agents-sandbox/coding-runtime:test")
	req.CreateSpec.IdleTtl = durationpb.New(-1 * time.Second)

	_, err := client.CreateSandbox(context.Background(), req)
	if err == nil {
		t.Fatal("expected CreateSandbox to fail with negative idle_ttl")
	}
	assertStatusCode(t, err, codes.InvalidArgument)
}
