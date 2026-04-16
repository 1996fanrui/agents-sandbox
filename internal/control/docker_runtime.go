package control

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// ReapplyNetworkIsolation re-applies nftables host isolation rules for a sandbox network.
	// Called during daemon restart recovery because nftables rules are lost on host reboot.
	ReapplyNetworkIsolation(ctx context.Context, record *sandboxRecord) error
}

// ContainerEvent represents a Docker container lifecycle event relevant to sandbox state management.
type ContainerEvent struct {
	SandboxID              string
	ContainerName          string
	Action                 string // "die" or "oom"
	IsCompanionContainer   bool
	CompanionContainerName string
}

// ContainerInspectResult holds the state of a single container queried via Docker inspect.
type ContainerInspectResult struct {
	Exists    bool
	Running   bool
	ExitCode  int
	OOMKilled bool
}

type runtimeCreateResult struct {
	CompanionContainerStatuses <-chan companionContainerStatus
	RuntimeState               *sandboxRuntimeState
}

type runtimeResumeResult struct {
	CompanionContainerStatuses []companionContainerStatus
}

type companionContainerStatus struct {
	Name    string
	Ready   bool
	Message string
}

type companionContainerStarts struct {
	Statuses <-chan companionContainerStatus
	done     <-chan struct{}
	cancel   context.CancelFunc
}

func (starts companionContainerStarts) CancelAndWait() {
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
	NetworkName              string
	PrimaryContainerName     string
	CompanionContainers      []runtimeCompanionContainer
	CompanionContainerStarts companionContainerStarts
}

type runtimeCompanionContainer struct {
	Name          string
	ContainerName string
}

type fakeRuntimeBackend struct {
	inspectResults map[string]ContainerInspectResult
	eventCh        chan ContainerEvent
	errCh          chan error
}

func (fakeRuntimeBackend) CreateSandbox(_ context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	statuses := make(chan companionContainerStatus, len(record.companionContainers))
	containers := make([]runtimeCompanionContainer, 0, len(record.companionContainers))
	for _, cc := range record.companionContainers {
		statuses <- companionContainerStatus{Name: cc.GetName(), Ready: true}
		containers = append(containers, runtimeCompanionContainer{
			Name:          cc.GetName(),
			ContainerName: "fake-companion-" + record.handle.GetSandboxId() + "-" + sanitizeRuntimeName(cc.GetName()),
		})
	}
	close(statuses)
	return runtimeCreateResult{
		CompanionContainerStatuses: statuses,
		RuntimeState: &sandboxRuntimeState{
			NetworkName:          "fake-network-" + record.handle.GetSandboxId(),
			PrimaryContainerName: "fake-primary-" + record.handle.GetSandboxId(),
			CompanionContainers:  containers,
		},
	}, nil
}

func (fakeRuntimeBackend) ResumeSandbox(_ context.Context, record *sandboxRecord) (runtimeResumeResult, error) {
	statuses := make([]companionContainerStatus, 0, len(record.companionContainers))
	for _, cc := range record.companionContainers {
		statuses = append(statuses, companionContainerStatus{Name: cc.GetName(), Ready: true})
	}
	return runtimeResumeResult{CompanionContainerStatuses: statuses}, nil
}
func (fakeRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error             { return nil }
func (fakeRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error           { return nil }
func (fakeRuntimeBackend) ReapplyNetworkIsolation(context.Context, *sandboxRecord) error { return nil }

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
	nftConn      nftablesConnector
}

func newDockerRuntimeBackend(config ServiceConfig) (runtimeBackend, io.Closer, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, fmt.Errorf("initialize docker client: %w", err)
	}
	backend := &dockerRuntimeBackend{
		config:       config,
		dockerClient: dockerClient,
		nftConn:      newNftablesConnector(config.Logger),
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
	var ccStarts companionContainerStarts
	defer func() {
		if !cleanupRequired {
			return
		}
		ccStarts.CancelAndWait()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = backend.deleteRuntimeArtifacts(cleanupCtx, state)
	}()

	mounts, err := backend.materializeBuiltinTools(record.handle.GetSandboxId(), record.createSpec.GetBuiltinTools(), state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	genericMounts, err := backend.materializeGenericMounts(record.createSpec.GetMounts())
	if err != nil {
		return runtimeCreateResult{}, err
	}
	mounts = append(mounts, genericMounts...)
	deferredCopies, err := validateGenericCopies(record.createSpec.GetCopies())
	if err != nil {
		return runtimeCreateResult{}, err
	}
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
	for _, cc := range record.companionContainers {
		if err := backend.ensureDockerImage(ctx, cc.GetImage()); err != nil {
			return runtimeCreateResult{}, err
		}
	}
	userLabels := record.handle.GetLabels()
	if err := backend.dockerNetworkCreate(ctx, state.NetworkName, runtimedocker.SandboxLabels(record.handle.GetSandboxId(), "default", userLabels)); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := backend.applyNetworkHostIsolation(ctx, state.NetworkName); err != nil {
		return runtimeCreateResult{}, err
	}

	ccStarts = startCompanionContainersAsync(ctx, record.handle.GetSandboxId(), state.NetworkName, state.PrimaryContainerName, record.companionContainers, userLabels, backend, func(ctx context.Context, spec dockerContainerSpec) error {
		return backend.dockerContainerCreate(ctx, spec)
	}, func(ctx context.Context, name string) error {
		return backend.dockerContainerStart(ctx, name)
	})
	state.CompanionContainers = make([]runtimeCompanionContainer, 0, len(record.companionContainers))
	for _, cc := range record.companionContainers {
		state.CompanionContainers = append(state.CompanionContainers, runtimeCompanionContainer{
			Name:          cc.GetName(),
			ContainerName: dockerCompanionContainerName(record.handle.GetSandboxId(), cc.GetName()),
		})
	}

	var portMappings []dockerPortMapping
	for _, p := range record.createSpec.GetPorts() {
		portMappings = append(portMappings, dockerPortMapping{
			ContainerPort: p.GetContainerPort(),
			HostPort:      p.GetHostPort(),
			Protocol:      portProtocolToString(p.GetProtocol()),
		})
	}
	primaryEnv := primaryContainerEnvironment(mounts)
	for k, v := range record.createSpec.GetEnvs() {
		primaryEnv[k] = v
	}
	primaryCommand := primaryContainerCommand(record.createSpec.GetCommand())
	if err := backend.dockerContainerCreate(ctx, dockerContainerSpec{
		Name:        state.PrimaryContainerName,
		Image:       record.createSpec.GetImage(),
		NetworkName: state.NetworkName,
		Labels:      runtimedocker.SandboxLabels(record.handle.GetSandboxId(), "default", userLabels),
		Mounts:      mounts,
		Ports:       portMappings,
		Environment: primaryEnv,
		Workdir:     "/workspace",
		Command:     primaryCommand,
	}); err != nil {
		return runtimeCreateResult{}, err
	}
	for _, copy := range deferredCopies {
		if err := backend.dockerCopyToContainer(ctx, state.PrimaryContainerName, copy); err != nil {
			return runtimeCreateResult{}, err
		}
	}
	if err := backend.dockerContainerStart(ctx, state.PrimaryContainerName); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := backend.dockerWaitContainerRunning(ctx, state.PrimaryContainerName, 10*time.Second); err != nil {
		return runtimeCreateResult{}, err
	}
	cleanupRequired = false
	state.CompanionContainerStarts = ccStarts
	return runtimeCreateResult{
		CompanionContainerStatuses: ccStarts.Statuses,
		RuntimeState:               state,
	}, nil
}

func (backend *dockerRuntimeBackend) ResumeSandbox(ctx context.Context, record *sandboxRecord) (runtimeResumeResult, error) {
	if record.runtimeState == nil {
		return runtimeResumeResult{}, errors.New("sandbox runtime state is missing")
	}
	if err := backend.dockerContainerMustExist(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeResumeResult{}, err
	}
	for _, cc := range record.runtimeState.CompanionContainers {
		if err := backend.dockerContainerMustExist(ctx, cc.ContainerName); err != nil {
			return runtimeResumeResult{}, err
		}
	}
	// Re-apply nftables host isolation rules for the sandbox network.
	// Rules are lost on host reboot or nftables flush, so re-apply on every resume.
	if err := backend.applyNetworkHostIsolation(ctx, record.runtimeState.NetworkName); err != nil {
		return runtimeResumeResult{}, err
	}
	if err := backend.dockerContainerEnsureRunning(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeResumeResult{}, err
	}
	statuses := make([]companionContainerStatus, 0, len(record.companionContainers))
	for _, cc := range record.companionContainers {
		containerName := dockerCompanionContainerName(record.handle.GetSandboxId(), cc.GetName())
		if err := backend.dockerContainerEnsureRunning(ctx, containerName); err != nil {
			statuses = append(statuses, companionContainerStatus{Name: cc.GetName(), Ready: false, Message: err.Error()})
			continue
		}
		statuses = append(statuses, companionContainerStatus{Name: cc.GetName(), Ready: true})
	}
	return runtimeResumeResult{CompanionContainerStatuses: statuses}, nil
}

// primaryContainerCommand returns the Docker Cmd argv for the primary
// container. If the CreateSpec supplies a command, it is used verbatim;
// otherwise the daemon's built-in sleep-loop default is returned to keep
// pre-issue-170 behavior unchanged.
func primaryContainerCommand(specCommand []string) []string {
	if len(specCommand) > 0 {
		return append([]string(nil), specCommand...)
	}
	return []string{
		"sh",
		"-lc",
		"trap 'exit 0' TERM INT; while sleep 3600; do :; done",
	}
}

func startCompanionContainersAsync(
	ctx context.Context,
	sandboxID string,
	networkName string,
	primaryContainerName string,
	containers []*agboxv1.CompanionContainerSpec,
	userLabels map[string]string,
	backend *dockerRuntimeBackend,
	createContainer func(context.Context, dockerContainerSpec) error,
	startContainer func(context.Context, string) error,
) companionContainerStarts {
	ccCtx, cancel := context.WithCancel(ctx)
	results := make(chan companionContainerStatus, len(containers))
	done := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(containers))
	for _, cc := range containers {
		containerName := dockerCompanionContainerName(sandboxID, cc.GetName())
		go func(cc *agboxv1.CompanionContainerSpec, containerName string) {
			defer waitGroup.Done()
			status := companionContainerStatus{Name: cc.GetName(), Ready: true}
			if err := createContainer(ccCtx, dockerContainerSpec{
				Name:         containerName,
				Image:        cc.GetImage(),
				NetworkName:  networkName,
				NetworkAlias: cc.GetName(),
				Labels:       runtimedocker.CompanionContainerLabels(sandboxID, cc.GetName(), userLabels),
				Environment:  cc.GetEnvs(),
				Healthcheck:  cc.GetHealthcheck(),
				// nil/empty passes nil to Docker so the image CMD applies.
				Command: cc.GetCommand(),
			}); err != nil {
				status.Ready = false
				status.Message = err.Error()
				results <- status
				return
			}
			if err := startContainer(ccCtx, containerName); err != nil {
				status.Ready = false
				status.Message = err.Error()
				results <- status
				return
			}
			results <- status
			// Run post_start_on_primary hooks after healthcheck passes in a separate
			// goroutine so sandbox ready is not delayed by companion container setup.
			if len(cc.GetPostStartOnPrimary()) > 0 && cc.GetHealthcheck() != nil {
				go func(spec *agboxv1.CompanionContainerSpec, ctrName string) {
					if err := backend.dockerWaitCompanionContainerHealthy(ccCtx, ctrName, spec.GetHealthcheck()); err != nil {
						return
					}
					for _, hook := range spec.GetPostStartOnPrimary() {
						if _, err := backend.dockerExec(ccCtx, dockerExecSpec{
							ContainerName: primaryContainerName,
							Command:       []string{"sh", "-lc", hook},
							Environment:   spec.GetEnvs(),
						}); err != nil {
							return
						}
					}
				}(cc, containerName)
			}
		}(cc, containerName)
	}
	go func() {
		waitGroup.Wait()
		close(results)
		close(done)
	}()
	return companionContainerStarts{
		Statuses: results,
		done:     done,
		cancel:   cancel,
	}
}

func collectCompanionContainerStatuses(results <-chan companionContainerStatus) []companionContainerStatus {
	statuses := make([]companionContainerStatus, 0)
	for result := range results {
		statuses = append(statuses, result)
	}
	return statuses
}

func (backend *dockerRuntimeBackend) StopSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return errors.New("sandbox runtime state is missing")
	}
	record.runtimeState.CompanionContainerStarts.CancelAndWait()
	if err := backend.dockerContainerStop(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return err
	}
	for _, cc := range record.runtimeState.CompanionContainers {
		if err := backend.dockerContainerStop(ctx, cc.ContainerName); err != nil {
			return err
		}
	}
	return nil
}

func (backend *dockerRuntimeBackend) DeleteSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return nil
	}
	record.runtimeState.CompanionContainerStarts.CancelAndWait()
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
	for _, cc := range state.CompanionContainers {
		joined = append(joined, backend.dockerContainerRemove(ctx, cc.ContainerName))
	}
	if state.NetworkName != "" {
		backend.removeNetworkHostIsolation(ctx, state.NetworkName)
		joined = append(joined, backend.dockerNetworkRemove(ctx, state.NetworkName))
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
			ccName := event.Actor.Attributes[runtimedocker.LabelCompanionContainerName]
			component := event.Actor.Attributes[runtimedocker.LabelComponent]

			ce := ContainerEvent{
				SandboxID:              sandboxID,
				ContainerName:          containerName,
				Action:                 string(event.Action),
				IsCompanionContainer:   component == "companion",
				CompanionContainerName: ccName,
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
	execEnv := make(map[string]string)
	for k, v := range record.createSpec.GetEnvs() {
		execEnv[k] = v
	}
	for k, v := range execRecord.GetEnvOverrides() {
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

func (backend *dockerRuntimeBackend) ReapplyNetworkIsolation(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil || record.runtimeState.NetworkName == "" {
		return nil
	}
	return backend.applyNetworkHostIsolation(ctx, record.runtimeState.NetworkName)
}
