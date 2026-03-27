package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/google/uuid"
	bbolt "go.etcd.io/bbolt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSandboxLifecycleAndExecStream(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "session-1",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			RequiredServices: []*agboxv1.ServiceSpec{
				{
					Name:  "db",
					Image: "postgres:16",
					Healthcheck: &agboxv1.HealthcheckConfig{
						Test: []string{"CMD", "true"},
					},
				},
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
		agboxv1.EventType_SANDBOX_SERVICE_READY,
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

	events = collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_EXEC_FINISHED {
				return true
			}
		}
		return false
	})

	var startEvent *agboxv1.SandboxEvent
	last := events[len(events)-1]
	for _, event := range events {
		if event.GetEventType() == agboxv1.EventType_EXEC_STARTED {
			startEvent = event
		}
	}
	if startEvent == nil || startEvent.GetExecId() != execResp.GetExecId() {
		t.Fatalf("missing exec started event: %#v", startEvent)
	}
	if last.GetEventType() != agboxv1.EventType_EXEC_FINISHED || last.GetExecId() != execResp.GetExecId() || last.GetExitCode() != 0 {
		t.Fatalf("unexpected exec terminal event: %#v", last)
	}
}

func TestConfiguredArtifactOutputPathIsCreatedForExecs(t *testing.T) {
	artifactRoot := t.TempDir()
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay:        5 * time.Millisecond,
		PollInterval:           2 * time.Millisecond,
		ArtifactOutputRoot:     artifactRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}.log",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("workspace-1", "ghcr.io/agents-sandbox/coding-runtime:test"))
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
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	artifactPath := filepath.Join(artifactRoot, createResp.GetSandboxId(), execResp.GetExecId()+".log")
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 1 {
		t.Fatalf("unexpected artifact content: %q", string(content))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if payload["state"] != agboxv1.ExecState_EXEC_STATE_FINISHED.String() {
		t.Fatalf("unexpected artifact state: %#v", payload)
	}
}

func TestCreateExecFailsFastWhenArtifactTemplateEscapesRoot(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		ArtifactOutputRoot:     t.TempDir(),
		ArtifactOutputTemplate: "../escape.log",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("workspace-escape", "ghcr.io/agents-sandbox/coding-runtime:test"))
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

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-1", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	if _, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-1", "ghcr.io/agents-sandbox/coding-runtime:test")); status.Code(err) != codes.AlreadyExists {
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
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	if _, err := client.CancelExec(context.Background(), &agboxv1.CancelExecRequest{ExecId: execResp.GetExecId()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected terminal error, got %v", err)
	}
	if _, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("expected not-found error, got %v", err)
	}
	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestCallerProvidedSandboxID(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "issue11-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	if createResp.GetSandboxId() != "issue11-sandbox" {
		t.Fatalf("unexpected sandbox id: %q", createResp.GetSandboxId())
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	eventCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.SubscribeSandboxEvents(eventCtx, &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:              createResp.GetSandboxId(),
		IncludeCurrentSnapshot: true,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if event.GetSandboxId() != "issue11-sandbox" {
		t.Fatalf("unexpected event sandbox id: %q", event.GetSandboxId())
	}

	resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "issue11-sandbox"})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if resp.GetSandbox().GetSandboxId() != "issue11-sandbox" {
		t.Fatalf("unexpected handle sandbox id: %q", resp.GetSandbox().GetSandboxId())
	}
}

func TestCallerProvidedSandboxIDValidation(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})
	invalidIDs := []string{
		"-mybox",   // must start with letter or digit
		"_mybox",   // must start with letter or digit
		"mybox-",   // must end with letter or digit
		"mybox_",   // must end with letter or digit
		"ab",       // too short (< 4 characters)
		"a" + strings.Repeat("x", 200) + "z", // too long (> 200)
		"my/box",   // slashes not allowed
		"my.box",   // dots not allowed
		"my box",   // spaces not allowed
	}
	for _, sandboxID := range invalidIDs {
		t.Run(sandboxID, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId:  sandboxID,
				CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}
}

func TestCallerProvidedSandboxIDAcceptsFlexibleFormats(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})
	validIDs := []string{
		"MyBox-1",                                     // mixed case
		"my_box_1",                                    // underscores
		"36d4492a-f142-4d30-afbe-7954cf698d73",        // UUID
		"Session_With-Mixed_Chars-123",                // mixed separators
		"ALLCAPS",                                     // all uppercase
	}
	for _, sandboxID := range validIDs {
		t.Run(sandboxID, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId:  sandboxID,
				CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
			})
			if err != nil {
				t.Fatalf("expected valid sandbox_id %q, got error: %v", sandboxID, err)
			}
		})
	}
}

func TestCallerProvidedSandboxIDDuplicate(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})

	if _, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "dup-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	}); err != nil {
		t.Fatalf("CreateSandbox(first) failed: %v", err)
	}
	_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "dup-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists, got %v", err)
	}
	assertErrorReason(t, err, ReasonSandboxIDAlreadyExists)
}

func TestDaemonGeneratedSandboxIDUsesUUIDAndRegistry(t *testing.T) {
	registry := newMemoryIDRegistry()
	client := newBufconnClient(t, ServiceConfig{idRegistry: registry})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	if _, err := uuid.Parse(createResp.GetSandboxId()); err != nil {
		t.Fatalf("expected UUIDv4 sandbox id, got %q: %v", createResp.GetSandboxId(), err)
	}
	if _, ok := registry.sandboxIDs[createResp.GetSandboxId()]; !ok {
		t.Fatalf("sandbox id %q was not recorded in registry", createResp.GetSandboxId())
	}
	if err := registry.ReserveSandboxID(createResp.GetSandboxId(), time.Now().UTC()); !errors.Is(err, errSandboxIDAlreadyExists) {
		t.Fatalf("expected duplicate registry reservation to fail, got %v", err)
	}
}

func TestCallerProvidedExecID(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "issue11-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		ExecId:    "issue11-exec",
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if execResp.GetExecId() != "issue11-exec" {
		t.Fatalf("unexpected exec id: %q", execResp.GetExecId())
	}
	resp, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: "issue11-exec"})
	if err != nil {
		t.Fatalf("GetExec failed: %v", err)
	}
	if resp.GetExec().GetExecId() != "issue11-exec" {
		t.Fatalf("unexpected exec handle: %q", resp.GetExec().GetExecId())
	}
	if got := resp.GetExec().GetLastEventSequence(); got == 0 {
		t.Fatal("expected non-zero exec last_event_sequence")
	}
}

func TestExecIDValidationDuplicateAndUUIDFallback(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})
	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "exec-validation-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	for _, execID := range []string{"-myexec", "ab", "exec/"} {
		t.Run("invalid-"+execID, func(t *testing.T) {
			_, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
				SandboxId: createResp.GetSandboxId(),
				ExecId:    execID,
				Command:   []string{"echo"},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}

	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		ExecId:    "dup-exec",
		Command:   []string{"echo"},
	}); err != nil {
		t.Fatalf("CreateExec(first) failed: %v", err)
	}
	_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		ExecId:    "dup-exec",
		Command:   []string{"echo"},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists, got %v", err)
	}
	assertErrorReason(t, err, ReasonExecIDAlreadyExists)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo", "uuid"},
	})
	if err != nil {
		t.Fatalf("CreateExec(generated) failed: %v", err)
	}
	if _, err := uuid.Parse(execResp.GetExecId()); err != nil {
		t.Fatalf("expected UUID exec id, got %q: %v", execResp.GetExecId(), err)
	}
}

func TestPersistentIDRegistrySurvivesServiceRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	registry, err := openPersistentIDRegistry(dbPath)
	if err != nil {
		t.Fatalf("openPersistentIDRegistry(first) failed: %v", err)
	}
	client := newBufconnClient(t, ServiceConfig{idRegistry: registry})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "persistent-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		ExecId:    "persistent-exec",
		Command:   []string{"echo"},
	}); err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close(first registry) failed: %v", err)
	}

	db, err := bbolt.Open(dbPath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("bbolt.Open(readonly) failed: %v", err)
	}
	if err := db.View(func(tx *bbolt.Tx) error {
		bucketNames := []string{string(sandboxIDBucket), string(execIDBucket)}
		for _, bucketName := range bucketNames {
			bucket := tx.Bucket([]byte(bucketName))
			if bucket == nil {
				t.Fatalf("missing bucket %q", bucketName)
			}
		}
		rawSandbox := tx.Bucket(sandboxIDBucket).Get([]byte("persistent-sandbox"))
		rawExec := tx.Bucket(execIDBucket).Get([]byte("persistent-exec"))
		if _, err := time.Parse(time.RFC3339Nano, string(rawSandbox)); err != nil {
			t.Fatalf("sandbox registry timestamp is invalid: %v", err)
		}
		if _, err := time.Parse(time.RFC3339Nano, string(rawExec)); err != nil {
			t.Fatalf("exec registry timestamp is invalid: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("db.View failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(readonly db) failed: %v", err)
	}

	registry2, err := openPersistentIDRegistry(dbPath)
	if err != nil {
		t.Fatalf("openPersistentIDRegistry(second) failed: %v", err)
	}
	defer registry2.Close()
	client2 := newBufconnClient(t, ServiceConfig{idRegistry: registry2})

	_, err = client2.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "persistent-sandbox",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists for sandbox id, got %v", err)
	}
	assertErrorReason(t, err, ReasonSandboxIDAlreadyExists)

	secondSandbox, err := client2.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "persistent-sandbox-2",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox(second) failed: %v", err)
	}
	waitForSandboxState(t, client2, secondSandbox.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	_, err = client2.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: secondSandbox.GetSandboxId(),
		ExecId:    "persistent-exec",
		Command:   []string{"echo"},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists for exec id, got %v", err)
	}
	assertErrorReason(t, err, ReasonExecIDAlreadyExists)
}

func TestEventPersistenceAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("persist-restart", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
	}, dbPath)
	stream, err := second.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		return len(items) == 3
	})
	wantTypes := []agboxv1.EventType{
		agboxv1.EventType_SANDBOX_ACCEPTED,
		agboxv1.EventType_SANDBOX_PREPARING,
		agboxv1.EventType_SANDBOX_READY,
	}
	for index, event := range events {
		if event.GetEventType() != wantTypes[index] {
			t.Fatalf("unexpected event type at %d: got %s want %s", index, event.GetEventType(), wantTypes[index])
		}
		if !event.GetReplay() {
			t.Fatalf("expected replay event at %d", index)
		}
		if event.GetSequence() != uint64(index+1) {
			t.Fatalf("unexpected sequence at %d: got %d want %d", index, event.GetSequence(), index+1)
		}
		if event.GetSequence() != uint64(index+1) {
			t.Fatalf("unexpected sequence at %d: got %d want %d", index, event.GetSequence(), index+1)
		}
	}
}

func TestDeletedSandboxEventsRetained(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay:   5 * time.Millisecond,
		PollInterval:      2 * time.Millisecond,
		EventRetentionTTL: time.Hour,
	}, dbPath)
	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("retain-delete", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := harness.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	stream, err := harness.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, event := range items {
			if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETED {
				return true
			}
		}
		return false
	})
	var sawDeleteRequested bool
	for _, event := range events {
		if event.GetEventType() == agboxv1.EventType_SANDBOX_DELETE_REQUESTED {
			sawDeleteRequested = true
		}
	}
	if !sawDeleteRequested || events[len(events)-1].GetEventType() != agboxv1.EventType_SANDBOX_DELETED {
		t.Fatalf("expected delete events to be retained, got %#v", events)
	}
}

func TestExpiredEventsCleanedUp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay:   5 * time.Millisecond,
		PollInterval:      2 * time.Millisecond,
		EventRetentionTTL: time.Millisecond,
	}, dbPath)
	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("cleanup-expired", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := harness.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	if err := harness.service.config.eventStore.MarkDeleted(createResp.GetSandboxId(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	if err := harness.service.cleanupExpiredEvents(); err != nil {
		t.Fatalf("cleanupExpiredEvents failed: %v", err)
	}

	_, err = harness.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err == nil {
		t.Fatal("expected sandbox to be removed after cleanup")
	}
	assertStatusErrorReason(t, err, codes.NotFound, ReasonSandboxNotFound)
}

func TestRecoveredSandboxRejectsExecButAllowsDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("recovered-only", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval:      2 * time.Millisecond,
		runtimeBackend:    &scriptedRuntimeBackend{deleteErr: errors.New("runtime delete should not be called")},
		EventRetentionTTL: time.Hour,
	}, dbPath)

	_, err = second.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandboxId(),
		Command:   []string{"echo", "blocked"},
	})
	assertStatusErrorReason(t, err, codes.FailedPrecondition, ReasonSandboxRecoveredOnly)

	_, err = second.client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandboxId()})
	assertStatusErrorReason(t, err, codes.FailedPrecondition, ReasonSandboxRecoveredOnly)

	_, err = second.client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandboxId()})
	assertStatusErrorReason(t, err, codes.FailedPrecondition, ReasonSandboxRecoveredOnly)

	if _, err := second.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, second.client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)
}

func TestJoinServiceClosersClosesRuntimeBeforeRegistry(t *testing.T) {
	runtimeErr := errors.New("runtime close failed")
	registryErr := errors.New("registry close failed")
	closeOrder := make([]string, 0, 2)
	closer := joinServiceClosers(
		recordingCloser{name: "runtime", order: &closeOrder, err: runtimeErr},
		recordingCloser{name: "registry", order: &closeOrder, err: registryErr},
	)

	err := closer.Close()

	if !reflect.DeepEqual(closeOrder, []string{"runtime", "registry"}) {
		t.Fatalf("unexpected closer order: got %v want %v", closeOrder, []string{"runtime", "registry"})
	}
	if !errors.Is(err, runtimeErr) || !errors.Is(err, registryErr) {
		t.Fatalf("expected joined close error, got %v", err)
	}
}

func TestNewServiceDoesNotRequireReachableDockerDaemonAtConstruction(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "nonexistent-docker.sock"))

	service, closer, err := NewService(ServiceConfig{})
	if err != nil {
		t.Fatalf("NewService failed with unreachable docker host: %v", err)
	}
	if service == nil {
		t.Fatal("expected service instance")
	}
	if closer == nil {
		t.Fatal("expected runtime closer")
	}
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("service closer failed: %v", closeErr)
	}
}

func TestSandboxOwnerRemoved(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{})

	if _, ok := reflect.TypeOf(agboxv1.CreateSandboxRequest{}).FieldByName("SandboxOwner"); ok {
		t.Fatal("CreateSandboxRequest should not expose SandboxOwner")
	}
	if _, ok := reflect.TypeOf(agboxv1.SandboxHandle{}).FieldByName("SandboxOwner"); ok {
		t.Fatal("SandboxHandle should not expose SandboxOwner")
	}
	if _, ok := reflect.TypeOf(agboxv1.ListSandboxesRequest{}).FieldByName("SandboxOwner"); ok {
		t.Fatal("ListSandboxesRequest should not expose SandboxOwner")
	}

	first, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "owner-removed-1",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox(first) failed: %v", err)
	}
	second, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "owner-removed-2",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if err != nil {
		t.Fatalf("CreateSandbox(second) failed: %v", err)
	}
	if first.GetSandboxId() == second.GetSandboxId() {
		t.Fatal("expected distinct sandbox ids")
	}

	_, err = client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "owner-removed-1",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected duplicate sandbox id to fail, got %v", err)
	}
	assertErrorReason(t, err, ReasonSandboxIDAlreadyExists)
}
