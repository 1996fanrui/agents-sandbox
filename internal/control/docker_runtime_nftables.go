package control

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// nftablesConnector abstracts nftables operations so that unit tests can
// substitute a no-op implementation (real nftables requires CAP_NET_ADMIN).
type nftablesConnector interface {
	applyHostIsolation(bridge string, subnet *net.IPNet) error
	removeHostIsolation(bridge string, subnet *net.IPNet)
}

// noopNftablesConnector is used in tests where nftables is not available.
type noopNftablesConnector struct{}

func (noopNftablesConnector) applyHostIsolation(string, *net.IPNet) error { return nil }
func (noopNftablesConnector) removeHostIsolation(string, *net.IPNet)      {}

// extractNetworkIsolationParams extracts the bridge interface name and subnet
// from a Docker network inspect result. These are used to construct nftables
// rules that block all container-to-host traffic on that sandbox network.
func extractNetworkIsolationParams(inspectResult network.Inspect) (bridge string, subnet *net.IPNet, err error) {
	if len(inspectResult.IPAM.Config) == 0 {
		return "", nil, fmt.Errorf("network %s has no IPAM configuration", inspectResult.ID)
	}
	_, subnet, err = net.ParseCIDR(inspectResult.IPAM.Config[0].Subnet)
	if err != nil {
		return "", nil, fmt.Errorf("parse subnet %q: %w", inspectResult.IPAM.Config[0].Subnet, err)
	}

	bridge = inspectResult.Options["com.docker.network.bridge.name"]
	if bridge == "" {
		// Docker names bridge interfaces as "br-" + first 12 chars of network ID.
		if len(inspectResult.ID) < 12 {
			return "", nil, fmt.Errorf("network ID %q is too short to derive bridge name", inspectResult.ID)
		}
		bridge = "br-" + inspectResult.ID[:12]
	}
	return bridge, subnet, nil
}

// applyNetworkHostIsolation adds an nftables DOCKER-USER rule that drops all
// traffic from the sandbox network subnet to any host-local address, completely
// preventing containers from reaching host services. The operation is idempotent:
// if the rule already exists, it is not duplicated.
func (backend *dockerRuntimeBackend) applyNetworkHostIsolation(ctx context.Context, networkName string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	inspectResult, err := backend.dockerClient.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect network %s for host isolation: %w", networkName, err)
	}
	bridge, subnet, err := extractNetworkIsolationParams(inspectResult)
	if err != nil {
		return fmt.Errorf("extract isolation params for network %s: %w", networkName, err)
	}
	return backend.nftConn.applyHostIsolation(bridge, subnet)
}

// removeNetworkHostIsolation removes the nftables DOCKER-USER rule that blocks
// container-to-host traffic for the given sandbox network. This is best-effort:
// errors are logged but not propagated, since the network itself is about to be
// removed.
func (backend *dockerRuntimeBackend) removeNetworkHostIsolation(ctx context.Context, networkName string) {
	if runtime.GOOS != "linux" {
		return
	}
	inspectResult, err := backend.dockerClient.NetworkInspect(ctx, networkName, network.InspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			backend.config.Logger.Info("network already removed, skipping nftables cleanup",
				slog.String("network", networkName),
			)
			return
		}
		backend.config.Logger.Warn("failed to inspect network for nftables cleanup",
			slog.String("network", networkName),
			slog.Any("error", err),
		)
		return
	}
	bridge, subnet, err := extractNetworkIsolationParams(inspectResult)
	if err != nil {
		backend.config.Logger.Warn("failed to extract isolation params for nftables cleanup",
			slog.String("network", networkName),
			slog.Any("error", err),
		)
		return
	}
	backend.nftConn.removeHostIsolation(bridge, subnet)
}
