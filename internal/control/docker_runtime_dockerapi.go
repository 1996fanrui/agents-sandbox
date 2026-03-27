package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

type dockerMount struct {
	Source   string
	Target   string
	ReadOnly bool
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
	Workdir      string
	Command      []string
}

type dockerExecSpec struct {
	ContainerName string
	Command       []string
	Workdir       string
	Environment   map[string]string
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
	hostConfig := &container.HostConfig{
		Init:   ptrTo(true),
		Mounts: hostMounts,
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
		Image:       spec.Image,
		Cmd:         spec.Command,
		WorkingDir:  spec.Workdir,
		Env:         envMapToSlice(spec.Environment),
		Labels:      spec.Labels,
		Healthcheck: healthcheck,
	}, hostConfig, networkingConfig, nil, spec.Name)
	return err
}

func primaryContainerEnvironment(mounts []dockerMount) map[string]string {
	environment := map[string]string{
		"HOST_UID": strconv.Itoa(os.Getuid()),
		"HOST_GID": strconv.Itoa(os.Getgid()),
	}
	if hasMountedToolingTarget(mounts, "/ssh-agent") {
		environment["SSH_AUTH_SOCK"] = "/ssh-agent"
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

func dockerServiceContainerName(sandboxID string, serviceName string) string {
	return "agbox-svc-" + sanitizeRuntimeName(sandboxID) + "-" + sanitizeRuntimeName(serviceName)
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

	createResponse, err := backend.dockerClient.ContainerExecCreate(ctx, spec.ContainerName, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Env:          envMapToSlice(spec.Environment),
		WorkingDir:   spec.Workdir,
		Cmd:          spec.Command,
	})
	if err != nil {
		return 0, err
	}

	attachResponse, err := backend.dockerClient.ContainerExecAttach(ctx, createResponse.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return 0, err
	}
	defer attachResponse.Close()

	// Drain output to io.Discard so the exec completes; output is captured via bind-mounted log files.
	if _, err := stdcopy.StdCopy(io.Discard, io.Discard, attachResponse.Reader); err != nil {
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

func ptrTo[T any](value T) *T {
	return &value
}
