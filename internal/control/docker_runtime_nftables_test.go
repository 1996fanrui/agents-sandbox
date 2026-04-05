package control

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
)

func TestExtractNetworkIsolationParams(t *testing.T) {
	tests := []struct {
		name         string
		inspect      network.Inspect
		wantBridge   string
		wantSubnet   string
		wantErr      bool
		errSubstring string
	}{
		{
			name: "explicit_bridge_name",
			inspect: network.Inspect{
				ID: "abc123def456abcd",
				IPAM: network.IPAM{
					Config: []network.IPAMConfig{
						{Subnet: "172.18.0.0/16", Gateway: "172.18.0.1"},
					},
				},
				Options: map[string]string{
					"com.docker.network.bridge.name": "br-custom",
				},
			},
			wantBridge: "br-custom",
			wantSubnet: "172.18.0.0/16",
		},
		{
			name: "derived_bridge_name_from_network_id",
			inspect: network.Inspect{
				ID: "abc123def456abcd",
				IPAM: network.IPAM{
					Config: []network.IPAMConfig{
						{Subnet: "10.0.0.0/24", Gateway: "10.0.0.1"},
					},
				},
				Options: map[string]string{},
			},
			wantBridge: "br-abc123def456",
			wantSubnet: "10.0.0.0/24",
		},
		{
			name: "empty_ipam_config",
			inspect: network.Inspect{
				ID:   "abc123def456abcd",
				IPAM: network.IPAM{},
			},
			wantErr:      true,
			errSubstring: "no IPAM configuration",
		},
		{
			name: "short_network_id_without_explicit_bridge",
			inspect: network.Inspect{
				ID: "short",
				IPAM: network.IPAM{
					Config: []network.IPAMConfig{
						{Subnet: "172.18.0.0/16", Gateway: "172.18.0.1"},
					},
				},
				Options: map[string]string{},
			},
			wantErr:      true,
			errSubstring: "too short to derive bridge name",
		},
		{
			name: "invalid_subnet_cidr",
			inspect: network.Inspect{
				ID: "abc123def456abcd",
				IPAM: network.IPAM{
					Config: []network.IPAMConfig{
						{Subnet: "not-a-cidr", Gateway: "10.0.0.1"},
					},
				},
				Options: map[string]string{},
			},
			wantErr:      true,
			errSubstring: "parse subnet",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge, subnet, err := extractNetworkIsolationParams(tt.inspect)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstring != "" && !strings.Contains(err.Error(), tt.errSubstring) {
					t.Fatalf("expected error containing %q, got %q", tt.errSubstring, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bridge != tt.wantBridge {
				t.Fatalf("bridge: expected %q, got %q", tt.wantBridge, bridge)
			}
			if subnet.String() != tt.wantSubnet {
				t.Fatalf("subnet: expected %q, got %q", tt.wantSubnet, subnet.String())
			}
		})
	}
}
