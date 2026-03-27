package control

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	if got := execStatus.GetExec().GetLastEventSequence(); got == 0 {
		t.Fatal("expected non-zero exec last_event_sequence")
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
