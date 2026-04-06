package client

import (
	"context"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestCreateSandboxWithConfig(t *testing.T) {
	t.Parallel()

	t.Run("config_only", func(t *testing.T) {
		t.Parallel()
		configYAML := []byte("image: test:latest\n")

		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if len(req.GetConfigYaml()) == 0 {
				t.Fatal("expected config_yaml to be set")
			}
			if req.GetCreateSpec().GetImage() != "" {
				t.Fatal("expected empty image in create_spec when using config only")
			}
			return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sb-1", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 1}}, nil
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
		_, err := client.CreateSandbox(context.Background(), WithConfigYAML(configYAML), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
	})

	t.Run("config_and_image", func(t *testing.T) {
		t.Parallel()
		configYAML := []byte("builtin_tools:\n  - claude\n")

		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			if len(req.GetConfigYaml()) == 0 {
				t.Fatal("expected config_yaml to be set")
			}
			if req.GetCreateSpec().GetImage() != "override:latest" {
				t.Fatalf("expected image override, got %s", req.GetCreateSpec().GetImage())
			}
			return &agboxv1.CreateSandboxResponse{Sandbox: &agboxv1.SandboxHandle{SandboxId: "sb-2", State: agboxv1.SandboxState_SANDBOX_STATE_READY, LastEventSequence: 1}}, nil
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
		_, err := client.CreateSandbox(context.Background(), WithConfigYAML(configYAML), WithImage("override:latest"), WithWait(false))
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

	t.Run("empty_config_yaml", func(t *testing.T) {
		t.Parallel()
		client := newTestClient(&fakeRPCClient{}, nil)
		_, err := client.CreateSandbox(context.Background(), WithConfigYAML(nil))
		if err == nil {
			t.Fatal("expected error for empty config_yaml")
		}
	})
}

func TestWithIdleTTL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		d           time.Duration
		wantSeconds int64
	}{
		{"zero_disables", 0, 0},
		{"five_minutes", 5 * time.Minute, 300},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var captured *agboxv1.CreateSandboxRequest
			base := &fakeRPCClient{}
			base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
				captured = req
				return &agboxv1.CreateSandboxResponse{
					Sandbox: &agboxv1.SandboxHandle{
						SandboxId:         "sb-1",
						State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
						LastEventSequence: 1,
					},
				}, nil
			}

			client := newTestClient(base, nil)
			_, err := client.CreateSandbox(context.Background(), WithImage("test:latest"), WithIdleTTL(tc.d), WithWait(false))
			if err != nil {
				t.Fatalf("CreateSandbox failed: %v", err)
			}
			got := captured.GetCreateSpec().GetIdleTtl()
			if got == nil {
				t.Fatal("expected idle_ttl to be set, got nil")
			}
			want := durationpb.New(tc.d)
			if got.GetSeconds() != want.GetSeconds() || got.GetNanos() != want.GetNanos() {
				t.Fatalf("idle_ttl mismatch: got %v, want %v", got, want)
			}
		})
	}

	t.Run("no_option_leaves_nil", func(t *testing.T) {
		t.Parallel()
		var captured *agboxv1.CreateSandboxRequest
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			captured = req
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sb-1",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(), WithImage("test:latest"), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
		if got := captured.GetCreateSpec().GetIdleTtl(); got != nil {
			t.Fatalf("expected idle_ttl to be nil, got %v", got)
		}
	})
}

func TestWithIdleTTLRejectsNegative(t *testing.T) {
	t.Parallel()
	client := newTestClient(&fakeRPCClient{}, nil)
	_, err := client.CreateSandbox(context.Background(), WithImage("test:latest"), WithIdleTTL(-time.Second), WithWait(false))
	if err == nil {
		t.Fatal("expected error for negative idle_ttl")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestWithPorts(t *testing.T) {
	t.Parallel()

	t.Run("ports_in_create_spec", func(t *testing.T) {
		t.Parallel()
		var captured *agboxv1.CreateSandboxRequest
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			captured = req
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sb-1",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(),
			WithImage("test:latest"),
			WithPorts(
				PortMapping{ContainerPort: 8080, HostPort: 9090, Protocol: "tcp"},
				PortMapping{ContainerPort: 53, HostPort: 5353, Protocol: "udp"},
				PortMapping{ContainerPort: 3000, HostPort: 3000},
			),
			WithWait(false),
		)
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
		ports := captured.GetCreateSpec().GetPorts()
		if len(ports) != 3 {
			t.Fatalf("expected 3 ports, got %d", len(ports))
		}
		if ports[0].GetContainerPort() != 8080 || ports[0].GetHostPort() != 9090 || ports[0].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_TCP {
			t.Fatalf("unexpected first port: %v", ports[0])
		}
		if ports[1].GetContainerPort() != 53 || ports[1].GetHostPort() != 5353 || ports[1].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_UDP {
			t.Fatalf("unexpected second port: %v", ports[1])
		}
		// Empty protocol defaults to TCP.
		if ports[2].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_TCP {
			t.Fatalf("expected default TCP protocol, got %v", ports[2].GetProtocol())
		}
	})

	t.Run("no_ports_leaves_empty", func(t *testing.T) {
		t.Parallel()
		var captured *agboxv1.CreateSandboxRequest
		base := &fakeRPCClient{}
		base.createSandboxFn = func(_ context.Context, req *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
			captured = req
			return &agboxv1.CreateSandboxResponse{
				Sandbox: &agboxv1.SandboxHandle{
					SandboxId:         "sb-1",
					State:             agboxv1.SandboxState_SANDBOX_STATE_READY,
					LastEventSequence: 1,
				},
			}, nil
		}

		client := newTestClient(base, nil)
		_, err := client.CreateSandbox(context.Background(), WithImage("test:latest"), WithWait(false))
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
		if got := captured.GetCreateSpec().GetPorts(); len(got) != 0 {
			t.Fatalf("expected no ports, got %v", got)
		}
	})
}

func TestToProtoPortProtocol(t *testing.T) {
	t.Parallel()

	validCases := []struct {
		input string
		want  agboxv1.PortProtocol
	}{
		{"tcp", agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		{"TCP", agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		{"udp", agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
		{"UDP", agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
		{"sctp", agboxv1.PortProtocol_PORT_PROTOCOL_SCTP},
		{"SCTP", agboxv1.PortProtocol_PORT_PROTOCOL_SCTP},
		{"", agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		{"  tcp  ", agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
	}

	for _, tc := range validCases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := toProtoPortProtocol(tc.input)
			if err != nil {
				t.Fatalf("toProtoPortProtocol(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("toProtoPortProtocol(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}

	invalidCases := []string{"unknown", "http", "ftp"}
	for _, input := range invalidCases {
		input := input
		t.Run("error_"+input, func(t *testing.T) {
			t.Parallel()
			_, err := toProtoPortProtocol(input)
			if err == nil {
				t.Fatalf("toProtoPortProtocol(%q) expected error, got nil", input)
			}
			if !strings.Contains(err.Error(), "unsupported port protocol") {
				t.Fatalf("expected 'unsupported port protocol' error, got: %v", err)
			}
		})
	}
}
