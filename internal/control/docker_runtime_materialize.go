package control

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
)

// dockerDesktopSSHAgentSocket is the magic path provided by Docker Desktop
// for Mac (and Docker Desktop for Linux) to proxy the host SSH agent into
// containers. Direct bind-mounting of macOS launchd sockets is not supported
// by Docker Desktop's LinuxKit VM, so this synthetic socket must be used instead.
const dockerDesktopSSHAgentSocket = "/run/host-services/ssh-auth.sock"

// createMountSymlink creates a symlink in symlinkDir that points to sourcePath.
// The symlink name is "{md5hex}_{basename}" where md5hex is the MD5 hash of
// the absolute source path (32 hex chars) and basename is filepath.Base of the
// source. This flat naming avoids collisions between different parents with the
// same basename while keeping names human-readable.
//
// Idempotent: if the symlink already exists and points to the same target, it
// is left untouched. If it exists but points elsewhere, it is replaced.
func createMountSymlink(symlinkDir string, sourcePath string) (string, error) {
	cleanPath := filepath.Clean(sourcePath)
	hash := md5.Sum([]byte(cleanPath))
	hashHex := hex.EncodeToString(hash[:])

	basename := filepath.Base(cleanPath)
	if basename == "/" || basename == "." {
		basename = "_root"
	}

	symlinkName := hashHex + "_" + basename
	symlinkPath := filepath.Join(symlinkDir, symlinkName)

	existing, err := os.Readlink(symlinkPath)
	if err == nil {
		if existing == cleanPath {
			return symlinkPath, nil
		}
		// Symlink exists but points to a different target; replace it.
		if err := os.Remove(symlinkPath); err != nil {
			return "", fmt.Errorf("remove stale symlink %s: %w", symlinkPath, err)
		}
	}

	if err := os.Symlink(cleanPath, symlinkPath); err != nil {
		return "", fmt.Errorf("create symlink %s -> %s: %w", symlinkPath, cleanPath, err)
	}
	return symlinkPath, nil
}

func (backend *dockerRuntimeBackend) materializeGenericMounts(
	requests []*agboxv1.MountSpec,
	symlinkDir string,
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
		info, err := os.Stat(sourcePath)
		if err != nil {
			return nil, err
		}
		isSocket := info.Mode()&os.ModeSocket != 0
		if !info.Mode().IsRegular() && !info.IsDir() && !isSocket {
			return nil, fmt.Errorf("mount source must be a file, directory, or unix socket: %s", sourcePath)
		}
		mountSource := sourcePath
		if symlinkDir != "" {
			symlinkPath, err := createMountSymlink(symlinkDir, sourcePath)
			if err != nil {
				return nil, fmt.Errorf("create symlink for mount source %s: %w", sourcePath, err)
			}
			mountSource = symlinkPath
		}
		mounts = append(mounts, dockerMount{
			Source:   mountSource,
			Target:   request.GetTarget(),
			ReadOnly: !request.GetWritable(),
		})
	}
	return mounts, nil
}

// deferredCopy represents a validated copy request that will be applied via
// CopyToContainer after the container is created but before it is started.
type deferredCopy struct {
	SourcePath      string
	ContainerTarget string
	ExcludePatterns []string
}

func validateGenericCopies(requests []*agboxv1.CopySpec) ([]deferredCopy, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	copies := make([]deferredCopy, 0, len(requests))
	for _, request := range requests {
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
		copies = append(copies, deferredCopy{
			SourcePath:      sourcePath,
			ContainerTarget: request.GetTarget(),
			ExcludePatterns: request.GetExcludePatterns(),
		})
	}
	return copies, nil
}

func (backend *dockerRuntimeBackend) materializeBuiltinTools(
	sandboxID string,
	resources []string,
	symlinkDir string,
) ([]dockerMount, error) {
	if len(resources) == 0 {
		return nil, nil
	}

	// Resolve each tool into its mount IDs, tracking which mounts belong to optional tools.
	type mountEntry struct {
		mountID  profile.MountID
		optional bool
	}
	seen := make(map[profile.MountID]struct{})
	var entries []mountEntry
	for _, resource := range resources {
		capability, ok := profile.CapabilityByID(resource)
		if !ok {
			return nil, fmt.Errorf("unknown builtin resource %q", resource)
		}
		for _, mountID := range capability.MountIDs {
			if _, exists := seen[mountID]; !exists {
				seen[mountID] = struct{}{}
				mountDef, _ := profile.MountByID(mountID)
				entries = append(entries, mountEntry{mountID: mountID, optional: mountDef.Optional})
			}
		}
	}

	logger := backend.config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	mounts := make([]dockerMount, 0, len(entries))
	for _, entry := range entries {
		mount, ok := profile.MountByID(entry.mountID)
		if !ok {
			return nil, fmt.Errorf("unknown capability mount %q", entry.mountID)
		}
		sourcePath, err := resolveCapabilityMountSource(mount)
		if err != nil {
			if entry.optional {
				logger.Info("skipping optional builtin mount: host path not available",
					slog.String("mount", string(entry.mountID)),
					slog.String("error", err.Error()))
				continue
			}
			return nil, err
		}

		// Project credentials from macOS Keychain into the host mount directory
		// before bind-mounting. On non-macOS platforms this is a no-op.
		if mount.MacOSKeychain != nil {
			if err := projectMacOSKeychainCredential(logger, sourcePath, mount.MacOSKeychain); err != nil {
				logger.Warn("failed to project macOS Keychain credential",
					slog.String("mount", string(entry.mountID)),
					slog.String("error", err.Error()))
			}
		}

		switch mount.Mode {
		case profile.CapabilityModeSocket:
			// Docker Desktop magic paths (e.g. /run/host-services/ssh-auth.sock)
			// exist only inside the Docker VM, not on the host filesystem.
			// Skip the host-side stat check for these paths.
			socketSource := sourcePath
			if sourcePath != dockerDesktopSSHAgentSocket {
				if err := requireSocketPath(sourcePath); err != nil {
					if entry.optional {
						logger.Info("skipping optional builtin mount: socket not available",
							slog.String("mount", string(entry.mountID)),
							slog.String("error", err.Error()))
						continue
					}
					return nil, err
				}
				if symlinkDir != "" {
					symlinkPath, err := createMountSymlink(symlinkDir, sourcePath)
					if err != nil {
						return nil, fmt.Errorf("create symlink for socket mount %s: %w", sourcePath, err)
					}
					socketSource = symlinkPath
				}
			}
			mounts = append(mounts, dockerMount{
				Source:   socketSource,
				Target:   mount.ContainerTarget,
				ReadOnly: false,
			})
		default:
			writable := mount.Mode == profile.CapabilityModeReadWrite
			actualSource, readOnly, err := backend.materializeBuiltinToolPath(sourcePath, writable, symlinkDir)
			if err != nil {
				if entry.optional {
					logger.Info("skipping optional builtin mount: host path not available",
						slog.String("mount", string(entry.mountID)),
						slog.String("error", err.Error()))
					continue
				}
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
	symlinkDir string,
) (string, bool, error) {
	if _, err := os.Stat(sourcePath); err != nil {
		return "", false, err
	}
	if symlinkDir != "" {
		symlinkPath, err := createMountSymlink(symlinkDir, sourcePath)
		if err != nil {
			return "", false, err
		}
		return symlinkPath, !writable, nil
	}
	return sourcePath, !writable, nil
}

func resolveCapabilityMountSource(mount profile.CapabilityMount) (string, error) {
	if mount.ID == profile.MountIDSSHAgent {
		// On macOS, Docker Desktop cannot bind-mount the native launchd SSH agent
		// socket. Use Docker Desktop's built-in magic path instead.
		if runtime.GOOS == "darwin" {
			return dockerDesktopSSHAgentSocket, nil
		}
		socketPath := os.Getenv("SSH_AUTH_SOCK")
		if socketPath == "" {
			return "", errors.New("SSH_AUTH_SOCK is required for ssh-agent tooling projection")
		}
		return filepath.Abs(socketPath)
	}
	if mount.ID == profile.MountIDPulseAudio {
		// Resolve PulseAudio socket from XDG_RUNTIME_DIR or fall back to /run/user/<uid>.
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
		}
		return filepath.Join(runtimeDir, "pulse", "native"), nil
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

// projectMacOSKeychainCredential extracts a credential from macOS Keychain and
// writes it to the host filesystem so that bind-mounting picks it up.
// On non-macOS platforms this is a no-op.
func projectMacOSKeychainCredential(logger *slog.Logger, mountSourceDir string, cred *profile.MacOSKeychainCredential) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	targetFile := filepath.Join(mountSourceDir, cred.RelPath)
	if _, err := os.Stat(targetFile); err == nil {
		return nil // already exists on the host filesystem
	}
	out, err := exec.Command("security", "find-generic-password", "-s", cred.ServiceName, "-w").Output()
	if err != nil {
		return fmt.Errorf("read credential from macOS Keychain (service=%s): %w", cred.ServiceName, err)
	}
	if err := os.WriteFile(targetFile, out, 0600); err != nil {
		return fmt.Errorf("write credential file %s: %w", targetFile, err)
	}
	logger.Info("projected credential from macOS Keychain",
		slog.String("service", cred.ServiceName),
		slog.String("target", targetFile))
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
		if path == "~" {
			return filepath.Abs(homeDir)
		}
		path = filepath.Join(homeDir, path[2:]) // skip "~/"
	}
	return filepath.Abs(path)
}

// validateTildePath rejects ~username syntax while allowing ~ and ~/... paths.
func validateTildePath(path string) error {
	if strings.HasPrefix(path, "~") && path != "~" && !strings.HasPrefix(path, "~/") {
		return fmt.Errorf("~username syntax is not supported: %s", path)
	}
	return nil
}

// expandContainerHomePath replaces a leading ~ with the container user home directory.
func expandContainerHomePath(path string) string {
	if path == "~" {
		return profile.ContainerUserHome
	}
	if strings.HasPrefix(path, "~/") {
		return profile.ContainerUserHome + path[1:] // replace ~ with ContainerUserHome, keep /rest
	}
	return path
}
