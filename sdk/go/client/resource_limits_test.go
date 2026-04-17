package client

import (
	"context"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestCreateSandboxResourceLimitOptions(t *testing.T) {
	t.Parallel()

	var captured *agboxv1.CreateSandboxRequest
	fake := &fakeRPCClient{
		createSandboxFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			captured = request
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sandbox-1",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		},
	}
	client := newTestClient(fake, nil)
	_, err := client.CreateSandbox(
		context.Background(),
		WithImage("example:latest"),
		WithCPULimit("2"),
		WithMemoryLimit("4g"),
		WithPrimaryDiskLimit("10g"),
		WithCompanionContainers(CompanionContainerSpec{
			Name:      "db",
			Image:     "postgres:16",
			DiskLimit: "5g",
		}),
		WithWait(false),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected CreateSandbox to be invoked")
	}
	spec := captured.GetCreateSpec()
	if spec.GetCpuLimit() != "2" {
		t.Fatalf("CpuLimit: got %q, want %q", spec.GetCpuLimit(), "2")
	}
	if spec.GetMemoryLimit() != "4g" {
		t.Fatalf("MemoryLimit: got %q, want %q", spec.GetMemoryLimit(), "4g")
	}
	if spec.GetDiskLimit() != "10g" {
		t.Fatalf("DiskLimit: got %q, want %q", spec.GetDiskLimit(), "10g")
	}
	companions := spec.GetCompanionContainers()
	if len(companions) != 1 {
		t.Fatalf("expected 1 companion, got %d", len(companions))
	}
	if companions[0].GetDiskLimit() != "5g" {
		t.Fatalf("companion DiskLimit: got %q, want %q", companions[0].GetDiskLimit(), "5g")
	}
}
