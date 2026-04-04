package main

import (
	"context"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestSandboxDelete(t *testing.T) {
	service := &fakeSandboxService{
		deleteFn: func(_ context.Context, request *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error) {
			if request.GetSandboxId() != "sandbox-123" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{SandboxId: "sandbox-123", State: agboxv1.SandboxState_SANDBOX_STATE_DELETED, LastEventSequence: 3},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "delete", "sandbox-123")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "Waiting for sandbox sandbox-123 to be deleted") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Sandbox deleted in") {
		t.Fatalf("expected deleted-with-duration message in stderr, got %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
}

func TestSandboxDeleteByLabel(t *testing.T) {
	service := &fakeSandboxService{
		deleteManyFn: func(_ context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
			selector := request.GetLabelSelector()
			if selector["team"] != "a" || selector["env"] != "dev" {
				t.Fatalf("unexpected selector: %#v", selector)
			}
			return &agboxv1.DeleteSandboxesResponse{
				DeletedCount:      2,
				DeletedSandboxIds: []string{"sandbox-a", "sandbox-d"},
			}, nil
		},
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{SandboxId: request.GetSandboxId(), State: agboxv1.SandboxState_SANDBOX_STATE_DELETED, LastEventSequence: 3},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "delete", "--label", "team=a", "--label", "env=dev")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "Waiting for 2 sandbox(es) to be deleted") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "2 sandbox(es) deleted in") {
		t.Fatalf("expected deleted-with-duration message in stderr, got %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
}

func TestSandboxDeleteMissingTarget(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "delete")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "<sandbox_id>") || !strings.Contains(stderr, "--label") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxDeleteMixedTargetModes(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "delete", "sandbox-123", "--label", "team=a")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxDeleteBadLabel(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "delete", "--label", "badlabel")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "--label") || !strings.Contains(stderr, "=") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxDeleteRejectsJSON(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "delete", "sandbox-123", "--json")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "unknown flag: --json") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxDeleteRejectsUnknownFlag(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "sandbox", "delete", "--unknown")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "unknown flag: --unknown") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}
