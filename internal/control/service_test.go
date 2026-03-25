package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/google/uuid"
	bbolt "go.etcd.io/bbolt"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type scriptedEventStore struct {
	appendFn            func(string, *agboxv1.SandboxEvent) error
	loadEventsFn        func(string) ([]*agboxv1.SandboxEvent, error)
	loadAllSandboxIDsFn func() ([]string, error)
	maxSequenceFn       func(string) (uint64, error)
	markDeletedFn       func(string, time.Time) error
	cleanupFn           func(time.Duration) ([]string, error)
}

func (store scriptedEventStore) Append(sandboxID string, event *agboxv1.SandboxEvent) error {
	if store.appendFn != nil {
		return store.appendFn(sandboxID, event)
	}
	return nil
}

func (store scriptedEventStore) LoadEvents(sandboxID string) ([]*agboxv1.SandboxEvent, error) {
	if store.loadEventsFn != nil {
		return store.loadEventsFn(sandboxID)
	}
	return nil, nil
}

func (store scriptedEventStore) LoadAllSandboxIDs() ([]string, error) {
	if store.loadAllSandboxIDsFn != nil {
		return store.loadAllSandboxIDsFn()
	}
	return nil, nil
}

func (store scriptedEventStore) MaxSequence(sandboxID string) (uint64, error) {
	if store.maxSequenceFn != nil {
		return store.maxSequenceFn(sandboxID)
	}
	return 0, nil
}

func (store scriptedEventStore) MarkDeleted(sandboxID string, deletedAt time.Time) error {
	if store.markDeletedFn != nil {
		return store.markDeletedFn(sandboxID, deletedAt)
	}
	return nil
}

func (store scriptedEventStore) Cleanup(retentionTTL time.Duration) ([]string, error) {
	if store.cleanupFn != nil {
		return store.cleanupFn(retentionTTL)
	}
	return nil, nil
}

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
		"MyBox",
		"my_box",
		"-mybox",
		"mybox-",
		"ab",
		"a234567890123456789012345678901234567890123456789012345678901234",
		"my/box",
		"my.box",
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

	for _, execID := range []string{"MyExec", "my_exec", "-myexec", "ab"} {
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
		CallerMetadata: &agboxv1.CallerMetadata{
			Product: "p",
			RunId:   "r1",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox(first) failed: %v", err)
	}
	second, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "owner-removed-2",
		CreateSpec: createSpecWithImage("ghcr.io/agents-sandbox/coding-runtime:test"),
		CallerMetadata: &agboxv1.CallerMetadata{
			Product: "p",
			RunId:   "r1",
		},
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
		CallerMetadata: &agboxv1.CallerMetadata{
			Product: "p",
			RunId:   "r2",
		},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected duplicate sandbox id to fail, got %v", err)
	}
	assertErrorReason(t, err, ReasonSandboxIDAlreadyExists)
}

func TestCreateSandboxRequiresExplicitImage(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-valid", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox(valid) failed: %v", err)
	}
	if createResp.GetSandboxId() == "" {
		t.Fatal("expected sandbox_id for valid request")
	}

	testCases := []struct {
		name    string
		request *agboxv1.CreateSandboxRequest
	}{
		{
			name: "missing_create_spec",
			request: &agboxv1.CreateSandboxRequest{
				SandboxId: "session-missing-spec",
			},
		},
		{
			name:    "empty_image",
			request: createSandboxRequest("session-empty-image", ""),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), testCase.request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}
}

func TestCreateSandboxUsesRequestedImageForRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-runtime-image", "example.com/custom/runtime:1.2.3"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if runtime.lastCreateImage != "example.com/custom/runtime:1.2.3" {
		t.Fatalf("unexpected runtime image: got %q", runtime.lastCreateImage)
	}
}

func TestCreateSandboxPassesMountsCopiesAndBuiltinResourcesToRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})
	mountSource := filepath.Join(t.TempDir(), "mount")
	if err := os.MkdirAll(mountSource, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	copySource := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(copySource, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "session-generic-inputs",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "example.com/custom/runtime:1.2.3",
			Mounts: []*agboxv1.MountSpec{
				{Source: mountSource, Target: "/work/mount", Writable: true},
			},
			Copies: []*agboxv1.CopySpec{
				{Source: copySource, Target: "/workspace/project", ExcludePatterns: []string{".git"}},
			},
			BuiltinResources: []string{".claude", "uv"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if got := runtime.lastCreateSpec.GetMounts(); len(got) != 1 || got[0].GetTarget() != "/work/mount" {
		t.Fatalf("unexpected mounts passed to runtime: %#v", got)
	}
	if got := runtime.lastCreateSpec.GetCopies(); len(got) != 1 || got[0].GetTarget() != "/workspace/project" {
		t.Fatalf("unexpected copies passed to runtime: %#v", got)
	}
	if got := runtime.lastCreateSpec.GetBuiltinResources(); len(got) != 2 || got[0] != ".claude" || got[1] != "uv" {
		t.Fatalf("unexpected builtin resources passed to runtime: %#v", got)
	}
}

func TestCreateSandboxWithLabels(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})
	request := createSandboxRequest("session-with-labels", "ghcr.io/agents-sandbox/coding-runtime:test")
	request.CreateSpec.Labels = map[string]string{
		"owner": "team-a",
		"env":   "dev",
	}

	createResp, err := client.CreateSandbox(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	request.CreateSpec.Labels["owner"] = "mutated"
	request.CreateSpec.Labels["new"] = "value"
	if !reflect.DeepEqual(runtime.lastCreateSpec.GetLabels(), map[string]string{"owner": "team-a", "env": "dev"}) {
		t.Fatalf("runtime labels were not cloned: %#v", runtime.lastCreateSpec.GetLabels())
	}

	getResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if !reflect.DeepEqual(getResp.GetSandbox().GetLabels(), map[string]string{"owner": "team-a", "env": "dev"}) {
		t.Fatalf("unexpected sandbox labels: %#v", getResp.GetSandbox().GetLabels())
	}

	getResp.Sandbox.Labels["owner"] = "changed"
	verifyResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox verify failed: %v", err)
	}
	if verifyResp.GetSandbox().GetLabels()["owner"] != "team-a" {
		t.Fatalf("sandbox labels should be returned as clones: %#v", verifyResp.GetSandbox().GetLabels())
	}
}

func TestCreateSandboxWithoutLabels(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-without-labels", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	getResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if len(getResp.GetSandbox().GetLabels()) != 0 {
		t.Fatalf("expected no labels, got %#v", getResp.GetSandbox().GetLabels())
	}
	if len(runtime.lastCreateSpec.GetLabels()) != 0 {
		t.Fatalf("expected runtime labels to stay empty, got %#v", runtime.lastCreateSpec.GetLabels())
	}
}

func TestCreateSandboxLabelsNoValidation(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})
	longKey := strings.Repeat("k", 1024)
	longValue := strings.Repeat("v", 2048)
	request := createSandboxRequest("session-labels-no-validation", "ghcr.io/agents-sandbox/coding-runtime:test")
	request.CreateSpec.Labels = map[string]string{longKey: longValue}

	createResp, err := client.CreateSandbox(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	getResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if got := getResp.GetSandbox().GetLabels()[longKey]; got != longValue {
		t.Fatalf("unexpected long label value: got %q want %q", got, longValue)
	}
}

func TestListSandboxesWithLabelSelector(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	for _, item := range []struct {
		sandboxID string
		labels    map[string]string
	}{
		{sandboxID: "selector-api-dev", labels: map[string]string{"env": "dev", "tier": "api"}},
		{sandboxID: "selector-worker-dev", labels: map[string]string{"env": "dev", "tier": "worker"}},
		{sandboxID: "selector-api-prod", labels: map[string]string{"env": "prod", "tier": "api"}},
	} {
		request := createSandboxRequest(item.sandboxID, "ghcr.io/agents-sandbox/coding-runtime:test")
		request.CreateSpec.Labels = item.labels
		if _, err := client.CreateSandbox(context.Background(), request); err != nil {
			t.Fatalf("CreateSandbox(%s) failed: %v", item.sandboxID, err)
		}
		waitForSandboxState(t, client, item.sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	}

	listAll, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{})
	if err != nil {
		t.Fatalf("ListSandboxes(all) failed: %v", err)
	}
	if got := sandboxIDs(listAll.GetSandboxes()); !reflect.DeepEqual(got, []string{"selector-api-dev", "selector-api-prod", "selector-worker-dev"}) {
		t.Fatalf("unexpected all sandboxes: %#v", got)
	}

	listEnv, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{
		LabelSelector: map[string]string{"env": "dev"},
	})
	if err != nil {
		t.Fatalf("ListSandboxes(env) failed: %v", err)
	}
	if got := sandboxIDs(listEnv.GetSandboxes()); !reflect.DeepEqual(got, []string{"selector-api-dev", "selector-worker-dev"}) {
		t.Fatalf("unexpected env selector result: %#v", got)
	}

	listAnd, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{
		LabelSelector: map[string]string{"env": "dev", "tier": "api"},
	})
	if err != nil {
		t.Fatalf("ListSandboxes(and) failed: %v", err)
	}
	if got := sandboxIDs(listAnd.GetSandboxes()); !reflect.DeepEqual(got, []string{"selector-api-dev"}) {
		t.Fatalf("unexpected AND selector result: %#v", got)
	}

	listNone, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{
		LabelSelector: map[string]string{"env": "stage"},
	})
	if err != nil {
		t.Fatalf("ListSandboxes(none) failed: %v", err)
	}
	if len(listNone.GetSandboxes()) != 0 {
		t.Fatalf("expected no sandboxes, got %#v", sandboxIDs(listNone.GetSandboxes()))
	}
}

func TestListSandboxesReturnsLabels(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})
	request := createSandboxRequest("session-list-labels", "ghcr.io/agents-sandbox/coding-runtime:test")
	request.CreateSpec.Labels = map[string]string{"owner": "team-a", "env": "dev"}

	if _, err := client.CreateSandbox(context.Background(), request); err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, "session-list-labels", agboxv1.SandboxState_SANDBOX_STATE_READY)

	listResp, err := client.ListSandboxes(context.Background(), &agboxv1.ListSandboxesRequest{})
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if len(listResp.GetSandboxes()) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(listResp.GetSandboxes()))
	}
	if !reflect.DeepEqual(listResp.GetSandboxes()[0].GetLabels(), map[string]string{"owner": "team-a", "env": "dev"}) {
		t.Fatalf("unexpected labels in list response: %#v", listResp.GetSandboxes()[0].GetLabels())
	}
}

func TestDeleteSandboxesByLabels(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 100 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	for _, item := range []struct {
		sandboxID string
		labels    map[string]string
	}{
		{sandboxID: "delete-team-a-1", labels: map[string]string{"team": "a", "env": "dev"}},
		{sandboxID: "delete-team-a-2", labels: map[string]string{"team": "a", "env": "prod"}},
		{sandboxID: "delete-team-a-skip", labels: map[string]string{"team": "a", "env": "stage"}},
		{sandboxID: "delete-team-b", labels: map[string]string{"team": "b", "env": "dev"}},
	} {
		request := createSandboxRequest(item.sandboxID, "ghcr.io/agents-sandbox/coding-runtime:test")
		request.CreateSpec.Labels = item.labels
		if _, err := client.CreateSandbox(context.Background(), request); err != nil {
			t.Fatalf("CreateSandbox(%s) failed: %v", item.sandboxID, err)
		}
		waitForSandboxState(t, client, item.sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)
	}

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: "delete-team-a-skip"}); err != nil {
		t.Fatalf("DeleteSandbox(skip) failed: %v", err)
	}

	deleteResp, err := client.DeleteSandboxes(context.Background(), &agboxv1.DeleteSandboxesRequest{
		LabelSelector: map[string]string{"team": "a"},
	})
	if err != nil {
		t.Fatalf("DeleteSandboxes failed: %v", err)
	}
	if got := deleteResp.GetDeletedSandboxIds(); !reflect.DeepEqual(got, []string{"delete-team-a-1", "delete-team-a-2"}) {
		t.Fatalf("unexpected deleted sandbox ids: %#v", got)
	}
	if deleteResp.GetDeletedCount() != 2 {
		t.Fatalf("unexpected deleted count: %d", deleteResp.GetDeletedCount())
	}

	for _, sandboxID := range deleteResp.GetDeletedSandboxIds() {
		getResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
		if err != nil {
			t.Fatalf("GetSandbox(%s) failed: %v", sandboxID, err)
		}
		if getResp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_DELETING {
			t.Fatalf("expected sandbox %s to be deleting, got %s", sandboxID, getResp.GetSandbox().GetState())
		}
	}

	skipResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "delete-team-a-skip"})
	if err != nil {
		t.Fatalf("GetSandbox(skip) failed: %v", err)
	}
	if skipResp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_DELETING {
		t.Fatalf("expected skipped sandbox to stay deleting, got %s", skipResp.GetSandbox().GetState())
	}

	keepResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "delete-team-b"})
	if err != nil {
		t.Fatalf("GetSandbox(keep) failed: %v", err)
	}
	if keepResp.GetSandbox().GetState() != agboxv1.SandboxState_SANDBOX_STATE_READY {
		t.Fatalf("expected non-matching sandbox to stay ready, got %s", keepResp.GetSandbox().GetState())
	}

	for _, sandboxID := range deleteResp.GetDeletedSandboxIds() {
		waitForSandboxState(t, client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_DELETED)
	}
}

func TestDeleteSandboxesEmptySelector(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	_, err := client.DeleteSandboxes(context.Background(), &agboxv1.DeleteSandboxesRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestCreateSandboxRejectsConflictingGenericTargets(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "session-conflict",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			Mounts: []*agboxv1.MountSpec{
				{Source: "/tmp/a", Target: "/workspace/shared"},
			},
			Copies: []*agboxv1.CopySpec{
				{Source: "/tmp/b", Target: "/workspace/shared"},
			},
		},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}

func TestCreateSandboxRejectsInvalidGenericSourcesBeforeRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	testCases := []struct {
		name       string
		createSpec *agboxv1.CreateSpec
	}{
		{
			name: "missing_mount_source_path",
			createSpec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				Mounts: []*agboxv1.MountSpec{
					{Source: filepath.Join(t.TempDir(), "missing"), Target: "/workspace/mount"},
				},
			},
		},
		{
			name: "missing_copy_source_path",
			createSpec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				Copies: []*agboxv1.CopySpec{
					{Source: filepath.Join(t.TempDir(), "missing"), Target: "/workspace/copy"},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId:  "session-" + testCase.name,
				CreateSpec: testCase.createSpec,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
			if runtime.lastCreateSpec != nil {
				t.Fatalf("expected runtime backend to stay untouched, got %#v", runtime.lastCreateSpec)
			}
		})
	}
}

func TestCreateSandboxRejectsUnknownBuiltinResourcesBeforeRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "session-unknown-builtin",
		CreateSpec: &agboxv1.CreateSpec{
			Image:            "ghcr.io/agents-sandbox/coding-runtime:test",
			BuiltinResources: []string{"missing-builtin"},
		},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
	if runtime.lastCreateSpec != nil {
		t.Fatalf("expected runtime backend to stay untouched, got %#v", runtime.lastCreateSpec)
	}
}

func TestCreateSandboxRejectsInvalidServiceSpecsBeforeRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	testCases := []struct {
		name            string
		required        []*agboxv1.ServiceSpec
		optional        []*agboxv1.ServiceSpec
		expectedErrPart string
	}{
		{
			name: "empty_service_name",
			required: []*agboxv1.ServiceSpec{
				{Name: "", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}}},
			},
			expectedErrPart: "service name is required",
		},
		{
			name: "required_missing_healthcheck",
			required: []*agboxv1.ServiceSpec{
				{Name: "postgres", Image: "postgres:16"},
			},
			expectedErrPart: "must define healthcheck",
		},
		{
			name: "duplicate_service_name",
			required: []*agboxv1.ServiceSpec{
				{Name: "postgres", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}}},
			},
			optional: []*agboxv1.ServiceSpec{
				{Name: "postgres", Image: "postgres:17"},
			},
			expectedErrPart: "duplicate service name",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime.lastCreateSpec = nil
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId: "session-" + testCase.name,
				CreateSpec: &agboxv1.CreateSpec{
					Image:            "ghcr.io/agents-sandbox/coding-runtime:test",
					RequiredServices: testCase.required,
					OptionalServices: testCase.optional,
				},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
			if testCase.expectedErrPart != "" && !strings.Contains(err.Error(), testCase.expectedErrPart) {
				t.Fatalf("expected error to contain %q, got %v", testCase.expectedErrPart, err)
			}
			if runtime.lastCreateSpec != nil {
				t.Fatalf("expected runtime backend to stay untouched, got %#v", runtime.lastCreateSpec)
			}
		})
	}
}

func TestExecStatusCarriesStdoutAndStderr(t *testing.T) {
	runtime := &capturingRuntimeBackend{
		execResult: runtimeExecResult{
			ExitCode: 0,
			Stdout:   "hello",
			Stderr:   "warning",
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-exec-output", "ghcr.io/agents-sandbox/coding-runtime:test"))
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

	execStatus, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execResp.GetExecId()})
	if err != nil {
		t.Fatalf("GetExec failed: %v", err)
	}
	if execStatus.GetExec().GetStdout() != "hello" || execStatus.GetExec().GetStderr() != "warning" {
		t.Fatalf("unexpected exec output payload: %#v", execStatus.GetExec())
	}
}

func TestMaterializeGenericCopiesRejectsExternalSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(filepath.Join(externalRoot, "secret.txt"), filepath.Join(sourceRoot, "leak.txt")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	backend := &dockerRuntimeBackend{config: ServiceConfig{StateRoot: t.TempDir()}}
	state := &sandboxRuntimeState{}
	_, err := backend.materializeGenericCopies("sandbox-1", []*agboxv1.CopySpec{
		{Source: sourceRoot, Target: "/workspace/project"},
	}, state)
	if err == nil || !strings.Contains(err.Error(), "external symlink") {
		t.Fatalf("expected external symlink failure, got %v", err)
	}
}

func TestMaterializeGenericCopiesAppliesExcludePatterns(t *testing.T) {
	sourceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, ".git"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	backend := &dockerRuntimeBackend{config: ServiceConfig{StateRoot: t.TempDir()}}
	state := &sandboxRuntimeState{}
	mounts, err := backend.materializeGenericCopies("sandbox-1", []*agboxv1.CopySpec{
		{Source: sourceRoot, Target: "/workspace/project", ExcludePatterns: []string{".git"}},
	}, state)
	if err != nil {
		t.Fatalf("materializeGenericCopies failed: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected one mount, got %d", len(mounts))
	}
	if _, err := os.Stat(filepath.Join(mounts[0].Source, "keep.txt")); err != nil {
		t.Fatalf("expected keep.txt to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mounts[0].Source, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file to be absent, got %v", err)
	}
}

func TestSubscribeSandboxEventsReplayFromZeroCursor(t *testing.T) {
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
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("ResumeSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	replay, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:  createResp.GetSandboxId(),
		FromCursor: "0",
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
		SandboxId:  createResp.GetSandboxId(),
		FromCursor: fullHistory[2].GetCursor(),
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

func TestStaleCursorReturnsExpiredError(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("stale-cursor", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:  createResp.GetSandboxId(),
		FromCursor: createResp.GetSandboxId() + ":99",
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected stale cursor error")
	}
	assertStatusErrorReason(t, err, codes.OutOfRange, ReasonSandboxEventCursorStale)
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
		resp, getErr := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
		if getErr != nil {
			t.Fatalf("GetSandbox failed: %v", getErr)
		}
		if resp.GetSandbox().GetState() == agboxv1.SandboxState_SANDBOX_STATE_PENDING {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	t.Fatalf("expected sandbox to remain pending, got %s", resp.GetSandbox().GetState())
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
	if cancelEvent.GetEventType() != agboxv1.EventType_EXEC_CANCELLED || cancelEvent.GetExecId() != execResp.GetExecId() {
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
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId: createResp.GetSandboxId(),
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{
		SandboxId: createResp.GetSandboxId(),
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
	if stopEvents[len(stopEvents)-1].GetReason() != "stop_requested" {
		t.Fatalf("unexpected stop reason: %#v", stopEvents[len(stopEvents)-1])
	}

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{
		SandboxId: createResp.GetSandboxId(),
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
	if deleteEvents[len(deleteEvents)-1].GetReason() != "delete_requested" {
		t.Fatalf("unexpected delete reason: %#v", deleteEvents[len(deleteEvents)-1])
	}
}

func TestIdleTTLStopsReadySandboxAfterTerminalExec(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		IdleTTL:         20 * time.Millisecond,
		Version:         "test",
		DaemonName:      "agboxd-test",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-idle-ttl", "ghcr.io/agents-sandbox/coding-runtime:test"))
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
	if lastEvent.GetReason() != "idle_ttl" {
		t.Fatalf("unexpected idle-stop reason: %#v", lastEvent)
	}
}

func newBufconnClient(t *testing.T, config ServiceConfig) agboxv1.SandboxServiceClient {
	t.Helper()
	if config.runtimeBackend == nil {
		config.runtimeBackend = fakeRuntimeBackend{}
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	service, closer, err := NewService(config)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if closer != nil {
		t.Cleanup(func() {
			if closeErr := closer.Close(); closeErr != nil {
				t.Fatalf("service closer failed: %v", closeErr)
			}
		})
	}
	agboxv1.RegisterSandboxServiceServer(server, service)
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

func assertStatusCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	got := status.Code(err)
	if got != want {
		t.Fatalf("unexpected gRPC code: got %s want %s", got, want)
	}
}

func assertStatusErrorReason(t *testing.T, err error, wantCode codes.Code, wantReason string) {
	t.Helper()

	assertStatusCode(t, err, wantCode)
	st := status.Convert(err)
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetReason() == wantReason {
			return
		}
	}
	t.Fatalf("expected reason %q in error details, got %#v", wantReason, st.Details())
}

type recordingCloser struct {
	name  string
	order *[]string
	err   error
}

func (closer recordingCloser) Close() error {
	*closer.order = append(*closer.order, closer.name)
	return closer.err
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

type capturingRuntimeBackend struct {
	lastCreateImage string
	lastCreateSpec  *agboxv1.CreateSpec
	execResult      runtimeExecResult
}

func (backend *capturingRuntimeBackend) CreateSandbox(_ context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	backend.lastCreateImage = record.createSpec.GetImage()
	backend.lastCreateSpec = cloneCreateSpec(record.createSpec)
	return fakeRuntimeBackend{}.CreateSandbox(context.Background(), record)
}

func (backend *capturingRuntimeBackend) ResumeSandbox(context.Context, *sandboxRecord) (runtimeResumeResult, error) {
	return runtimeResumeResult{
		ServiceStatuses: []runtimeServiceStatus{
			{Name: "default", Required: true, Ready: true},
		},
	}, nil
}

func (*capturingRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error {
	return nil
}

func (*capturingRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error {
	return nil
}

func (backend *capturingRuntimeBackend) RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error) {
	if backend.execResult == (runtimeExecResult{}) {
		return runtimeExecResult{ExitCode: 0}, nil
	}
	return backend.execResult, nil
}

func createSandboxRequest(sandboxID string, image string) *agboxv1.CreateSandboxRequest {
	return &agboxv1.CreateSandboxRequest{
		SandboxId:  sandboxID,
		CreateSpec: createSpecWithImage(image),
	}
}

func createSpecWithImage(image string) *agboxv1.CreateSpec {
	return &agboxv1.CreateSpec{Image: image}
}

func sandboxIDs(handles []*agboxv1.SandboxHandle) []string {
	ids := make([]string, 0, len(handles))
	for _, handle := range handles {
		ids = append(ids, handle.GetSandboxId())
	}
	return ids
}

func assertErrorReason(t *testing.T, err error, want string) {
	t.Helper()

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetReason() == want {
			return
		}
	}
	t.Fatalf("expected reason %q in error details, got %v", want, st.Details())
}
