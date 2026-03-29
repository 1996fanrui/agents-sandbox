package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
)

func (backend *dockerRuntimeBackend) materializeGenericMounts(
	requests []*agboxv1.MountSpec,
) ([]dockerMount, error) {
	mounts := make([]dockerMount, 0, len(requests))
	for _, request := range requests {
		if request.GetSource() == "" {
			return nil, errors.New("mount source is required")
		}
		if request.GetTarget() == "" {
			return nil, errors.New("mount target is required")
		}
		if !filepath.IsAbs(request.GetTarget()) {
			return nil, fmt.Errorf("mount target must be absolute: %s", request.GetTarget())
		}
		sourcePath := request.GetSource()
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("mount source must not be a symlink: %s", sourcePath)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("mount source must be a file or directory: %s", sourcePath)
		}
		mounts = append(mounts, dockerMount{
			Source:   sourcePath,
			Target:   request.GetTarget(),
			ReadOnly: !request.GetWritable(),
		})
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) materializeGenericCopies(
	sandboxID string,
	requests []*agboxv1.CopySpec,
	state *sandboxRuntimeState,
) ([]dockerMount, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if backend.config.StateRoot == "" {
		return nil, errors.New("runtime.state_root is required for generic copy inputs")
	}
	if state.ShadowRoot == "" {
		state.ShadowRoot = filepath.Join(backend.config.StateRoot, "sandboxes", sandboxID, "shadow")
	}
	mounts := make([]dockerMount, 0, len(requests))
	for index, request := range requests {
		if request.GetSource() == "" {
			return nil, errors.New("copy source is required")
		}
		if request.GetTarget() == "" {
			return nil, errors.New("copy target is required")
		}
		if !filepath.IsAbs(request.GetTarget()) {
			return nil, fmt.Errorf("copy target must be absolute: %s", request.GetTarget())
		}
		sourcePath := request.GetSource()
		sourceInfo, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("copy source must not be a symlink: %s", sourcePath)
		}
		if !sourceInfo.Mode().IsRegular() && !sourceInfo.IsDir() {
			return nil, fmt.Errorf("copy source must be a file or directory: %s", sourcePath)
		}
		copyRoot := filepath.Join(state.ShadowRoot, "copies", fmt.Sprintf("%02d-%s", index, sanitizeRuntimeName(request.GetTarget())))
		if err := os.RemoveAll(copyRoot); err != nil {
			return nil, err
		}
		if err := copyTreeWithPatterns(sourcePath, copyRoot, request.GetExcludePatterns()); err != nil {
			return nil, err
		}
		mounts = append(mounts, dockerMount{
			Source:   copyRoot,
			Target:   request.GetTarget(),
			ReadOnly: false,
		})
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) materializeBuiltinTools(
	sandboxID string,
	resources []string,
	state *sandboxRuntimeState,
) ([]dockerMount, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	// Collect unique mount IDs across all requested tools, preserving first-seen order.
	seen := make(map[profile.MountID]struct{})
	var mountIDs []profile.MountID
	for _, resource := range resources {
		capability, ok := profile.CapabilityByID(resource)
		if !ok {
			return nil, fmt.Errorf("unknown builtin resource %q", resource)
		}
		for _, mountID := range capability.MountIDs {
			if _, exists := seen[mountID]; !exists {
				seen[mountID] = struct{}{}
				mountIDs = append(mountIDs, mountID)
			}
		}
	}

	mounts := make([]dockerMount, 0, len(mountIDs))
	for _, mountID := range mountIDs {
		mount, ok := profile.MountByID(mountID)
		if !ok {
			return nil, fmt.Errorf("unknown capability mount %q", mountID)
		}
		sourcePath, err := resolveCapabilityMountSource(mount)
		if err != nil {
			return nil, err
		}
		switch mount.Mode {
		case profile.CapabilityModeSocket:
			if err := requireSocketPath(sourcePath); err != nil {
				return nil, err
			}
			mounts = append(mounts, dockerMount{
				Source:   sourcePath,
				Target:   mount.ContainerTarget,
				ReadOnly: false,
			})
		default:
			writable := mount.Mode == profile.CapabilityModeReadWrite
			actualSource, readOnly, err := backend.materializeBuiltinToolPath(sourcePath, writable, state)
			if err != nil {
				return nil, err
			}
			mounts = append(mounts, dockerMount{
				Source:   actualSource,
				Target:   mount.ContainerTarget,
				ReadOnly: readOnly,
			})
		}
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) materializeBuiltinToolPath(
	sourcePath string,
	writable bool,
	state *sandboxRuntimeState,
) (string, bool, error) {
	// Builtin tools are always bind-mounted as-is, including any symlinks.
	// Shadow-copy logic is intentionally skipped: these are trusted host directories
	// (tool configs, caches) that may contain symlinks to arbitrary host paths,
	// and the container is expected to see them exactly as they appear on the host.
	if _, err := os.Stat(sourcePath); err != nil {
		return "", false, err
	}
	return sourcePath, !writable, nil
}

func resolveCapabilityMountSource(mount profile.CapabilityMount) (string, error) {
	if mount.ID == profile.MountIDSSHAgent {
		socketPath := os.Getenv("SSH_AUTH_SOCK")
		if socketPath == "" {
			return "", errors.New("SSH_AUTH_SOCK is required for ssh-agent tooling projection")
		}
		return filepath.Abs(socketPath)
	}
	return expandHomePath(mount.DefaultHostPath)
}

func requireSocketPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", path)
	}
	return nil
}

func expandHomePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}
