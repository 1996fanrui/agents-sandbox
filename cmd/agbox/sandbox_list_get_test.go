package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

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
					SandboxId:           "sandbox-123",
					State:               agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence:   7,
					Labels:              map[string]string{"env": "dev"},
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
