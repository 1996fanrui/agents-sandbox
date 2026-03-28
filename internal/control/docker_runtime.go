package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

type runtimeBackend interface {
	CreateSandbox(context.Context, *sandboxRecord) (runtimeCreateResult, error)
	ResumeSandbox(context.Context, *sandboxRecord) (runtimeResumeResult, error)
	StopSandbox(context.Context, *sandboxRecord) error
	DeleteSandbox(context.Context, *sandboxRecord) error
	RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error)
	InspectContainer(ctx context.Context, containerName string) (ContainerInspectResult, error)
	WatchContainerEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error)
}

// ContainerEvent represents a Docker container lifecycle event relevant to sandbox state management.
type ContainerEvent struct {
	SandboxID     string
	ContainerName string
	Action        string // "die" or "oom"
	IsService     bool
	ServiceName   string
}

// ContainerInspectResult holds the state of a single container queried via Docker inspect.
type ContainerInspectResult struct {
	Exists    bool
	Running   bool
	ExitCode  int
	OOMKilled bool
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

type fakeRuntimeBackend struct {
	inspectResults map[string]ContainerInspectResult
	eventCh        chan ContainerEvent
	errCh          chan error
}

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

func (backend fakeRuntimeBackend) InspectContainer(_ context.Context, containerName string) (ContainerInspectResult, error) {
	if backend.inspectResults != nil {
		if result, ok := backend.inspectResults[containerName]; ok {
			return result, nil
		}
	}
	return ContainerInspectResult{}, nil
}

func (backend fakeRuntimeBackend) WatchContainerEvents(_ context.Context) (<-chan ContainerEvent, <-chan error) {
	if backend.eventCh != nil {
		return backend.eventCh, backend.errCh
	}
	return make(chan ContainerEvent), make(chan error)
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
	// Bind-mount exec log directory into the primary container so exec output is written directly to the host.
	// The directory is pre-created by the service layer before calling this function.
	if backend.config.ArtifactOutputRoot != "" {
		execLogHostDir := filepath.Join(backend.config.ArtifactOutputRoot, record.handle.GetSandboxId())
		mounts = append(mounts, dockerMount{
			Source:   execLogHostDir,
			Target:   execLogContainerDir,
			ReadOnly: false,
		})
	}
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

	optionalStarts = startOptionalServicesAsync(ctx, record.handle.GetSandboxId(), state.NetworkName, state.PrimaryContainerName, record.optionalServices, userLabels, backend, func(ctx context.Context, spec dockerContainerSpec) error {
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

	primaryEnv := primaryContainerEnvironment(mounts)
	for _, kv := range record.createSpec.GetEnvs() {
		primaryEnv[kv.GetKey()] = kv.GetValue()
	}
	if err := backend.dockerContainerCreate(ctx, dockerContainerSpec{
		Name:        state.PrimaryContainerName,
		Image:       record.createSpec.GetImage(),
		NetworkName: state.NetworkName,
		Labels:      runtimedocker.SandboxLabels(record.handle.GetSandboxId(), "default", userLabels),
		Mounts:      mounts,
		Environment: primaryEnv,
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
			if _, err := backend.dockerExec(ctx, dockerExecSpec{
				ContainerName: state.PrimaryContainerName,
				Command:       []string{"sh", "-lc", hook},
				Environment:   keyValuesToMap(service.GetEnvironment()),
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
			if _, err := backend.dockerExec(ctx, dockerExecSpec{
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
	primaryContainerName string,
	services []*agboxv1.ServiceSpec,
	userLabels map[string]string,
	backend *dockerRuntimeBackend,
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
				results <- status
				return
			}
			results <- status
			// Run post_start_on_primary hooks after healthcheck passes in a separate
			// goroutine so sandbox ready is not delayed by optional service setup.
			if len(service.GetPostStartOnPrimary()) > 0 && service.GetHealthcheck() != nil {
				go func(svc *agboxv1.ServiceSpec, ctrName string) {
					if err := backend.dockerWaitRequiredServiceHealthy(optionalCtx, ctrName, svc.GetHealthcheck()); err != nil {
						return
					}
					for _, hook := range svc.GetPostStartOnPrimary() {
						if _, err := backend.dockerExec(optionalCtx, dockerExecSpec{
							ContainerName: primaryContainerName,
							Command:       []string{"sh", "-lc", hook},
							Environment:   keyValuesToMap(svc.GetEnvironment()),
						}); err != nil {
							return
						}
					}
				}(service, containerName)
			}
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

func (backend *dockerRuntimeBackend) InspectContainer(ctx context.Context, containerName string) (ContainerInspectResult, error) {
	resp, err := backend.dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return ContainerInspectResult{}, nil
		}
		return ContainerInspectResult{}, fmt.Errorf("inspect container %s: %w", containerName, err)
	}
	result := ContainerInspectResult{
		Exists:  true,
		Running: resp.State != nil && resp.State.Running,
	}
	if resp.State != nil {
		result.ExitCode = resp.State.ExitCode
		result.OOMKilled = resp.State.OOMKilled
	}
	return result, nil
}

func (backend *dockerRuntimeBackend) WatchContainerEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	filterArgs := filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
		filters.Arg("event", "die"),
		filters.Arg("event", "oom"),
		filters.Arg("label", runtimedocker.LabelSandboxID),
	)
	dockerEvents, dockerErrors := backend.dockerClient.Events(ctx, events.ListOptions{Filters: filterArgs})

	eventCh := make(chan ContainerEvent)
	go func() {
		defer close(eventCh)
		for event := range dockerEvents {
			sandboxID := event.Actor.Attributes[runtimedocker.LabelSandboxID]
			if sandboxID == "" {
				continue
			}
			containerName := event.Actor.Attributes["name"]
			serviceName := event.Actor.Attributes[runtimedocker.LabelServiceName]
			component := event.Actor.Attributes[runtimedocker.LabelComponent]

			ce := ContainerEvent{
				SandboxID:     sandboxID,
				ContainerName: containerName,
				Action:        string(event.Action),
				IsService:     component == "service",
				ServiceName:   serviceName,
			}
			eventCh <- ce
		}
	}()
	return eventCh, dockerErrors
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
	var logDir string
	if backend.config.ArtifactOutputRoot != "" {
		logDir = execLogContainerDir
	}
	// Merge sandbox-level envs with exec-level overrides.
	// Exec overrides take precedence over sandbox envs.
	execEnv := keyValuesToMap(record.createSpec.GetEnvs())
	for k, v := range keyValuesToMap(execRecord.GetEnvOverrides()) {
		execEnv[k] = v
	}
	exitCode, err := backend.dockerExec(ctx, dockerExecSpec{
		ContainerName: record.runtimeState.PrimaryContainerName,
		Command:       execRecord.GetCommand(),
		Workdir:       execRecord.GetCwd(),
		Environment:   execEnv,
		User:          "agbox",
		LogDir:        logDir,
		ExecID:        execRecord.GetExecId(),
	})
	return runtimeExecResult{ExitCode: exitCode}, err
}
