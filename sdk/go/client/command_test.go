package client

import (
	"context"
	"reflect"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestGoSDKRejectsEmptyCommandArray(t *testing.T) {
	t.Parallel()

	client := newTestClient(&fakeRPCClient{}, nil)
	_, err := client.CreateSandbox(
		context.Background(),
		WithImage("example:latest"),
		WithCommand(),
	)
	if err == nil {
		t.Fatal("expected error for empty command array, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "command") || !strings.Contains(msg, "empty") {
		t.Fatalf("expected error to mention 'command' and 'empty', got: %v", err)
	}
}

func TestGoSDKRejectsEmptyStringInCommand(t *testing.T) {
	t.Parallel()

	client := newTestClient(&fakeRPCClient{}, nil)
	_, err := client.CreateSandbox(
		context.Background(),
		WithImage("example:latest"),
		WithCommand("foo", "", "bar"),
	)
	if err == nil {
		t.Fatal("expected error for empty-string element in command, got nil")
	}
	if !strings.Contains(err.Error(), "command[1]") {
		t.Fatalf("expected error to reference command[1], got: %v", err)
	}
}

func TestGoSDKWithCommandPopulatesCreateSpec(t *testing.T) {
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
		WithCommand("myworker", "serve"),
		WithWait(false),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected CreateSandbox to be invoked")
	}
	got := captured.GetCreateSpec().GetCommand()
	want := []string{"myworker", "serve"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected CreateSpec.Command: got %#v, want %#v", got, want)
	}
}

func TestGoSDKCompanionRejectsEmptyCommandArray(t *testing.T) {
	t.Parallel()

	client := newTestClient(&fakeRPCClient{}, nil)
	_, err := client.CreateSandbox(
		context.Background(),
		WithImage("example:latest"),
		WithCompanionContainers(CompanionContainerSpec{
			Name:    "redis",
			Image:   "redis:7",
			Command: []string{},
		}),
	)
	if err == nil {
		t.Fatal("expected error for companion with empty command array, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "redis") || !strings.Contains(msg, "empty array") {
		t.Fatalf("expected error to mention companion name 'redis' and 'empty array', got: %v", err)
	}
}

func TestGoSDKCompanionRejectsEmptyStringInCommand(t *testing.T) {
	t.Parallel()

	client := newTestClient(&fakeRPCClient{}, nil)
	_, err := client.CreateSandbox(
		context.Background(),
		WithImage("example:latest"),
		WithCompanionContainers(CompanionContainerSpec{
			Name:    "redis",
			Image:   "redis:7",
			Command: []string{"foo", "", "bar"},
		}),
	)
	if err == nil {
		t.Fatal("expected error for companion with empty-string command element, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "redis") || !strings.Contains(msg, "command[1]") {
		t.Fatalf("expected error to mention companion name 'redis' and 'command[1]', got: %v", err)
	}
}

func TestGoSDKCompanionValidCommandRoundTripsThroughOption(t *testing.T) {
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
		WithCompanionContainers(CompanionContainerSpec{
			Name:    "redis",
			Image:   "redis:7",
			Command: []string{"redis-server"},
		}),
		WithWait(false),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected CreateSandbox to be invoked")
	}
	companions := captured.GetCreateSpec().GetCompanionContainers()
	if len(companions) != 1 {
		t.Fatalf("expected 1 companion in proto, got %d", len(companions))
	}
	got := companions[0].GetCommand()
	want := []string{"redis-server"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected companion Command: got %#v, want %#v", got, want)
	}
}

func TestGoSDKCompanionContainerCommandRoundTrip(t *testing.T) {
	t.Parallel()

	original := []CompanionContainerSpec{
		{
			Name:    "redis",
			Image:   "redis:7",
			Command: []string{"redis-server", "--appendonly", "yes"},
		},
	}
	protoSpecs := toProtoCompanionContainers(original)
	if len(protoSpecs) != 1 {
		t.Fatalf("expected 1 proto companion, got %d", len(protoSpecs))
	}
	if !reflect.DeepEqual(protoSpecs[0].GetCommand(), original[0].Command) {
		t.Fatalf("proto companion Command mismatch: got %#v, want %#v",
			protoSpecs[0].GetCommand(), original[0].Command)
	}
	round := toCompanionContainers(protoSpecs)
	if len(round) != 1 {
		t.Fatalf("expected 1 round-tripped companion, got %d", len(round))
	}
	if !reflect.DeepEqual(round[0].Command, original[0].Command) {
		t.Fatalf("round-trip Command mismatch: got %#v, want %#v",
			round[0].Command, original[0].Command)
	}
	if round[0].Name != "redis" || round[0].Image != "redis:7" {
		t.Fatalf("round-trip metadata mismatch: %#v", round[0])
	}
}
