package client

import (
	"context"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestCreateSandboxGPUOption(t *testing.T) {
	t.Parallel()

	t.Run("with_gpus_all", func(t *testing.T) {
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
			WithGPUs("all"),
			WithWait(false),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if captured == nil {
			t.Fatal("expected CreateSandbox to be invoked")
		}
		if got := captured.GetCreateSpec().GetGpus(); got != "all" {
			t.Fatalf("Gpus: got %q, want %q", got, "all")
		}
	})

	t.Run("defaults_to_empty", func(t *testing.T) {
		t.Parallel()

		var captured *agboxv1.CreateSandboxRequest
		fake := &fakeRPCClient{
			createSandboxFn: func(_ context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
				captured = request
				return &agboxv1.CreateSandboxResponse{
					Sandbox: &agboxv1.SandboxHandle{
						SandboxId:         "sandbox-2",
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
			WithWait(false),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if captured == nil {
			t.Fatal("expected CreateSandbox to be invoked")
		}
		if got := captured.GetCreateSpec().GetGpus(); got != "" {
			t.Fatalf("Gpus: got %q, want empty", got)
		}
	})
}
