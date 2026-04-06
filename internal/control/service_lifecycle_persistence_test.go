package control

import (
	"context"
	"errors"
	"log/slog"
	"os"
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
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
		if len(rawSandbox) != 8 {
			t.Fatalf("sandbox registry timestamp must be 8 bytes (int64), got %d bytes", len(rawSandbox))
		}
		sandboxNano := decodeInt64(rawSandbox)
		if sandboxNano <= 0 {
			t.Fatalf("sandbox registry timestamp must be positive UnixNano, got %d", sandboxNano)
		}
		if len(rawExec) != 8 {
			t.Fatalf("exec registry timestamp must be 8 bytes (int64), got %d bytes", len(rawExec))
		}
		execNano := decodeInt64(rawExec)
		if execNano <= 0 {
			t.Fatalf("exec registry timestamp must be positive UnixNano, got %d", execNano)
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
	waitForSandboxState(t, client2, secondSandbox.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	_, err = client2.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: secondSandbox.GetSandbox().GetSandboxId(),
		ExecId:    "persistent-exec",
		Command:   []string{"echo"},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists for exec id, got %v", err)
	}
	assertErrorReason(t, err, ReasonExecIDAlreadyExists)
}

func TestDeletedAtBucketName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	db, err := openBoltDB(dbPath)
	if err != nil {
		t.Fatalf("openBoltDB failed: %v", err)
	}
	defer db.Close()

	err = db.View(func(tx *bbolt.Tx) error {
		if tx.Bucket([]byte("sandbox-deleted-at")) == nil {
			t.Fatal("expected sandbox-deleted-at bucket to exist")
		}
		if tx.Bucket([]byte("event-meta")) != nil {
			t.Fatal("event-meta bucket should not exist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("db.View failed: %v", err)
	}
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
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	first.close()

	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
	}, dbPath)
	stream, err := second.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
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
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		CleanupTTL:      time.Hour,
	}, dbPath)
	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("retain-delete", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := harness.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	stream, err := harness.client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
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
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		CleanupTTL:      time.Millisecond,
	}, dbPath)
	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("cleanup-expired", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	if _, err := harness.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	if err := harness.service.config.eventStore.MarkDeleted(createResp.GetSandbox().GetSandboxId(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	if err := harness.service.cleanupExpiredEvents(); err != nil {
		t.Fatalf("cleanupExpiredEvents failed: %v", err)
	}

	_, err = harness.client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()})
	if err == nil {
		t.Fatal("expected sandbox to be removed after cleanup")
	}
	assertStatusErrorReason(t, err, codes.NotFound, ReasonSandboxNotFound)
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
	// Use a short temp path to stay under the macOS 104-char Unix socket limit.
	shortDir, err := os.MkdirTemp("/tmp", "agbox-dock-")
	if err != nil {
		t.Fatalf("mkdtemp failed: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(shortDir) })
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(shortDir, "docker.sock"))

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
	if first.GetSandbox().GetSandboxId() == second.GetSandbox().GetSandboxId() {
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

func TestSandboxConfigPersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "config-persist",
		CreateSpec: &agboxv1.CreateSpec{
			Image:  "ghcr.io/agents-sandbox/coding-runtime:test",
			Labels: map[string]string{"env": "test"},
			Envs:   map[string]string{"FOO": "bar"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Verify config can be loaded from the store
	loaded, err := first.service.config.eventStore.LoadSandboxConfig("config-persist")
	if err != nil {
		t.Fatalf("LoadSandboxConfig failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected sandbox config to be persisted")
	}
	if loaded.GetImage() != "ghcr.io/agents-sandbox/coding-runtime:test" {
		t.Fatalf("unexpected image: got %s", loaded.GetImage())
	}
	if loaded.GetLabels()["env"] != "test" {
		t.Fatalf("unexpected labels: got %v", loaded.GetLabels())
	}
	if len(loaded.GetEnvs()) != 1 || loaded.GetEnvs()["FOO"] != "bar" {
		t.Fatalf("unexpected envs: got %v", loaded.GetEnvs())
	}
	first.close()

	// Restart and verify config survives
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
	}, dbPath)
	loaded2, err := second.service.config.eventStore.LoadSandboxConfig("config-persist")
	if err != nil {
		t.Fatalf("LoadSandboxConfig after restart failed: %v", err)
	}
	if loaded2 == nil {
		t.Fatal("expected sandbox config to survive restart")
	}
	if loaded2.GetImage() != "ghcr.io/agents-sandbox/coding-runtime:test" {
		t.Fatalf("unexpected image after restart: got %s", loaded2.GetImage())
	}

	// Verify LoadAllSandboxConfigs
	allConfigs, err := second.service.config.eventStore.LoadAllSandboxConfigs()
	if err != nil {
		t.Fatalf("LoadAllSandboxConfigs failed: %v", err)
	}
	if len(allConfigs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(allConfigs))
	}
	if _, ok := allConfigs["config-persist"]; !ok {
		t.Fatal("expected config-persist in LoadAllSandboxConfigs result")
	}
}

func TestExecConfigPersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	}, dbPath)
	createResp, err := first.client.CreateSandbox(context.Background(), createSandboxRequest("exec-config-persist", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, first.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := first.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId:    createResp.GetSandbox().GetSandboxId(),
		ExecId:       "exec-1",
		Command:      []string{"echo", "hello"},
		Cwd:          "/tmp",
		EnvOverrides: map[string]string{"BAR": "baz"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}

	// Verify exec config can be loaded
	configs, err := first.service.config.eventStore.LoadExecConfigs(createResp.GetSandbox().GetSandboxId())
	if err != nil {
		t.Fatalf("LoadExecConfigs failed: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 exec config, got %d", len(configs))
	}
	if configs[0].GetExecId() != execResp.GetExecId() {
		t.Fatalf("unexpected exec id: got %s want %s", configs[0].GetExecId(), execResp.GetExecId())
	}
	if configs[0].GetCwd() != "/tmp" {
		t.Fatalf("unexpected cwd: got %s", configs[0].GetCwd())
	}
	if len(configs[0].GetCommand()) != 2 || configs[0].GetCommand()[0] != "echo" {
		t.Fatalf("unexpected command: got %v", configs[0].GetCommand())
	}
	first.close()

	// Restart and verify exec config survives
	second := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		PollInterval: 2 * time.Millisecond,
	}, dbPath)
	configs2, err := second.service.config.eventStore.LoadExecConfigs(createResp.GetSandbox().GetSandboxId())
	if err != nil {
		t.Fatalf("LoadExecConfigs after restart failed: %v", err)
	}
	if len(configs2) != 1 {
		t.Fatalf("expected 1 exec config after restart, got %d", len(configs2))
	}
	if configs2[0].GetExecId() != execResp.GetExecId() {
		t.Fatalf("unexpected exec id after restart: got %s", configs2[0].GetExecId())
	}
}

func TestCleanupRemovesSandboxAndExecConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ids.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	harness := newPersistentBufconnHarness(t, ctx, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		CleanupTTL:      time.Millisecond,
	}, dbPath)
	createResp, err := harness.client.CreateSandbox(context.Background(), createSandboxRequest("cleanup-config", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := harness.client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "cleanup-exec",
		Command:   []string{"echo"},
	}); err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, harness.client, "cleanup-exec", agboxv1.ExecState_EXEC_STATE_FINISHED)

	// Delete and force cleanup
	if _, err := harness.client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, harness.client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)

	// Backdate deleted_at to force cleanup
	if err := harness.service.config.eventStore.MarkDeleted(createResp.GetSandbox().GetSandboxId(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	if err := harness.service.cleanupExpiredEvents(); err != nil {
		t.Fatalf("cleanupExpiredEvents failed: %v", err)
	}

	// Verify sandbox config is gone
	loaded, err := harness.service.config.eventStore.LoadSandboxConfig(createResp.GetSandbox().GetSandboxId())
	if err != nil {
		t.Fatalf("LoadSandboxConfig failed: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected sandbox config to be removed after cleanup")
	}

	// Verify exec configs are gone
	configs, err := harness.service.config.eventStore.LoadExecConfigs(createResp.GetSandbox().GetSandboxId())
	if err != nil {
		t.Fatalf("LoadExecConfigs failed: %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("expected exec configs to be removed after cleanup, got %d", len(configs))
	}
}
