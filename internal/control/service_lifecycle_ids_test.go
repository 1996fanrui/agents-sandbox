package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/google/uuid"
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
			CompanionContainers: []*agboxv1.CompanionContainerSpec{
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
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
		agboxv1.EventType_COMPANION_CONTAINER_READY,
		agboxv1.EventType_SANDBOX_READY,
	}
	for index, eventType := range wantLifecycle {
		if events[index].GetEventType() != eventType {
			t.Fatalf("unexpected lifecycle event at %d: got %s want %s", index, events[index].GetEventType(), eventType)
		}
	}

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	if startEvent == nil || eventExecID(startEvent) != execResp.GetExecId() {
		t.Fatalf("missing exec started event: %#v", startEvent)
	}
	if last.GetEventType() != agboxv1.EventType_EXEC_FINISHED || eventExecID(last) != execResp.GetExecId() || eventExitCode(last) != 0 {
		t.Fatalf("unexpected exec terminal event: %#v", last)
	}
}

func TestConfiguredArtifactOutputPathIsCreatedForExecs(t *testing.T) {
	artifactRoot := t.TempDir()
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay:        5 * time.Millisecond,
		PollInterval:           2 * time.Millisecond,
		ArtifactOutputRoot:     artifactRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("workspace-1", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	// Daemon returns separate paths for stdout and stderr; files are written by the container, not pre-created.
	if execResp.GetStdoutLogPath() == "" {
		t.Fatal("expected non-empty stdout_log_path")
	}
	if execResp.GetStderrLogPath() == "" {
		t.Fatal("expected non-empty stderr_log_path")
	}
	if !strings.HasSuffix(execResp.GetStdoutLogPath(), ".stdout.log") {
		t.Fatalf("expected stdout path ending with .stdout.log, got %q", execResp.GetStdoutLogPath())
	}
	if !strings.HasSuffix(execResp.GetStderrLogPath(), ".stderr.log") {
		t.Fatalf("expected stderr path ending with .stderr.log, got %q", execResp.GetStderrLogPath())
	}
	// The parent directory is created by the daemon; actual log files are written by the container process.
	parentDir := filepath.Dir(execResp.GetStdoutLogPath())
	if _, err := os.Stat(parentDir); err != nil {
		t.Fatalf("expected parent directory to exist: %v", err)
	}
	if _, err := os.Stat(execResp.GetStdoutLogPath()); !os.IsNotExist(err) {
		t.Fatalf("stdout file should not be pre-created by daemon, got err=%v", err)
	}
}

func TestCreateSandboxCreatesExecLogDirectory(t *testing.T) {
	artifactRoot := t.TempDir()
	backend := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay:        5 * time.Millisecond,
		PollInterval:           2 * time.Millisecond,
		runtimeBackend:         backend,
		ArtifactOutputRoot:     artifactRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}",
	})

	_, err := client.CreateSandbox(context.Background(), createSandboxRequest("exec-log-mount", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, "exec-log-mount", agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Exec log host directory must be created during CreateSandbox so the bind-mount succeeds.
	execLogDir := filepath.Join(artifactRoot, "exec-log-mount")
	info, err := os.Stat(execLogDir)
	if err != nil {
		t.Fatalf("exec log directory should exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got file at %q", execLogDir)
	}
}

func TestCreateExecResponseIncludesLogPaths(t *testing.T) {
	artifactRoot := t.TempDir()
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay:        5 * time.Millisecond,
		PollInterval:           2 * time.Millisecond,
		ArtifactOutputRoot:     artifactRoot,
		ArtifactOutputTemplate: "{sandbox_id}/{exec_id}",
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("resp-log-paths", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo", "hello"},
		ExecId:    "exec-resp-test",
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if execResp.GetStdoutLogPath() == "" {
		t.Fatal("expected non-empty stdout_log_path")
	}
	if execResp.GetStderrLogPath() == "" {
		t.Fatal("expected non-empty stderr_log_path")
	}
	if !strings.HasSuffix(execResp.GetStdoutLogPath(), ".stdout.log") {
		t.Fatalf("expected stdout path ending with .stdout.log, got %q", execResp.GetStdoutLogPath())
	}
	if !strings.HasSuffix(execResp.GetStderrLogPath(), ".stderr.log") {
		t.Fatalf("expected stderr path ending with .stderr.log, got %q", execResp.GetStderrLogPath())
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
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo"},
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected not-ready error, got %v", err)
	}

	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	if createResp.GetSandbox().GetSandboxId() != "issue11-sandbox" {
		t.Fatalf("unexpected sandbox id: %q", createResp.GetSandbox().GetSandboxId())
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	eventCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.SubscribeSandboxEvents(eventCtx, &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:              createResp.GetSandbox().GetSandboxId(),
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
	if _, err := uuid.Parse(createResp.GetSandbox().GetSandboxId()); err != nil {
		t.Fatalf("expected UUIDv4 sandbox id, got %q: %v", createResp.GetSandbox().GetSandboxId(), err)
	}
	if _, ok := registry.sandboxIDs[createResp.GetSandbox().GetSandboxId()]; !ok {
		t.Fatalf("sandbox id %q was not recorded in registry", createResp.GetSandbox().GetSandboxId())
	}
	if err := registry.ReserveSandboxID(createResp.GetSandbox().GetSandboxId(), time.Now().UTC()); !errors.Is(err, errSandboxIDAlreadyExists) {
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
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
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
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	for _, execID := range []string{"-myexec", "ab", "exec/"} {
		t.Run("invalid-"+execID, func(t *testing.T) {
			_, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
				SandboxId: createResp.GetSandbox().GetSandboxId(),
				ExecId:    execID,
				Command:   []string{"echo"},
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected invalid argument, got %v", err)
			}
		})
	}

	if _, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "dup-exec",
		Command:   []string{"echo"},
	}); err != nil {
		t.Fatalf("CreateExec(first) failed: %v", err)
	}
	_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		ExecId:    "dup-exec",
		Command:   []string{"echo"},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected already exists, got %v", err)
	}
	assertErrorReason(t, err, ReasonExecIDAlreadyExists)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo", "uuid"},
	})
	if err != nil {
		t.Fatalf("CreateExec(generated) failed: %v", err)
	}
	if _, err := uuid.Parse(execResp.GetExecId()); err != nil {
		t.Fatalf("expected UUID exec id, got %q: %v", execResp.GetExecId(), err)
	}
}
