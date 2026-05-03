package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	nat "github.com/docker/go-connections/nat"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// execLogContainerDir is the container-side directory for exec log files when output redirection is enabled.
const execLogContainerDir = "/var/log/agents-sandbox/"

// supplementalGroupsEnv is reserved for daemon-to-entrypoint communication.
const supplementalGroupsEnv = "AGENTS_SANDBOX_SUPPLEMENTAL_GROUPS"

var macOSBlockedHostAliases = []string{
	"host.docker.internal:0.0.0.0",
	"gateway.docker.internal:0.0.0.0",
}

type dockerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type dockerPortMapping struct {
	ContainerPort uint32
	HostPort      uint32
	Protocol      string // "tcp", "udp", "sctp"
}

type dockerContainerSpec struct {
	Name         string
	Image        string
	NetworkName  string
	NetworkAlias string
	Labels       map[string]string
	Environment  map[string]string
	Healthcheck  *agboxv1.HealthcheckConfig
	Mounts       []dockerMount
	Ports        []dockerPortMapping
	Workdir      string
	Command      []string
	// CPUMillicores drives HostConfig.NanoCPUs (multiplied by 1_000_000).
	// Zero means no per-container CPU quota.
	CPUMillicores int64
	// MemoryBytes drives HostConfig.Memory in bytes. Zero means no
	// per-container memory quota. HostConfig.MemorySwap is intentionally
	// left at Docker's default (= 2 * Memory on hosts with swap).
	MemoryBytes int64
	// DiskSizeBytes drives HostConfig.StorageOpt["size"] in plain decimal
	// bytes. Zero means no per-container disk quota.
	DiskSizeBytes int64
	GPUs          string
	GroupAdd      []string
}

type dockerExecSpec struct {
	ContainerName string
	Command       []string
	Workdir       string
	Environment   map[string]string
	User          string // Override exec user; empty = container default
	LogDir        string // Container-side log directory; non-empty enables output redirection
	ExecID        string // Used to construct log file names when LogDir is set
	Stdout        io.Writer
	Stderr        io.Writer
}

var gpuDeviceGroupGlobPatterns = []string{
	"/dev/nvidia*",
	"/dev/dri/renderD*",
}

var gpuDeviceStat = os.Stat

func discoverGPUDeviceGroups() ([]string, error) {
	groupIDs := make(map[uint32]struct{})
	for _, pattern := range gpuDeviceGroupGlobPatterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("scan GPU device groups with pattern %q: %w", pattern, err)
		}
		for _, path := range paths {
			info, err := gpuDeviceStat(path)
			if err != nil {
				return nil, fmt.Errorf("stat GPU device %s: %w", path, err)
			}
			if info.Mode()&os.ModeDevice == 0 {
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil, fmt.Errorf("stat GPU device %s: unsupported stat type %T", path, info.Sys())
			}
			groupIDs[stat.Gid] = struct{}{}
		}
	}
	if len(groupIDs) == 0 {
		return nil, nil
	}
	sorted := make([]int, 0, len(groupIDs))
	for gid := range groupIDs {
		sorted = append(sorted, int(gid))
	}
	sort.Ints(sorted)
	groups := make([]string, 0, len(sorted))
	for _, gid := range sorted {
		groups = append(groups, strconv.Itoa(gid))
	}
	return groups, nil
}

// buildPortBindings translates dockerPortMapping entries into the Docker API
// PortSet/PortMap pair. Multiple PortMappings sharing the same
// (container_port, protocol) but differing host_port are appended into the
// same PortMap entry; direct assignment would silently drop earlier bindings.
func buildPortBindings(ports []dockerPortMapping) (nat.PortSet, nat.PortMap, error) {
	exposedPorts := make(nat.PortSet)
	portBindings := make(nat.PortMap)
	for _, p := range ports {
		natPort, err := nat.NewPort(p.Protocol, fmt.Sprintf("%d", p.ContainerPort))
		if err != nil {
			return nil, nil, fmt.Errorf("invalid port spec %d/%s: %w", p.ContainerPort, p.Protocol, err)
		}
		exposedPorts[natPort] = struct{}{}
		portBindings[natPort] = append(portBindings[natPort], nat.PortBinding{
			HostIP:   "127.0.0.1",
			HostPort: fmt.Sprintf("%d", p.HostPort),
		})
	}
	return exposedPorts, portBindings, nil
}

func ensureUniqueMountTargets(mounts []dockerMount) error {
	targets := make(map[string]string, len(mounts))
	for _, mount := range mounts {
		if mount.Target == "" {
			return errors.New("mount target is required")
		}
		if !filepath.IsAbs(mount.Target) {
			return fmt.Errorf("mount target must be absolute: %s", mount.Target)
		}
		if existingSource, exists := targets[mount.Target]; exists {
			return fmt.Errorf("conflicting mount target %s for %s and %s", mount.Target, existingSource, mount.Source)
		}
		targets[mount.Target] = mount.Source
	}
	return nil
}

func (backend *dockerRuntimeBackend) ensureDockerImage(ctx context.Context, imageRef string) error {
	if imageRef == "" {
		return errors.New("sandbox image must be configured")
	}
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	if _, _, err := backend.dockerClient.ImageInspectWithRaw(ctx, imageRef); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	reader, err := backend.dockerClient.ImagePull(ctx, imageRef, imagetypes.PullOptions{})
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(io.Discard, reader)
	return errors.Join(copyErr, reader.Close())
}

func (backend *dockerRuntimeBackend) dockerNetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	_, err := backend.dockerClient.NetworkCreate(ctx, name, network.CreateOptions{Labels: labels})
	return err
}

func (backend *dockerRuntimeBackend) dockerNetworkRemove(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	err := backend.dockerClient.NetworkRemove(ctx, name)
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (backend *dockerRuntimeBackend) dockerContainerCreate(ctx context.Context, spec dockerContainerSpec) error {
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	healthcheck, err := toContainerHealthConfig(spec.Healthcheck)
	if err != nil {
		return err
	}
	hostMounts := make([]mount.Mount, 0, len(spec.Mounts))
	for _, item := range spec.Mounts {
		hostMounts = append(hostMounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   item.Source,
			Target:   item.Target,
			ReadOnly: item.ReadOnly,
		})
	}
	exposedPorts, portBindings, err := buildPortBindings(spec.Ports)
	if err != nil {
		return err
	}
	hostConfig := &container.HostConfig{
		Init:         ptrTo(true),
		Mounts:       hostMounts,
		PortBindings: portBindings,
		GroupAdd:     append([]string(nil), spec.GroupAdd...),
		// Black-hole Docker Desktop's stable host-discovery aliases on macOS.
		// This is a DNS-layer best-effort control; Linux host isolation is
		// enforced separately via nftables on the sandbox network.
		ExtraHosts: append([]string(nil), macOSBlockedHostAliases...),
		// Auto-recover containers after host reboot or Docker daemon restart.
		// Explicit `docker stop` (e.g. via agbox sandbox stop) is still honored:
		// "unless-stopped" only skips restart for containers that were in the
		// stopped state, so stopped sandboxes stay stopped across reboots.
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
	}
	if spec.CPUMillicores > 0 {
		// 1 millicore = 1e6 NanoCPUs. HostConfig.NanoCPUs is Docker's native
		// per-container CPU quota (equivalent to `docker run --cpus`).
		hostConfig.Resources.NanoCPUs = spec.CPUMillicores * 1_000_000
	}
	if spec.MemoryBytes > 0 {
		hostConfig.Resources.Memory = spec.MemoryBytes
	}
	if spec.DiskSizeBytes > 0 {
		// Docker's storage-opt size= key takes the byte count as a plain
		// decimal string (no "g"/"m" suffix through the HTTP API).
		hostConfig.StorageOpt = map[string]string{"size": strconv.FormatInt(spec.DiskSizeBytes, 10)}
	}
	if spec.GPUs == "all" {
		hostConfig.Resources.DeviceRequests = []container.DeviceRequest{
			{
				Driver:       "nvidia",
				Count:        -1,
				Capabilities: [][]string{{"gpu"}},
			},
		}
	}
	var networkingConfig *network.NetworkingConfig
	if spec.NetworkName != "" {
		hostConfig.NetworkMode = container.NetworkMode(spec.NetworkName)
		endpointSettings := &network.EndpointSettings{}
		if spec.NetworkAlias != "" {
			endpointSettings.Aliases = []string{spec.NetworkAlias}
		}
		networkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				spec.NetworkName: endpointSettings,
			},
		}
	}
	_, err = backend.dockerClient.ContainerCreate(ctx, &container.Config{
		Image:        spec.Image,
		Cmd:          spec.Command,
		WorkingDir:   spec.Workdir,
		Env:          envMapToSlice(spec.Environment),
		Labels:       spec.Labels,
		Healthcheck:  healthcheck,
		ExposedPorts: exposedPorts,
	}, hostConfig, networkingConfig, nil, spec.Name)
	if err != nil {
		if translated := translateDiskLimitError(err); translated != nil {
			return translated
		}
		if len(spec.Ports) > 0 {
			// Wrap with port context so async error messages are actionable.
			ports := make([]string, 0, len(spec.Ports))
			for _, p := range spec.Ports {
				ports = append(ports, fmt.Sprintf("%d->%d/%s", p.HostPort, p.ContainerPort, p.Protocol))
			}
			return fmt.Errorf("failed to create container with port bindings [%s]: %w",
				strings.Join(ports, ", "), err)
		}
	}
	return err
}

// translateDiskLimitError rewrites Docker's native storage-opt prerequisite
// errors into a FailedPrecondition gRPC status with diagnostic hints. The
// substrings below come from moby/moby's ContainerCreate path (overlay2 size
// guard); revisit if Docker rewords them.
func translateDiskLimitError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, "storage-opt is supported only for overlay") &&
		!strings.Contains(msg, "inappropriate ioctl for device") {
		return nil
	}
	return status.Errorf(codes.FailedPrecondition,
		"disk_limit not supported by Docker runtime: %s. "+
			"Prerequisites: overlay2 storage driver on XFS with prjquota mount option and ftype=1. "+
			"Run: docker info --format '{{.Driver}} {{.DockerRootDir}}' and findmnt -T <DockerRootDir> to diagnose.",
		err)
}

func primaryContainerEnvironment(mounts []dockerMount) map[string]string {
	environment := map[string]string{
		"HOST_UID": strconv.Itoa(os.Getuid()),
		"HOST_GID": strconv.Itoa(os.Getgid()),
	}
	if hasMountedToolingTarget(mounts, "/ssh-agent") {
		environment["SSH_AUTH_SOCK"] = "/ssh-agent"
	}
	if hasMountedToolingTarget(mounts, "/pulse-audio") {
		environment["PULSE_SERVER"] = "unix:/pulse-audio"
	}
	return environment
}

func hasMountedToolingTarget(mounts []dockerMount, target string) bool {
	for _, mountValue := range mounts {
		if mountValue.Target == target {
			return true
		}
	}
	return false
}

func dockerNetworkName(sandboxID string) string {
	return "agbox-net-" + sanitizeRuntimeName(sandboxID)
}

func dockerPrimaryContainerName(sandboxID string) string {
	return "agbox-primary-" + sanitizeRuntimeName(sandboxID)
}

func dockerCompanionContainerName(sandboxID string, name string) string {
	return "agbox-cc-" + sanitizeRuntimeName(sandboxID) + "-" + sanitizeRuntimeName(name)
}

func sanitizeRuntimeName(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", ".", "-", "_", "-")
	return replacer.Replace(value)
}

func (backend *dockerRuntimeBackend) dockerContainerStart(ctx context.Context, name string) error {
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	return backend.dockerClient.ContainerStart(ctx, name, container.StartOptions{})
}

func (backend *dockerRuntimeBackend) dockerContainerEnsureRunning(ctx context.Context, name string) error {
	state, err := backend.dockerContainerState(ctx, name)
	if err != nil {
		return err
	}
	if state.Running {
		return nil
	}
	if err := backend.dockerContainerStart(ctx, name); err != nil {
		return err
	}
	return backend.dockerWaitContainerRunning(ctx, name, 10*time.Second)
}

func (backend *dockerRuntimeBackend) dockerWaitContainerRunning(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := backend.dockerContainerState(ctx, name)
		if err != nil {
			return err
		}
		if state.Running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("container %s did not become running", name)
}

func (backend *dockerRuntimeBackend) dockerContainerStop(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	state, err := backend.dockerContainerState(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !state.Running {
		return nil
	}
	timeout := 5
	err = backend.dockerClient.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (backend *dockerRuntimeBackend) dockerContainerRemove(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}
	err := backend.dockerClient.ContainerRemove(ctx, name, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (backend *dockerRuntimeBackend) dockerContainerMustExist(ctx context.Context, name string) error {
	_, err := backend.dockerContainerState(ctx, name)
	return err
}

func (backend *dockerRuntimeBackend) dockerContainerState(ctx context.Context, name string) (*container.State, error) {
	if backend == nil || backend.dockerClient == nil {
		return nil, errors.New("docker client is not initialized")
	}
	inspectResponse, err := backend.dockerClient.ContainerInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if inspectResponse.State == nil {
		return nil, fmt.Errorf("container %s does not expose structured state", name)
	}
	return inspectResponse.State, nil
}

func (backend *dockerRuntimeBackend) dockerExec(ctx context.Context, spec dockerExecSpec) (int32, error) {
	if len(spec.Command) == 0 {
		return 0, errors.New("exec command must not be empty")
	}
	if backend == nil || backend.dockerClient == nil {
		return 0, errors.New("docker client is not initialized")
	}

	cmd := spec.Command
	attachStdout := true
	attachStderr := true

	if spec.LogDir != "" {
		// Redirect stdout and stderr to per-exec log files inside the container.
		stdoutLog := filepath.Join(spec.LogDir, spec.ExecID+".stdout.log")
		stderrLog := filepath.Join(spec.LogDir, spec.ExecID+".stderr.log")
		shellCmd := fmt.Sprintf("exec \"$@\" >%s 2>%s", stdoutLog, stderrLog)
		cmd = append([]string{"sh", "-c", shellCmd, "--"}, spec.Command...)
		attachStdout = false
		attachStderr = false
	}

	createResponse, err := backend.dockerClient.ContainerExecCreate(ctx, spec.ContainerName, container.ExecOptions{
		AttachStdout: attachStdout,
		AttachStderr: attachStderr,
		Tty:          false,
		Env:          envMapToSlice(spec.Environment),
		WorkingDir:   spec.Workdir,
		User:         spec.User,
		Cmd:          cmd,
	})
	if err != nil {
		return 0, err
	}

	if spec.LogDir != "" {
		// Detached mode: start exec without attaching and poll for completion.
		if err := backend.dockerClient.ContainerExecStart(ctx, createResponse.ID, container.ExecStartOptions{Detach: true}); err != nil {
			return 0, err
		}
		return backend.pollExecCompletion(ctx, createResponse.ID)
	}

	// Attached mode drains output so completion is observed through stream closure.
	stdout := io.Discard
	if spec.Stdout != nil {
		stdout = spec.Stdout
	}
	stderr := io.Discard
	if spec.Stderr != nil {
		stderr = spec.Stderr
	}
	attachResponse, err := backend.dockerClient.ContainerExecAttach(ctx, createResponse.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return 0, err
	}
	defer attachResponse.Close()

	if _, err := stdcopy.StdCopy(stdout, stderr, attachResponse.Reader); err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, fmt.Errorf("read docker exec output: %w", err)
	}

	inspectResponse, err := backend.dockerClient.ContainerExecInspect(ctx, createResponse.ID)
	if err != nil {
		return -1, err
	}

	exitCode := int32(inspectResponse.ExitCode)
	if exitCode != 0 {
		return exitCode, fmt.Errorf("docker exec failed: exit code %d", exitCode)
	}
	return exitCode, nil
}

func (backend *dockerRuntimeBackend) pollExecCompletion(ctx context.Context, execID string) (int32, error) {
	for {
		inspect, err := backend.dockerClient.ContainerExecInspect(ctx, execID)
		if err != nil {
			return -1, err
		}
		if !inspect.Running {
			exitCode := int32(inspect.ExitCode)
			if exitCode != 0 {
				return exitCode, fmt.Errorf("docker exec failed: exit code %d", exitCode)
			}
			return exitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(backend.config.PollInterval):
		}
	}
}

func envMapToSlice(environment map[string]string) []string {
	if len(environment) == 0 {
		return nil
	}
	values := make([]string, 0, len(environment))
	for key, value := range environment {
		values = append(values, key+"="+value)
	}
	return values
}

// dockerCopyToContainer copies the source tree into the container at the given target path,
// applying exclude patterns. The container must be created but may or may not be started.
// Symlinks within the source tree are preserved in the tar stream.
func (backend *dockerRuntimeBackend) dockerCopyToContainer(ctx context.Context, containerName string, copy deferredCopy) error {
	if backend == nil || backend.dockerClient == nil {
		return errors.New("docker client is not initialized")
	}

	pipeReader, pipeWriter := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- buildCopyTar(pipeWriter, copy.SourcePath, copy.ExcludePatterns)
	}()

	err := backend.dockerClient.CopyToContainer(ctx, containerName, copy.ContainerTarget, pipeReader, container.CopyToContainerOptions{})
	tarErr := <-errCh
	if err != nil {
		return fmt.Errorf("copy to container %s at %s: %w", containerName, copy.ContainerTarget, err)
	}
	if tarErr != nil {
		return fmt.Errorf("build tar for %s: %w", copy.SourcePath, tarErr)
	}
	return nil
}

// portProtocolToString converts a proto PortProtocol enum to a Docker protocol
// string. Returns empty string for unknown values so callers can detect invalid input.
func portProtocolToString(protocol agboxv1.PortProtocol) string {
	switch protocol {
	case agboxv1.PortProtocol_PORT_PROTOCOL_TCP:
		return "tcp"
	case agboxv1.PortProtocol_PORT_PROTOCOL_UDP:
		return "udp"
	case agboxv1.PortProtocol_PORT_PROTOCOL_SCTP:
		return "sctp"
	default:
		return ""
	}
}

func ptrTo[T any](value T) *T {
	return &value
}
