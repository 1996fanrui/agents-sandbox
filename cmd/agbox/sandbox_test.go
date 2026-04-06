package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc"
)

type fakeSandboxService struct {
	agboxv1.UnimplementedSandboxServiceServer

	createFn               func(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	listFn                 func(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	getFn                  func(context.Context, *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error)
	deleteFn               func(context.Context, *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error)
	deleteManyFn           func(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
	createExecFn           func(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	cancelExecFn           func(context.Context, *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error)
	getExecFn              func(context.Context, *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error)
	stopFn                 func(context.Context, *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error)
	resumeFn               func(context.Context, *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error)
	listActiveExecsFn      func(context.Context, *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error)
	subscribeFn            func(*agboxv1.SubscribeSandboxEventsRequest, grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error
	createReq              *agboxv1.CreateSandboxRequest
	listReq                *agboxv1.ListSandboxesRequest
	getReq                 *agboxv1.GetSandboxRequest
	deleteReq              *agboxv1.DeleteSandboxRequest
	deleteManyReq          *agboxv1.DeleteSandboxesRequest
	createExecReq          *agboxv1.CreateExecRequest
	cancelExecReq          *agboxv1.CancelExecRequest
	getExecReq             *agboxv1.GetExecRequest
	stopReq                *agboxv1.StopSandboxRequest
	resumeReq              *agboxv1.ResumeSandboxRequest
	listActiveExecsReq     *agboxv1.ListActiveExecsRequest
	subscribeReq           *agboxv1.SubscribeSandboxEventsRequest
	subscribeEventsPayload []*agboxv1.SandboxEvent
	subscribeErr           error
}

func (f *fakeSandboxService) CreateSandbox(ctx context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	f.createReq = request
	if f.createFn != nil {
		return f.createFn(ctx, request)
	}
	return &agboxv1.CreateSandboxResponse{}, nil
}

func (f *fakeSandboxService) ListSandboxes(ctx context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	f.listReq = request
	if f.listFn != nil {
		return f.listFn(ctx, request)
	}
	return &agboxv1.ListSandboxesResponse{}, nil
}

func (f *fakeSandboxService) GetSandbox(ctx context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
	f.getReq = request
	if f.getFn != nil {
		return f.getFn(ctx, request)
	}
	return &agboxv1.GetSandboxResponse{}, nil
}

func (f *fakeSandboxService) DeleteSandbox(ctx context.Context, request *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	f.deleteReq = request
	if f.deleteFn != nil {
		return f.deleteFn(ctx, request)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (f *fakeSandboxService) DeleteSandboxes(ctx context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	f.deleteManyReq = request
	if f.deleteManyFn != nil {
		return f.deleteManyFn(ctx, request)
	}
	return &agboxv1.DeleteSandboxesResponse{}, nil
}

func (f *fakeSandboxService) CreateExec(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	f.createExecReq = request
	if f.createExecFn != nil {
		return f.createExecFn(ctx, request)
	}
	return &agboxv1.CreateExecResponse{}, nil
}

func (f *fakeSandboxService) CancelExec(ctx context.Context, request *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
	f.cancelExecReq = request
	if f.cancelExecFn != nil {
		return f.cancelExecFn(ctx, request)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (f *fakeSandboxService) GetExec(ctx context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
	f.getExecReq = request
	if f.getExecFn != nil {
		return f.getExecFn(ctx, request)
	}
	return &agboxv1.GetExecResponse{}, nil
}

func (f *fakeSandboxService) SubscribeSandboxEvents(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
	f.subscribeReq = request
	if f.subscribeFn != nil {
		return f.subscribeFn(request, stream)
	}
	for _, event := range f.subscribeEventsPayload {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return f.subscribeErr
}

func (f *fakeSandboxService) StopSandbox(ctx context.Context, request *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	f.stopReq = request
	if f.stopFn != nil {
		return f.stopFn(ctx, request)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (f *fakeSandboxService) ResumeSandbox(ctx context.Context, request *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	f.resumeReq = request
	if f.resumeFn != nil {
		return f.resumeFn(ctx, request)
	}
	return &agboxv1.AcceptedResponse{Accepted: true}, nil
}

func (f *fakeSandboxService) ListActiveExecs(ctx context.Context, request *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
	f.listActiveExecsReq = request
	if f.listActiveExecsFn != nil {
		return f.listActiveExecsFn(ctx, request)
	}
	return &agboxv1.ListActiveExecsResponse{}, nil
}

func TestSandboxCreate(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if request.GetCreateSpec().GetImage() != "ubuntu:latest" {
				t.Fatalf("unexpected image: %q", request.GetCreateSpec().GetImage())
			}
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 2,
				},
			}, nil
		},
		subscribeEventsPayload: []*agboxv1.SandboxEvent{
			{SandboxId: "sandbox-123", Sequence: 2, SandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY},
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--image", "ubuntu:latest")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "Waiting for sandbox sandbox-123 to be ready...") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Sandbox ready in") {
		t.Fatalf("expected ready message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "docker exec") {
		t.Fatalf("expected docker exec tip in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "agbox sandbox delete sandbox-123") {
		t.Fatalf("expected delete tip in stderr, got %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if service.createReq.GetCreateSpec().GetImage() != "ubuntu:latest" {
		t.Fatalf("create request mismatch: %#v", service.createReq)
	}
}

func TestSandboxCreateWithLabels(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			labels := request.GetCreateSpec().GetLabels()
			if labels["team"] != "platform" || labels["env"] != "dev" {
				t.Fatalf("unexpected labels: %#v", labels)
			}
			return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sandbox-123", LastEventSequence: 1}}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{SandboxId: "sandbox-123", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 2},
			}, nil
		},
		subscribeEventsPayload: []*agboxv1.SandboxEvent{
			{SandboxId: "sandbox-123", Sequence: 2, SandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY},
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(
		t,
		service,
		"sandbox",
		"create",
		"--image", "ubuntu:latest",
		"--label", "team=backend",
		"--label", "env=dev",
		"--label", "team=platform",
	)
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if got := service.createReq.GetCreateSpec().GetLabels()["team"]; got != "platform" {
		t.Fatalf("duplicate label should be overwritten, got %q", got)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
}

func TestSandboxCreateJSON(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if request.GetCreateSpec().GetImage() != "ubuntu:latest" {
				t.Fatalf("unexpected image: %q", request.GetCreateSpec().GetImage())
			}
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId: "sandbox-123",
					State:     agboxv1.SandboxState_SANDBOX_STATE_PENDING,
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--image", "ubuntu:latest", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "\"sandbox\"") || !strings.Contains(stdout, "\"sandbox_id\"") {
		t.Fatalf("JSON is not pretty-printed with proto names: %q", stdout)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	sandbox, ok := payload["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("expected sandbox key in JSON: %#v", payload)
	}
	if sandbox["sandbox_id"] != "sandbox-123" {
		t.Fatalf("unexpected sandbox_id: %#v", sandbox["sandbox_id"])
	}
	if sandbox["state"] != "SANDBOX_STATE_PENDING" {
		t.Fatalf("unexpected state: %#v", sandbox["state"])
	}
}

func TestSandboxCreateDefaultImage(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-default",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{SandboxId: "sandbox-default", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 2},
			}, nil
		},
		subscribeEventsPayload: []*agboxv1.SandboxEvent{
			{SandboxId: "sandbox-default", Sequence: 2, SandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY},
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if service.createReq.GetCreateSpec().GetImage() != defaultImage {
		t.Fatalf("expected default image %q, got %q", defaultImage, service.createReq.GetCreateSpec().GetImage())
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
}

func TestSandboxCreateBadLabel(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "create", "--image", "ubuntu:latest", "--label", "badlabel")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "--label") || !strings.Contains(stderr, "=") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxCreateIdleTTL(t *testing.T) {
	t.Parallel()

	t.Run("five_minutes", func(t *testing.T) {
		t.Parallel()
		service := &fakeSandboxService{
			createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
				return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sb-1", LastEventSequence: 1}}, nil
			},
		}
		withCreateWaitMocks(service, "sb-1")
		_, _, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--image", "ubuntu:latest", "--idle-ttl", "5m")
		if exitCode != exitCodeSuccess {
			t.Fatalf("unexpected exit code %d", exitCode)
		}
		got := service.createReq.GetCreateSpec().GetIdleTtl()
		if got == nil {
			t.Fatal("expected idle_ttl to be set")
		}
		if got.GetSeconds() != 300 || got.GetNanos() != 0 {
			t.Fatalf("unexpected idle_ttl: %v", got)
		}
	})

	t.Run("zero_disables", func(t *testing.T) {
		t.Parallel()
		service := &fakeSandboxService{
			createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
				return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sb-2", LastEventSequence: 1}}, nil
			},
		}
		withCreateWaitMocks(service, "sb-2")
		_, _, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--image", "ubuntu:latest", "--idle-ttl", "0")
		if exitCode != exitCodeSuccess {
			t.Fatalf("unexpected exit code %d", exitCode)
		}
		got := service.createReq.GetCreateSpec().GetIdleTtl()
		if got == nil {
			t.Fatal("expected idle_ttl to be set")
		}
		if got.GetSeconds() != 0 || got.GetNanos() != 0 {
			t.Fatalf("unexpected idle_ttl: %v", got)
		}
	})

	t.Run("no_flag_leaves_nil", func(t *testing.T) {
		t.Parallel()
		service := &fakeSandboxService{
			createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
				return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sb-3", LastEventSequence: 1}}, nil
			},
		}
		withCreateWaitMocks(service, "sb-3")
		_, _, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--image", "ubuntu:latest")
		if exitCode != exitCodeSuccess {
			t.Fatalf("unexpected exit code %d", exitCode)
		}
		if got := service.createReq.GetCreateSpec().GetIdleTtl(); got != nil {
			t.Fatalf("expected idle_ttl nil, got %v", got)
		}
	})

	t.Run("invalid_value", func(t *testing.T) {
		t.Parallel()
		_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "create", "--image", "ubuntu:latest", "--idle-ttl", "notaduration")
		if exitCode != exitCodeUsageError {
			t.Fatalf("unexpected exit code %d", exitCode)
		}
		if !strings.Contains(stderr, "--idle-ttl") {
			t.Fatalf("unexpected stderr %q", stderr)
		}
	})
}

func TestSandboxCreateIdleTTLRejectsNegative(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "create", "--image", "ubuntu:latest", "--idle-ttl", "-1s")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "negative") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

// withCreateWaitMocks adds getFn and subscribeEventsPayload to a fake service
// so that sandbox create can wait for the READY state. The sandboxID must match
// what createFn returns.
func withCreateWaitMocks(service *fakeSandboxService, sandboxID string) {
	if service.getFn == nil {
		service.getFn = func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{SandboxId: sandboxID, State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 2},
			}, nil
		}
	}
	if service.subscribeEventsPayload == nil {
		service.subscribeEventsPayload = []*agboxv1.SandboxEvent{
			{SandboxId: sandboxID, Sequence: 2, SandboxState: agboxv1.SandboxState_SANDBOX_STATE_READY},
		}
	}
}

func TestSandboxCreateWithYAMLFile(t *testing.T) {
	yamlContent := []byte("runtime:\n  image: my-image:latest\n")
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, yamlContent, 0o644); err != nil {
		t.Fatal(err)
	}

	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-yaml",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	withCreateWaitMocks(service, "sandbox-yaml")

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--yaml-file", yamlPath)
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if string(service.createReq.GetConfigYaml()) != string(yamlContent) {
		t.Fatalf("expected config yaml %q, got %q", yamlContent, service.createReq.GetConfigYaml())
	}
	if service.createReq.GetCreateSpec().GetImage() != "" {
		t.Fatalf("expected empty image when --yaml-file is used without --image, got %q", service.createReq.GetCreateSpec().GetImage())
	}
}

func TestSandboxCreateWithYAMLFileAndImageOverride(t *testing.T) {
	yamlContent := []byte("runtime:\n  image: default-image:latest\n")
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, yamlContent, 0o644); err != nil {
		t.Fatal(err)
	}

	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-yaml-img",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	withCreateWaitMocks(service, "sandbox-yaml-img")

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--yaml-file", yamlPath, "--image", "custom:latest")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if string(service.createReq.GetConfigYaml()) != string(yamlContent) {
		t.Fatalf("expected config yaml %q, got %q", yamlContent, service.createReq.GetConfigYaml())
	}
	if service.createReq.GetCreateSpec().GetImage() != "custom:latest" {
		t.Fatalf("expected image %q, got %q", "custom:latest", service.createReq.GetCreateSpec().GetImage())
	}
}

func TestSandboxCreateYAMLFileNotFound(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "create", "--yaml-file", "/nonexistent/path.yaml")
	if exitCode != exitCodeRuntimeError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "/nonexistent/path.yaml") {
		t.Fatalf("expected file path in stderr, got %q", stderr)
	}
}

func TestSandboxCreateWithYAMLFileNoLabelNoIdleTTL(t *testing.T) {
	yamlContent := []byte("runtime:\n  idle_ttl: 30m\nlabels:\n  env: prod\n")
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(yamlPath, yamlContent, 0o644); err != nil {
		t.Fatal(err)
	}

	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-yaml-defaults",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	withCreateWaitMocks(service, "sandbox-yaml-defaults")

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--yaml-file", yamlPath)
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	// Without explicit --label, labels should be empty (not override YAML labels).
	if len(service.createReq.GetCreateSpec().GetLabels()) != 0 {
		t.Fatalf("expected empty labels when --label not set, got %v", service.createReq.GetCreateSpec().GetLabels())
	}
	// Without explicit --idle-ttl, IdleTtl should be nil (not override YAML idle_ttl).
	if service.createReq.GetCreateSpec().GetIdleTtl() != nil {
		t.Fatalf("expected nil idle_ttl when --idle-ttl not set, got %v", service.createReq.GetCreateSpec().GetIdleTtl())
	}
}

func TestSandboxCreateWithSandboxID(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "my-sandbox",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	withCreateWaitMocks(service, "my-sandbox")

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--sandbox-id", "my-sandbox")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if service.createReq.GetSandboxId() != "my-sandbox" {
		t.Fatalf("expected sandbox id %q, got %q", "my-sandbox", service.createReq.GetSandboxId())
	}
}

func TestSandboxCreateWithEmptySandboxID(t *testing.T) {
	service := &fakeSandboxService{
		createFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "auto-generated-id",
					State:             agboxv1.SandboxState_SANDBOX_STATE_PENDING,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	withCreateWaitMocks(service, "auto-generated-id")

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "create", "--sandbox-id", "")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if service.createReq.GetSandboxId() != "" {
		t.Fatalf("expected empty sandbox id, got %q", service.createReq.GetSandboxId())
	}
}

func runCLIWithSandboxServer(t *testing.T, service *fakeSandboxService, args ...string) (string, string, int) {
	t.Helper()

	_, lookupEnv := startSandboxTestServer(t, service)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), args, &stdout, &stderr, lookupEnv)
	return stdout.String(), stderr.String(), exitCode
}
