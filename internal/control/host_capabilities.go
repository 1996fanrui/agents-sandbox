package control

import (
	"context"
	"log/slog"
	"os"

	"github.com/docker/docker/client"
)

// hostCapabilities captures the cgroup facts probed once at daemon startup.
// It is a pure data snapshot, cached for the lifetime of the process, and
// consumed by validateCreateSpec to decide whether cpu/memory limits can be
// enforced on this host.
type hostCapabilities struct {
	CgroupDriver      string
	CgroupV2Available bool
}

// probeHostCapabilities inspects the Docker daemon + host filesystem to fill
// hostCapabilities. Never fails; any probing error is logged and leaves the
// corresponding field at its zero value so the daemon can still start on
// platforms where cgroup v2 or systemd is unavailable (e.g. macOS).
func probeHostCapabilities(ctx context.Context, dockerClient *client.Client, logger *slog.Logger) hostCapabilities {
	caps := hostCapabilities{}
	if dockerClient != nil {
		info, err := dockerClient.Info(ctx)
		if err != nil {
			logger.Warn("probe docker info for cgroup driver failed", slog.Any("error", err))
		} else {
			caps.CgroupDriver = info.CgroupDriver
		}
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		caps.CgroupV2Available = true
	}
	return caps
}
