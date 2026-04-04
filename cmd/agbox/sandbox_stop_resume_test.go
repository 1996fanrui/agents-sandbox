package main

import (
	"context"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestSandboxStop(t *testing.T) {
	stopCalled := false
	service := &fakeSandboxService{
		stopFn: func(_ context.Context, request *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
			if request.GetSandboxId() != "sandbox-123" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			stopCalled = true
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
					LastEventSequence: 5,
				},
			}, nil
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "stop", "sandbox-123")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !stopCalled {
		t.Fatal("StopSandbox was not called")
	}
	if !strings.Contains(stderr, "Waiting for sandbox sandbox-123 to be stopped...") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Sandbox stopped in") {
		t.Fatalf("expected stopped message in stderr, got %q", stderr)
	}
}

func TestSandboxStopFailed(t *testing.T) {
	service := &fakeSandboxService{
		stopFn: func(_ context.Context, _ *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					ErrorMessage:      "container crashed",
					LastEventSequence: 5,
				},
			}, nil
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "stop", "sandbox-123")
	if exitCode != exitCodeRuntimeError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "failed") {
		t.Fatalf("expected failure message in stderr, got %q", stderr)
	}
}

func TestSandboxResume(t *testing.T) {
	resumeCalled := false
	service := &fakeSandboxService{
		resumeFn: func(_ context.Context, request *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error) {
			if request.GetSandboxId() != "sandbox-123" {
				t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
			}
			resumeCalled = true
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getFn: func(_ context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 5,
				},
			}, nil
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "resume", "sandbox-123")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !resumeCalled {
		t.Fatal("ResumeSandbox was not called")
	}
	if !strings.Contains(stderr, "Waiting for sandbox sandbox-123 to be resumed...") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Sandbox resumed in") {
		t.Fatalf("expected resumed message in stderr, got %q", stderr)
	}
}

func TestSandboxResumeFailed(t *testing.T) {
	service := &fakeSandboxService{
		resumeFn: func(_ context.Context, _ *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getFn: func(_ context.Context, _ *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-123",
					State:             agboxv1.SandboxState_SANDBOX_STATE_FAILED,
					ErrorMessage:      "container crashed",
					LastEventSequence: 5,
				},
			}, nil
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "sandbox", "resume", "sandbox-123")
	if exitCode != exitCodeRuntimeError {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "failed") {
		t.Fatalf("expected failure message in stderr, got %q", stderr)
	}
}
