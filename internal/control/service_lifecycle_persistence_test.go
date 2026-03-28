package control

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	bbolt "go.etcd.io/bbolt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	service, closer, err := NewService(ServiceConfig{Logger: slog.Default()})
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
