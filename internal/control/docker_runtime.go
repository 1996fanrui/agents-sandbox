package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

type runtimeBackend interface {
	CreateSandbox(context.Context, *sandboxRecord) (runtimeCreateResult, error)
	ResumeSandbox(context.Context, *sandboxRecord) (runtimeResumeResult, error)
	StopSandbox(context.Context, *sandboxRecord) error
	DeleteSandbox(context.Context, *sandboxRecord) error
	RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error)
}

type runtimeCreateResult struct {
	ServiceStatuses         []runtimeServiceStatus
	OptionalServiceStatuses <-chan runtimeServiceStatus
	RuntimeState            *sandboxRuntimeState
}

type runtimeResumeResult struct {
	ServiceStatuses []runtimeServiceStatus
}

type runtimeServiceStatus struct {
	Name     string
	Required bool
	Ready    bool
	Message  string
}

type optionalServiceStarts struct {
	Statuses <-chan runtimeServiceStatus
	done     <-chan struct{}
	cancel   context.CancelFunc
}

func (starts optionalServiceStarts) CancelAndWait() {
	if starts.cancel != nil {
		starts.cancel()
	}
	if starts.done != nil {
		<-starts.done
	}
}

type runtimeExecResult struct {
	ExitCode int32
	Stdout   string
	Stderr   string
}

type sandboxRuntimeState struct {
	NetworkName           string
	PrimaryContainerName  string
	ServiceContainers     []runtimeServiceContainer
	ShadowRoot            string
	OptionalServiceStarts optionalServiceStarts
}

type runtimeServiceContainer struct {
	Name          string
	ContainerName string
	Required      bool
}

type fakeRuntimeBackend struct{}

func (fakeRuntimeBackend) CreateSandbox(_ context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	statuses := make([]runtimeServiceStatus, 0, len(record.requiredServices)+len(record.optionalServices))
	containers := make([]runtimeServiceContainer, 0, len(record.requiredServices)+len(record.optionalServices))
	for _, service := range record.requiredServices {
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: true, Ready: true})
		containers = append(containers, runtimeServiceContainer{
			Name:          service.GetName(),
			ContainerName: "fake-service-" + record.handle.GetSandboxId() + "-" + sanitizeRuntimeName(service.GetName()),
			Required:      true,
		})
	}
	for _, service := range record.optionalServices {
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: false, Ready: true})
		containers = append(containers, runtimeServiceContainer{
			Name:          service.GetName(),
			ContainerName: "fake-service-" + record.handle.GetSandboxId() + "-" + sanitizeRuntimeName(service.GetName()),
			Required:      false,
		})
	}
	return runtimeCreateResult{
		ServiceStatuses: statuses,
		RuntimeState: &sandboxRuntimeState{
			NetworkName:          "fake-network-" + record.handle.GetSandboxId(),
			PrimaryContainerName: "fake-primary-" + record.handle.GetSandboxId(),
			ServiceContainers:    containers,
		},
	}, nil
}

func (fakeRuntimeBackend) ResumeSandbox(_ context.Context, record *sandboxRecord) (runtimeResumeResult, error) {
	statuses := make([]runtimeServiceStatus, 0, len(record.requiredServices)+len(record.optionalServices))
	for _, service := range record.requiredServices {
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: true, Ready: true})
	}
	for _, service := range record.optionalServices {
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: false, Ready: true})
	}
	return runtimeResumeResult{ServiceStatuses: statuses}, nil
}
func (fakeRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error   { return nil }
func (fakeRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error { return nil }

func (fakeRuntimeBackend) RunExec(_ context.Context, _ *sandboxRecord, _ *agboxv1.ExecStatus) (runtimeExecResult, error) {
	return runtimeExecResult{ExitCode: 0}, nil
}

type dockerRuntimeBackend struct {
	config       ServiceConfig
	dockerClient *client.Client
}

func newDockerRuntimeBackend(config ServiceConfig) (runtimeBackend, io.Closer, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, fmt.Errorf("initialize docker client: %w", err)
	}
	backend := &dockerRuntimeBackend{
		config:       config,
		dockerClient: dockerClient,
	}
	return backend, backend, nil
}

func (backend *dockerRuntimeBackend) Close() error {
	if backend == nil || backend.dockerClient == nil {
		return nil
	}
	return backend.dockerClient.Close()
}

func (backend *dockerRuntimeBackend) CreateSandbox(ctx context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	state := &sandboxRuntimeState{
		NetworkName:          dockerNetworkName(record.handle.GetSandboxId()),
		PrimaryContainerName: dockerPrimaryContainerName(record.handle.GetSandboxId()),
	}
	cleanupRequired := false
	var optionalStarts optionalServiceStarts
	defer func() {
		if !cleanupRequired {
			return
		}
		optionalStarts.CancelAndWait()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = backend.deleteRuntimeArtifacts(cleanupCtx, state)
	}()

	mounts, err := backend.materializeBuiltinResources(record.handle.GetSandboxId(), record.createSpec.GetBuiltinResources(), state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	genericMounts, err := backend.materializeGenericMounts(record.createSpec.GetMounts())
	if err != nil {
		return runtimeCreateResult{}, err
	}
	mounts = append(mounts, genericMounts...)
	copyMounts, err := backend.materializeGenericCopies(record.handle.GetSandboxId(), record.createSpec.GetCopies(), state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	mounts = append(mounts, copyMounts...)
	if err := ensureUniqueMountTargets(mounts); err != nil {
		return runtimeCreateResult{}, err
	}

	cleanupRequired = true
	if err := backend.ensureDockerImage(ctx, record.createSpec.GetImage()); err != nil {
		return runtimeCreateResult{}, err
	}
	for _, service := range record.requiredServices {
		if err := backend.ensureDockerImage(ctx, service.GetImage()); err != nil {
			return runtimeCreateResult{}, err
		}
	}
	for _, service := range record.optionalServices {
		if err := backend.ensureDockerImage(ctx, service.GetImage()); err != nil {
			return runtimeCreateResult{}, err
		}
	}
	userLabels := record.handle.GetLabels()
	if err := backend.dockerNetworkCreate(ctx, state.NetworkName, runtimedocker.SandboxLabels(record.handle.GetSandboxId(), "default", userLabels)); err != nil {
		return runtimeCreateResult{}, err
	}

	statuses := make([]runtimeServiceStatus, 0, len(record.requiredServices)+len(record.optionalServices))
	state.ServiceContainers = make([]runtimeServiceContainer, 0, len(record.requiredServices)+len(record.optionalServices))
	for _, service := range record.requiredServices {
		containerName := dockerServiceContainerName(record.handle.GetSandboxId(), service.GetName())
		state.ServiceContainers = append(state.ServiceContainers, runtimeServiceContainer{
			Name:          service.GetName(),
			ContainerName: containerName,
			Required:      true,
		})
		if err := backend.dockerContainerCreate(ctx, dockerContainerSpec{
			Name:         containerName,
			Image:        service.GetImage(),
			NetworkName:  state.NetworkName,
			NetworkAlias: service.GetName(),
			Labels:       runtimedocker.ServiceLabels(record.handle.GetSandboxId(), service.GetName(), userLabels),
			Environment:  keyValuesToMap(service.GetEnvironment()),
			Healthcheck:  service.GetHealthcheck(),
		}); err != nil {
			return runtimeCreateResult{}, err
		}
		if err := backend.dockerContainerStart(ctx, containerName); err != nil {
			return runtimeCreateResult{}, err
		}
		if err := backend.dockerWaitRequiredServiceHealthy(ctx, containerName, service.GetHealthcheck()); err != nil {
			return runtimeCreateResult{}, err
		}
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: true, Ready: true})
	}

	optionalStarts = startOptionalServicesAsync(ctx, record.handle.GetSandboxId(), state.NetworkName, record.optionalServices, userLabels, func(ctx context.Context, spec dockerContainerSpec) error {
		return backend.dockerContainerCreate(ctx, spec)
	}, func(ctx context.Context, name string) error {
		return backend.dockerContainerStart(ctx, name)
	})
	for _, service := range record.optionalServices {
		state.ServiceContainers = append(state.ServiceContainers, runtimeServiceContainer{
			Name:          service.GetName(),
			ContainerName: dockerServiceContainerName(record.handle.GetSandboxId(), service.GetName()),
			Required:      false,
		})
	}

	if err := backend.dockerContainerCreate(ctx, dockerContainerSpec{
		Name:        state.PrimaryContainerName,
		Image:       record.createSpec.GetImage(),
		NetworkName: state.NetworkName,
		Labels:      runtimedocker.SandboxLabels(record.handle.GetSandboxId(), "default", userLabels),
		Mounts:      mounts,
		Environment: primaryContainerEnvironment(mounts),
		Workdir:     "/workspace",
		Command: []string{
			"sh",
			"-lc",
			"trap 'exit 0' TERM INT; while sleep 3600; do :; done",
		},
	}); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := backend.dockerContainerStart(ctx, state.PrimaryContainerName); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := backend.dockerWaitContainerRunning(ctx, state.PrimaryContainerName, 10*time.Second); err != nil {
		return runtimeCreateResult{}, err
	}
	for _, service := range record.requiredServices {
		for _, hook := range service.GetPostStartOnPrimary() {
			if _, _, err := backend.dockerExec(ctx, dockerExecSpec{
				ContainerName: state.PrimaryContainerName,
				Command:       []string{"sh", "-lc", hook},
			}); err != nil {
				return runtimeCreateResult{}, err
			}
		}
	}
	cleanupRequired = false
	state.OptionalServiceStarts = optionalStarts
	return runtimeCreateResult{
		ServiceStatuses:         statuses,
		OptionalServiceStatuses: optionalStarts.Statuses,
		RuntimeState:            state,
	}, nil
}

func (backend *dockerRuntimeBackend) ResumeSandbox(ctx context.Context, record *sandboxRecord) (runtimeResumeResult, error) {
	if record.runtimeState == nil {
		return runtimeResumeResult{}, errors.New("sandbox runtime state is missing")
	}
	if err := backend.dockerContainerMustExist(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeResumeResult{}, err
	}
	for _, serviceContainer := range record.runtimeState.ServiceContainers {
		if err := backend.dockerContainerMustExist(ctx, serviceContainer.ContainerName); err != nil {
			return runtimeResumeResult{}, err
		}
	}
	statuses := make([]runtimeServiceStatus, 0, len(record.runtimeState.ServiceContainers))
	for _, service := range record.requiredServices {
		containerName := dockerServiceContainerName(record.handle.GetSandboxId(), service.GetName())
		if err := backend.dockerContainerEnsureRunning(ctx, containerName); err != nil {
			return runtimeResumeResult{}, err
		}
		if err := backend.dockerWaitRequiredServiceHealthy(ctx, containerName, service.GetHealthcheck()); err != nil {
			return runtimeResumeResult{}, err
		}
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: true, Ready: true})
	}
	if err := backend.dockerContainerEnsureRunning(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeResumeResult{}, err
	}
	for _, service := range record.requiredServices {
		for _, hook := range service.GetPostStartOnPrimary() {
			if _, _, err := backend.dockerExec(ctx, dockerExecSpec{
				ContainerName: record.runtimeState.PrimaryContainerName,
				Command:       []string{"sh", "-lc", hook},
			}); err != nil {
				return runtimeResumeResult{}, err
			}
		}
	}
	for _, service := range record.optionalServices {
		containerName := dockerServiceContainerName(record.handle.GetSandboxId(), service.GetName())
		if err := backend.dockerContainerEnsureRunning(ctx, containerName); err != nil {
			statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: false, Ready: false, Message: err.Error()})
			continue
		}
		statuses = append(statuses, runtimeServiceStatus{Name: service.GetName(), Required: false, Ready: true})
	}
	return runtimeResumeResult{ServiceStatuses: statuses}, nil
}

func startOptionalServicesAsync(
	ctx context.Context,
	sandboxID string,
	networkName string,
	services []*agboxv1.ServiceSpec,
	userLabels map[string]string,
	createContainer func(context.Context, dockerContainerSpec) error,
	startContainer func(context.Context, string) error,
) optionalServiceStarts {
	optionalCtx, cancel := context.WithCancel(ctx)
	results := make(chan runtimeServiceStatus, len(services))
	done := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(services))
	for _, service := range services {
		containerName := dockerServiceContainerName(sandboxID, service.GetName())
		go func(service *agboxv1.ServiceSpec, containerName string) {
			defer waitGroup.Done()
			status := runtimeServiceStatus{Name: service.GetName(), Required: false, Ready: true}
			if err := createContainer(optionalCtx, dockerContainerSpec{
				Name:         containerName,
				Image:        service.GetImage(),
				NetworkName:  networkName,
				NetworkAlias: service.GetName(),
				Labels:       runtimedocker.ServiceLabels(sandboxID, service.GetName(), userLabels),
				Environment:  keyValuesToMap(service.GetEnvironment()),
				Healthcheck:  service.GetHealthcheck(),
			}); err != nil {
				status.Ready = false
				status.Message = err.Error()
				results <- status
				return
			}
			if err := startContainer(optionalCtx, containerName); err != nil {
				status.Ready = false
				status.Message = err.Error()
			}
			results <- status
		}(service, containerName)
	}
	go func() {
		waitGroup.Wait()
		close(results)
		close(done)
	}()
	return optionalServiceStarts{
		Statuses: results,
		done:     done,
		cancel:   cancel,
	}
}

func collectRuntimeServiceStatuses(results <-chan runtimeServiceStatus) []runtimeServiceStatus {
	statuses := make([]runtimeServiceStatus, 0)
	for result := range results {
		statuses = append(statuses, result)
	}
	return statuses
}

func (backend *dockerRuntimeBackend) StopSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return errors.New("sandbox runtime state is missing")
	}
	record.runtimeState.OptionalServiceStarts.CancelAndWait()
	if err := backend.dockerContainerStop(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return err
	}
	for _, serviceContainer := range record.runtimeState.ServiceContainers {
		if err := backend.dockerContainerStop(ctx, serviceContainer.ContainerName); err != nil {
			return err
		}
	}
	return nil
}

func (backend *dockerRuntimeBackend) DeleteSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return nil
	}
	record.runtimeState.OptionalServiceStarts.CancelAndWait()
	return backend.deleteRuntimeArtifacts(ctx, record.runtimeState)
}

func (backend *dockerRuntimeBackend) deleteRuntimeArtifacts(ctx context.Context, state *sandboxRuntimeState) error {
	var joined []error
	if state == nil {
		return nil
	}
	if state.PrimaryContainerName != "" {
		joined = append(joined, backend.dockerContainerRemove(ctx, state.PrimaryContainerName))
	}
	for _, serviceContainer := range state.ServiceContainers {
		joined = append(joined, backend.dockerContainerRemove(ctx, serviceContainer.ContainerName))
	}
	if state.NetworkName != "" {
		joined = append(joined, backend.dockerNetworkRemove(ctx, state.NetworkName))
	}
	if state.ShadowRoot != "" {
		joined = append(joined, os.RemoveAll(state.ShadowRoot))
	}
	return errors.Join(joined...)
}

func (backend *dockerRuntimeBackend) RunExec(ctx context.Context, record *sandboxRecord, execRecord *agboxv1.ExecStatus) (runtimeExecResult, error) {
	if record.runtimeState == nil {
		return runtimeExecResult{}, errors.New("sandbox runtime state is missing")
	}
	if err := backend.dockerContainerMustExist(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeExecResult{}, err
	}
	if err := backend.dockerContainerEnsureRunning(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeExecResult{}, err
	}
	output, exitCode, err := backend.dockerExec(ctx, dockerExecSpec{
		ContainerName: record.runtimeState.PrimaryContainerName,
		Command:       execRecord.GetCommand(),
		Workdir:       execRecord.GetCwd(),
		Environment:   keyValuesToMap(execRecord.GetEnvOverrides()),
	})
	return runtimeExecResult{
		ExitCode: exitCode,
		Stdout:   output.Stdout,
		Stderr:   output.Stderr,
	}, err
}

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
		sourcePath, err := filepath.Abs(request.GetSource())
		if err != nil {
			return nil, err
		}
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
		sourcePath, err := filepath.Abs(request.GetSource())
		if err != nil {
			return nil, err
		}
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

func (backend *dockerRuntimeBackend) materializeBuiltinResources(
	sandboxID string,
	resources []string,
	state *sandboxRuntimeState,
) ([]dockerMount, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	mounts := make([]dockerMount, 0, len(resources))
	for _, resource := range resources {
		capability, ok := profile.CapabilityByID(resource)
		if !ok {
			return nil, fmt.Errorf("unknown builtin resource %q", resource)
		}
		sourcePath, err := resolveCapabilitySource(capability)
		if err != nil {
			return nil, err
		}
		switch capability.Mode {
		case profile.CapabilityModeSocket:
			if err := requireSocketPath(sourcePath); err != nil {
				return nil, err
			}
			mounts = append(mounts, dockerMount{
				Source:   sourcePath,
				Target:   capability.ContainerTarget,
				ReadOnly: false,
			})
		default:
			writable := capability.Mode == profile.CapabilityModeReadWrite
			actualSource, readOnly, err := backend.materializeBuiltinResourcePath(sandboxID, capability, sourcePath, writable, state)
			if err != nil {
				return nil, err
			}
			mounts = append(mounts, dockerMount{
				Source:   actualSource,
				Target:   capability.ContainerTarget,
				ReadOnly: readOnly,
			})
		}
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) materializeBuiltinResourcePath(
	sandboxID string,
	capability profile.ToolingCapability,
	sourcePath string,
	writable bool,
	state *sandboxRuntimeState,
) (string, bool, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", false, err
	}
	actualSource := sourcePath
	readOnly := !writable
	if info.IsDir() {
		resolution, err := runtimedocker.ResolveProjectionMode(sourcePath, []string{sourcePath}, writable)
		if err != nil {
			return "", false, err
		}
		if resolution.Mode == runtimedocker.ProjectionModeShadowCopy {
			if backend.config.StateRoot == "" {
				return "", false, errors.New("runtime.state_root is required for builtin resource shadow copies")
			}
			if state.ShadowRoot == "" {
				state.ShadowRoot = filepath.Join(backend.config.StateRoot, "sandboxes", sandboxID, "shadow")
			}
			actualSource = filepath.Join(state.ShadowRoot, "builtin", sanitizeRuntimeName(capability.ID))
			if err := os.RemoveAll(actualSource); err != nil {
				return "", false, err
			}
			if err := copyTreeAllowExternalSymlinks(sourcePath, actualSource); err != nil {
				return "", false, err
			}
		}
	}
	return actualSource, readOnly, nil
}

func resolveCapabilitySource(capability profile.ToolingCapability) (string, error) {
	if capability.DefaultHostPath == "SSH_AUTH_SOCK" {
		socketPath := os.Getenv("SSH_AUTH_SOCK")
		if socketPath == "" {
			return "", errors.New("SSH_AUTH_SOCK is required for ssh-agent tooling projection")
		}
		return filepath.Abs(socketPath)
	}
	return expandHomePath(capability.DefaultHostPath)
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

func keyValuesToMap(items []*agboxv1.KeyValue) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		result[item.GetKey()] = item.GetValue()
	}
	return result
}

func hasMountedToolingTarget(mounts []dockerMount, target string) bool {
	for _, mount := range mounts {
		if mount.Target == target {
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

func copyTree(sourceRoot string, targetRoot string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, nil, false)
}

func copyTreeAllowExternalSymlinks(sourceRoot string, targetRoot string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, nil, true)
}

func copyTreeWithPatterns(sourceRoot string, targetRoot string, excludePatterns []string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, excludePatterns, false)
}

func copyTreeWithOptions(sourceRoot string, targetRoot string, excludePatterns []string, allowExternalSymlinks bool) error {
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return copyFile(sourceRoot, targetRoot, sourceInfo.Mode())
	}
	rootAbs, err := filepath.Abs(sourceRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(currentSource string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, currentSource)
		if err != nil {
			return err
		}
		currentTarget := targetRoot
		if relativePath != "." {
			currentTarget = filepath.Join(targetRoot, relativePath)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if relativePath != "." && matchesExcludePattern(relativePath, excludePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(currentTarget, info.Mode())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, copyResolved, resolvedTarget, err := rewriteCopiedSymlink(rootAbs, targetRoot, currentSource, currentTarget, allowExternalSymlinks)
			if err != nil {
				return err
			}
			if copyResolved {
				resolvedInfo, err := os.Stat(resolvedTarget)
				if err != nil {
					return err
				}
				if resolvedInfo.IsDir() {
					return copyTreeWithOptions(resolvedTarget, currentTarget, nil, allowExternalSymlinks)
				}
				return copyFile(resolvedTarget, currentTarget, resolvedInfo.Mode())
			}
			if err := os.MkdirAll(filepath.Dir(currentTarget), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, currentTarget)
		}
		return copyFile(currentSource, currentTarget, info.Mode())
	})
}

func rewriteCopiedSymlink(
	sourceRoot string,
	targetRoot string,
	currentSource string,
	currentTarget string,
	allowExternalSymlinks bool,
) (string, bool, string, error) {
	target, err := os.Readlink(currentSource)
	if err != nil {
		return "", false, "", err
	}
	resolvedTarget, err := runtimedocker.ResolveLinkTarget(currentSource)
	if err != nil {
		return "", false, "", err
	}
	if !pathWithinRoot(sourceRoot, resolvedTarget) {
		if !allowExternalSymlinks {
			return "", false, "", fmt.Errorf("copy source contains external symlink: %s", currentSource)
		}
		return "", true, resolvedTarget, nil
	}
	if filepath.IsAbs(target) {
		relativeTarget, err := filepath.Rel(sourceRoot, resolvedTarget)
		if err != nil {
			return "", false, "", err
		}
		rewrittenTarget, err := filepath.Rel(filepath.Dir(currentTarget), filepath.Join(targetRoot, relativeTarget))
		return rewrittenTarget, false, "", err
	}
	return target, false, "", nil
}

func matchesExcludePattern(relativePath string, patterns []string) bool {
	base := filepath.Base(relativePath)
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if matched, err := filepath.Match(pattern, relativePath); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(pattern, base); err == nil && matched {
			return true
		}
	}
	return false
}

func copyFile(sourcePath string, targetPath string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer targetFile.Close()
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return err
	}
	return nil
}

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

func (backend *dockerRuntimeBackend) dockerWaitRequiredServiceHealthy(ctx context.Context, name string, healthcheck *agboxv1.HealthcheckConfig) error {
	if healthcheck == nil {
		return fmt.Errorf("required service %s is missing healthcheck", name)
	}
	upperBound, err := requiredServiceHealthWaitUpperBound(healthcheck)
	if err != nil {
		return fmt.Errorf("compute health wait upper bound for %s: %w", name, err)
	}
	deadline := time.Now().Add(upperBound)
	var lastLogTime time.Time
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			state, err := backend.dockerContainerState(ctx, name)
			if err != nil {
				return err
			}
			if !state.Running {
				return fmt.Errorf("container %s is not running while waiting for health", name)
			}
			if state.Health == nil {
				return fmt.Errorf("container %s does not expose structured health state", name)
			}
			// Structured health fields are the source of truth.
			healthStatus := strings.ToLower(strings.TrimSpace(state.Health.Status))
			failingStreak := state.Health.FailingStreak
			latestLogTime := latestHealthLogTimestamp(state.Health.Log)
			if !latestLogTime.IsZero() {
				lastLogTime = latestLogTime
			}
			if healthStatus == "healthy" {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf(
					"service %s did not become healthy within %s (status=%s failing_streak=%d last_log=%s)",
					name,
					upperBound,
					healthStatus,
					failingStreak,
					lastLogTime.UTC().Format(time.RFC3339Nano),
				)
			}
		}
	}
}

func requiredServiceHealthWaitUpperBound(healthcheck *agboxv1.HealthcheckConfig) (time.Duration, error) {
	const (
		defaultInterval      = 30 * time.Second
		defaultTimeout       = 30 * time.Second
		defaultStartInterval = 5 * time.Second
		defaultRetries       = uint32(3)
		maxUpperBound        = 5 * time.Minute
	)
	startPeriod, err := parseHealthDuration(healthcheck.GetStartPeriod(), 0)
	if err != nil {
		return 0, err
	}
	interval, err := parseHealthDuration(healthcheck.GetInterval(), defaultInterval)
	if err != nil {
		return 0, err
	}
	timeout, err := parseHealthDuration(healthcheck.GetTimeout(), defaultTimeout)
	if err != nil {
		return 0, err
	}
	startIntervalDefault := time.Duration(0)
	if startPeriod > 0 {
		startIntervalDefault = defaultStartInterval
	}
	startInterval, err := parseHealthDuration(healthcheck.GetStartInterval(), startIntervalDefault)
	if err != nil {
		return 0, err
	}
	retries := healthcheck.GetRetries()
	if retries == 0 {
		retries = defaultRetries
	}
	startupGraceCheckWindow := time.Duration(0)
	if startPeriod > 0 {
		startupGraceCheckWindow = maxDuration(startInterval, timeout)
	}
	countedCheckWindow := maxDuration(interval, timeout)
	theoreticalUpperBound := startPeriod + startupGraceCheckWindow + countedCheckWindow*time.Duration(retries+1)
	return minDuration(theoreticalUpperBound, maxUpperBound), nil
}

func parseHealthDuration(raw string, defaultValue time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	return time.ParseDuration(raw)
}

func latestHealthLogTimestamp(items []*container.HealthcheckResult) time.Time {
	var latest time.Time
	for _, item := range items {
		if item == nil {
			continue
		}
		candidate := item.End
		if candidate.IsZero() {
			candidate = item.Start
		}
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func maxDuration(left time.Duration, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func minDuration(left time.Duration, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
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

type dockerExecOutput struct {
	Stdout string
	Stderr string
}

func (backend *dockerRuntimeBackend) dockerExec(ctx context.Context, spec dockerExecSpec) (dockerExecOutput, int32, error) {
	if len(spec.Command) == 0 {
		return dockerExecOutput{}, 0, errors.New("exec command must not be empty")
	}
	if backend == nil || backend.dockerClient == nil {
		return dockerExecOutput{}, 0, errors.New("docker client is not initialized")
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
		return dockerExecOutput{}, 0, err
	}

	attachResponse, err := backend.dockerClient.ContainerExecAttach(ctx, createResponse.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return dockerExecOutput{}, 0, err
	}
	defer attachResponse.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResponse.Reader); err != nil {
		if ctx.Err() != nil {
			return dockerExecOutput{}, -1, ctx.Err()
		}
		return dockerExecOutput{
			Stdout: strings.TrimSpace(stdout.String()),
			Stderr: strings.TrimSpace(stderr.String()),
		}, -1, fmt.Errorf("read docker exec output: %w", err)
	}

	inspectResponse, err := backend.dockerClient.ContainerExecInspect(ctx, createResponse.ID)
	output := dockerExecOutput{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	if err != nil {
		return output, -1, err
	}

	exitCode := int32(inspectResponse.ExitCode)
	if exitCode != 0 {
		message := strings.TrimSpace(strings.Join([]string{output.Stderr, output.Stdout}, "\n"))
		if message == "" {
			message = fmt.Sprintf("exit code %d", exitCode)
		}
		return output, exitCode, fmt.Errorf("docker exec failed: %s", message)
	}
	return output, exitCode, nil
}

func toContainerHealthConfig(healthcheck *agboxv1.HealthcheckConfig) (*container.HealthConfig, error) {
	if healthcheck == nil {
		return nil, nil
	}
	config := &container.HealthConfig{
		Test: append([]string(nil), healthcheck.GetTest()...),
	}
	if healthcheck.GetInterval() != "" {
		interval, err := time.ParseDuration(healthcheck.GetInterval())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck interval: %w", err)
		}
		config.Interval = interval
	}
	if healthcheck.GetTimeout() != "" {
		timeout, err := time.ParseDuration(healthcheck.GetTimeout())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck timeout: %w", err)
		}
		config.Timeout = timeout
	}
	if healthcheck.GetStartPeriod() != "" {
		startPeriod, err := time.ParseDuration(healthcheck.GetStartPeriod())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck start period: %w", err)
		}
		config.StartPeriod = startPeriod
	}
	if healthcheck.GetStartInterval() != "" {
		startInterval, err := time.ParseDuration(healthcheck.GetStartInterval())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck start interval: %w", err)
		}
		config.StartInterval = startInterval
	}
	if healthcheck.GetRetries() > 0 {
		config.Retries = int(healthcheck.GetRetries())
	}
	return config, nil
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
