package client

import (
	"context"
	"os"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestCreateSandboxWithConfig(t *testing.T) {
	t.Parallel()

	t.Run("config_only", func(t *testing.T) {
		t.Parallel()
		tmpFile := t.TempDir() + "/test.yaml"
		if err := os.WriteFile(tmpFile, []byte("image: test:latest\n"), 0644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}

		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if len(req.GetConfigYaml()) == 0 {
				t.Fatal("expected config_yaml to be set")
			}
			if req.GetCreateSpec().GetImage() != "" {
				t.Fatal("expected empty image in create_spec when using config only")
			}
			return &agboxv1.CreateSandboxResponse{SandboxId: "sb-1"}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         sandboxID,
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(), WithConfig(tmpFile), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
	})

	t.Run("config_and_image", func(t *testing.T) {
		t.Parallel()
		tmpFile := t.TempDir() + "/test.yaml"
		if err := os.WriteFile(tmpFile, []byte("builtin_resources:\n  - .claude\n"), 0644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}

		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if len(req.GetConfigYaml()) == 0 {
				t.Fatal("expected config_yaml to be set")
			}
			if req.GetCreateSpec().GetImage() != "override:latest" {
				t.Fatalf("expected image override, got %s", req.GetCreateSpec().GetImage())
			}
			return &agboxv1.CreateSandboxResponse{SandboxId: "sb-2"}, nil
		}
		base.getSandboxFn = func(_ context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
			return &agboxv1.GetSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         sandboxID,
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(), WithConfig(tmpFile), WithImage("override:latest"), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
	})

	t.Run("neither_config_nor_image", func(t *testing.T) {
		t.Parallel()
		client := newTestClient(&fakeRPCClient{}, nil)
		_, err := client.CreateSandbox(context.Background(), WithWait(false))
		if err == nil {
			t.Fatal("expected error when neither config nor image provided")
		}
		if !strings.Contains(err.Error(), "at least one") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("config_file_not_found", func(t *testing.T) {
		t.Parallel()
		client := newTestClient(&fakeRPCClient{}, nil)
		_, err := client.CreateSandbox(context.Background(), WithConfig("/nonexistent/path.yaml"))
		if err == nil {
			t.Fatal("expected error for nonexistent config file")
		}
	})
}
