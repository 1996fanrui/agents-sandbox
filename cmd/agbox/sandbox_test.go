package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	subscribeFn            func(*agboxv1.SubscribeSandboxEventsRequest, grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error
	createReq              *agboxv1.CreateSandboxRequest
	listReq                *agboxv1.ListSandboxesRequest
	getReq                 *agboxv1.GetSandboxRequest
	deleteReq              *agboxv1.DeleteSandboxRequest
	deleteManyReq          *agboxv1.DeleteSandboxesRequest
	createExecReq          *agboxv1.CreateExecRequest
	cancelExecReq          *agboxv1.CancelExecRequest
	getExecReq             *agboxv1.GetExecRequest
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

func TestSandboxList(t *testing.T) {
	service := &fakeSandboxService{
		listFn: func(_ context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
			selector := request.GetLabelSelector()
			switch {
			case len(selector) == 0 && !request.GetIncludeDeleted():
				return &agboxv1.ListSandboxesResponse{
					Sandboxes: []*agboxv1.SandboxHandle{
						{SandboxId: "sandbox-a", State: agboxv1.SandboxState_SANDBOX_STATE_READY, Labels: map[string]string{"team": "backend", "env": "dev"}},
						{SandboxId: "sandbox-b", State: agboxv1.SandboxState_SANDBOX_STATE_PENDING, Labels: map[string]string{"team": "frontend", "env": "dev"}},
						{SandboxId: "sandbox-c", State: agboxv1.SandboxState_SANDBOX_STATE_STOPPED, Labels: map[string]string{"team": "backend", "env": "prod"}},
					},
				}, nil
			case len(selector) == 0 && request.GetIncludeDeleted():
				return &agboxv1.ListSandboxesResponse{
					Sandboxes: []*agboxv1.SandboxHandle{
						{SandboxId: "sandbox-a", State: agboxv1.SandboxState_SANDBOX_STATE_READY, Labels: map[string]string{"team": "backend", "env": "dev"}},
						{SandboxId: "sandbox-deleted", State: agboxv1.SandboxState_SANDBOX_STATE_DELETED, Labels: map[string]string{"team": "backend", "env": "archived"}},
					},
				}, nil
			case selector["env"] == "dev" && selector["team"] == "backend":
				return &agboxv1.ListSandboxesResponse{
					Sandboxes: []*agboxv1.SandboxHandle{
						{SandboxId: "sandbox-a", State: agboxv1.SandboxState_SANDBOX_STATE_READY, Labels: map[string]string{"team": "backend", "env": "dev"}},
					},
				}, nil
			case selector["env"] == "qa":
				return &agboxv1.ListSandboxesResponse{}, nil
			default:
				t.Fatalf("unexpected label selector: %#v", selector)
				return nil, nil
			}
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "list")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("unexpected output lines: %#v", lines)
	}
	if !strings.Contains(lines[0], "SANDBOX ID") || !strings.Contains(lines[0], "CREATED") || !strings.Contains(lines[0], "STATUS") || !strings.Contains(lines[0], "LABELS") || !strings.Contains(lines[0], "ERROR") {
		t.Fatalf("unexpected header line: %q", lines[0])
	}
	if !strings.Contains(stdout, "sandbox-a") || !strings.Contains(stdout, "sandbox-b") || !strings.Contains(stdout, "sandbox-c") {
		t.Fatalf("unexpected stdout %q", stdout)
	}
	if !strings.Contains(stdout, "env=dev,team=backend") {
		t.Fatalf("labels were not sorted in stdout %q", stdout)
	}

	stdout, stderr, exitCode = runCLIWithSandboxServer(t, service, "sandbox", "list", "--label", "env=dev", "--label", "team=backend")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "sandbox-a") || strings.Contains(stdout, "sandbox-b") || strings.Contains(stdout, "sandbox-c") {
		t.Fatalf("unexpected filtered stdout %q", stdout)
	}
	if got := service.listReq.GetLabelSelector(); got["env"] != "dev" || got["team"] != "backend" {
		t.Fatalf("unexpected list request selector: %#v", got)
	}

	stdout, stderr, exitCode = runCLIWithSandboxServer(t, service, "sandbox", "list", "--label", "env=qa")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	emptyLines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(emptyLines) != 1 {
		t.Fatalf("unexpected empty output: %#v", emptyLines)
	}
	if !strings.Contains(emptyLines[0], "SANDBOX ID") || !strings.Contains(emptyLines[0], "CREATED") || !strings.Contains(emptyLines[0], "STATUS") || !strings.Contains(emptyLines[0], "LABELS") || !strings.Contains(emptyLines[0], "ERROR") {
		t.Fatalf("unexpected header line: %q", emptyLines[0])
	}
}

func TestSandboxListJSON(t *testing.T) {
	service := &fakeSandboxService{
		listFn: func(_ context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
			if request.GetIncludeDeleted() {
				t.Fatal("include_deleted should be false")
			}
			return &agboxv1.ListSandboxesResponse{
				Sandboxes: []*agboxv1.SandboxHandle{
					{SandboxId: "sandbox-a", State: agboxv1.SandboxState_SANDBOX_STATE_READY},
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "list", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "\n  \"sandboxes\"") {
		t.Fatalf("JSON is not pretty-printed with proto names: %q", stdout)
	}
	var payload struct {
		Sandboxes []map[string]any `json:"sandboxes"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if len(payload.Sandboxes) != 1 {
		t.Fatalf("unexpected sandboxes payload: %#v", payload.Sandboxes)
	}

	stdout, stderr, exitCode = runCLIWithSandboxServer(t, &fakeSandboxService{
		listFn: func(_ context.Context, _ *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
			return &agboxv1.ListSandboxesResponse{}, nil
		},
	}, "sandbox", "list", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if len(payload.Sandboxes) != 0 {
		t.Fatalf("unexpected empty payload: %#v", payload.Sandboxes)
	}
}

func TestSandboxListIncludeDeleted(t *testing.T) {
	service := &fakeSandboxService{
		listFn: func(_ context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
			if request.GetIncludeDeleted() {
				return &agboxv1.ListSandboxesResponse{
					Sandboxes: []*agboxv1.SandboxHandle{
						{SandboxId: "sandbox-deleted", State: agboxv1.SandboxState_SANDBOX_STATE_DELETED},
					},
				}, nil
			}
			return &agboxv1.ListSandboxesResponse{
				Sandboxes: []*agboxv1.SandboxHandle{
					{SandboxId: "sandbox-ready", State: agboxv1.SandboxState_SANDBOX_STATE_READY},
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "list")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if strings.Contains(stdout, "sandbox-deleted") {
		t.Fatalf("deleted sandbox should not be listed by default: %q", stdout)
	}

	stdout, stderr, exitCode = runCLIWithSandboxServer(t, service, "sandbox", "list", "--include-deleted")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "sandbox-deleted") {
		t.Fatalf("unexpected stdout %q", stdout)
	}
	if !service.listReq.GetIncludeDeleted() {
		t.Fatal("include_deleted flag was not forwarded")
	}
}

func TestSandboxGet(t *testing.T) {
	service := &fakeSandboxService{
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			if request.GetSandboxId() != "sandbox-123" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 7,
					Labels:            map[string]string{"team": "backend", "env": "dev"},
					CompanionContainers: []*agboxv1.CompanionContainerSpec{
						{Name: "db", Image: "postgres:16"},
						{Name: "cache", Image: "redis:7"},
					},
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "get", "sandbox-123")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	want := []string{
		"sandbox_id=sandbox-123",
		"state=Ready",
		"image=",
		"created_at=",
		`labels={"env":"dev","team":"backend"}`,
	}
	if len(lines) != len(want) {
		t.Fatalf("unexpected line count: %v", lines)
	}
	for index, line := range lines {
		if !strings.HasPrefix(line, want[index]) {
			t.Fatalf("unexpected line %d: got %q want prefix %q", index, line, want[index])
		}
	}

	service = &fakeSandboxService{
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			if request.GetSandboxId() != "sandbox-empty" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId: "sandbox-empty",
					State:     agboxv1.SandboxState_SANDBOX_STATE_PENDING,
				},
			}, nil
		},
	}
	stdout, stderr, exitCode = runCLIWithSandboxServer(t, service, "sandbox", "get", "sandbox-empty")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	lines = strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	want = []string{
		"sandbox_id=sandbox-empty",
		"state=Pending",
		"image=",
		"created_at=",
	}
	if len(lines) != len(want) {
		t.Fatalf("unexpected line count: %v", lines)
	}
	for index, line := range lines {
		if !strings.HasPrefix(line, want[index]) {
			t.Fatalf("unexpected line %d: got %q want prefix %q", index, line, want[index])
		}
	}
}

func TestSandboxGetFailed(t *testing.T) {
	service := &fakeSandboxService{
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:    "sandbox-fail",
					State:        agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					ErrorCode:    "CONTAINER_NOT_RUNNING",
					ErrorMessage: "primary container not running",
				},
			}, nil
		},
	}
	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "get", "sandbox-fail")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "error_code=CONTAINER_NOT_RUNNING") {
		t.Fatalf("expected error_code in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "error_message=primary container not running") {
		t.Fatalf("expected error_message in output, got %q", stdout)
	}
}

func TestSandboxGetJSON(t *testing.T) {
	service := &fakeSandboxService{
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			if request.GetSandboxId() != "sandbox-123" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 7,
					Labels:            map[string]string{"env": "dev"},
					CompanionContainers: []*agboxv1.CompanionContainerSpec{},
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "get", "sandbox-123", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "\n  \"sandbox\": {") {
		t.Fatalf("JSON is not pretty-printed with proto names: %q", stdout)
	}
	var payload struct {
		Sandbox map[string]any `json:"sandbox"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	for _, key := range []string{"sandbox_id", "state", "last_event_sequence", "labels", "companion_containers"} {
		if _, ok := payload.Sandbox[key]; !ok {
			t.Fatalf("missing field %q in JSON: %#v", key, payload.Sandbox)
		}
	}
	if payload.Sandbox["sandbox_id"] != "sandbox-123" {
		t.Fatalf("unexpected sandbox_id: %#v", payload.Sandbox["sandbox_id"])
	}
	if payload.Sandbox["state"] != "SANDBOX_STATE_READY" {
		t.Fatalf("unexpected state: %#v", payload.Sandbox["state"])
	}
	if payload.Sandbox["last_event_sequence"] != "7" {
		t.Fatalf("unexpected last_event_sequence: %#v", payload.Sandbox["last_event_sequence"])
	}
}

func TestSandboxGetMissingSandboxID(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "get")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "accepts 1 arg(s)") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxGetRejectsUnknownFlag(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "get", "--unknown")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "unknown flag: --unknown") {
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

func runCLIWithSandboxServer(t *testing.T, service *fakeSandboxService, args ...string) (string, string, int) {
	t.Helper()

	_, lookupEnv := startSandboxTestServer(t, service)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), args, &stdout, &stderr, lookupEnv)
	return stdout.String(), stderr.String(), exitCode
}
