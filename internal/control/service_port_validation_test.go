package control

import (
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestValidateCreateSpec_PortMapping(t *testing.T) {
	testCases := []struct {
		name        string
		ports       []*agboxv1.PortMapping
		expectError bool
		errContains string
	}{
		{
			name: "valid_tcp_port",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: false,
		},
		{
			name: "valid_udp_port",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 53, HostPort: 5353, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
			},
			expectError: false,
		},
		{
			name: "valid_sctp_port",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 3868, HostPort: 3868, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_SCTP},
			},
			expectError: false,
		},
		{
			name: "container_port_zero",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 0, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: true,
			errContains: "container_port must be between 1 and 65535",
		},
		{
			name: "host_port_zero",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 0, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: true,
			errContains: "host_port must be between 1 and 65535",
		},
		{
			name: "container_port_exceeds_max",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 70000, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: true,
			errContains: "container_port must be between 1 and 65535",
		},
		{
			name: "host_port_exceeds_max",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 70000, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: true,
			errContains: "host_port must be between 1 and 65535",
		},
		{
			name: "duplicate_host_port_same_protocol",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 9090, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
				{ContainerPort: 8081, HostPort: 9090, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			},
			expectError: true,
			errContains: "duplicate host_port 9090",
		},
		{
			name: "same_host_port_different_protocol_ok",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 9090, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
				{ContainerPort: 8081, HostPort: 9090, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
			},
			expectError: false,
		},
		{
			name: "unknown_protocol_enum",
			ports: []*agboxv1.PortMapping{
				{ContainerPort: 8080, HostPort: 8080, Protocol: agboxv1.PortProtocol(99)},
			},
			expectError: true,
			errContains: "port protocol must be TCP, UDP, or SCTP",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				Ports: tc.ports,
			}
			err := validateCreateSpec(spec, hostCapabilities{})
			if tc.expectError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error to contain %q, got %v", tc.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}
