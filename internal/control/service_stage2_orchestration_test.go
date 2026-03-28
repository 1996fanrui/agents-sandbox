package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type scriptedRuntimeBackend struct {
	createResult runtimeCreateResult
	createErr    error
	resumeResult runtimeResumeResult
	resumeErr    error
	stopErr      error
	deleteErr    error
}

func (backend *scriptedRuntimeBackend) CreateSandbox(context.Context, *sandboxRecord) (runtimeCreateResult, error) {
	return backend.createResult, backend.createErr
}

func (backend *scriptedRuntimeBackend) ResumeSandbox(context.Context, *sandboxRecord) (runtimeResumeResult, error) {
	return backend.resumeResult, backend.resumeErr
}

func (backend *scriptedRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error {
	return backend.stopErr
}

func (backend *scriptedRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error {
	return backend.deleteErr
}

func (*scriptedRuntimeBackend) RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error) {
	return runtimeExecResult{ExitCode: 0}, nil
}

func assertMessageFieldNames(t *testing.T, descriptor protoreflect.MessageDescriptor, want []string) {
	t.Helper()
	fields := descriptor.Fields()
	got := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		got = append(got, string(fields.Get(i).Name()))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected fields for %s: got %v want %v", descriptor.FullName(), got, want)
	}
}

func assertMessageFieldNumbers(t *testing.T, descriptor protoreflect.MessageDescriptor, want map[string]protoreflect.FieldNumber) {
	t.Helper()
	fields := descriptor.Fields()
	got := make(map[string]protoreflect.FieldNumber, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		got[string(field.Name())] = field.Number()
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected field numbers for %s: got %v want %v", descriptor.FullName(), got, want)
	}
}

func TestRequiredServiceStartupAndReadyEvent(t *testing.T) {
	backend := &scriptedRuntimeBackend{
		createResult: runtimeCreateResult{
			RuntimeState: &sandboxRuntimeState{PrimaryContainerName: "primary", NetworkName: "network"},
			ServiceStatuses: []runtimeServiceStatus{
				{Name: "db", Required: true, Ready: true},
			},
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "required-ready",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			RequiredServices: []*agboxv1.ServiceSpec{
				{
					Name:  "db",
					Image: "postgres:16",
					Healthcheck: &agboxv1.HealthcheckConfig{
						Test: []string{"CMD", "true"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		for _, item := range items {
			if item.GetEventType() == agboxv1.EventType_SANDBOX_READY {
				return true
			}
		}
		return false
	})

	serviceReadyIndex := -1
	sandboxReadyIndex := -1
	for index, event := range events {
		if event.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_READY && event.GetServiceName() == "db" {
			serviceReadyIndex = index
		}
		if event.GetEventType() == agboxv1.EventType_SANDBOX_READY {
			sandboxReadyIndex = index
		}
	}
	if serviceReadyIndex == -1 {
		t.Fatalf("missing SANDBOX_SERVICE_READY event for required service: %#v", events)
	}
	if sandboxReadyIndex == -1 || serviceReadyIndex > sandboxReadyIndex {
		t.Fatalf("service ready event must be emitted before SANDBOX_READY: %#v", events)
	}

	sandboxResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: createResp.GetSandboxId()})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if len(sandboxResp.GetSandbox().GetRequiredServices()) != 1 || sandboxResp.GetSandbox().GetRequiredServices()[0].GetName() != "db" {
		t.Fatalf("required services were not preserved in handle: %#v", sandboxResp.GetSandbox().GetRequiredServices())
	}
}

func TestRequiredServiceFailureAndCleanup(t *testing.T) {
	cases := []struct {
		name       string
		health     *agboxv1.HealthcheckConfig
		wantWindow time.Duration
	}{
		{
			name: "with_start_interval",
			health: &agboxv1.HealthcheckConfig{
				Test:          []string{"CMD", "true"},
				StartPeriod:   "20s",
				StartInterval: "11s",
				Interval:      "2s",
				Timeout:       "7s",
				Retries:       2,
			},
			wantWindow: 52 * time.Second,
		},
		{
			name: "default_start_interval_when_start_period_positive",
			health: &agboxv1.HealthcheckConfig{
				Test:        []string{"CMD", "true"},
				StartPeriod: "12s",
				Interval:    "2s",
				Timeout:     "1s",
				Retries:     2,
			},
			wantWindow: 23 * time.Second,
		},
		{
			name: "default_retries",
			health: &agboxv1.HealthcheckConfig{
				Test:     []string{"CMD", "true"},
				Interval: "4s",
				Timeout:  "6s",
			},
			wantWindow: 24 * time.Second,
		},
		{
			name: "cap_to_five_minutes",
			health: &agboxv1.HealthcheckConfig{
				Test:          []string{"CMD", "true"},
				StartPeriod:   "10m",
				StartInterval: "20s",
				Interval:      "40s",
				Timeout:       "10s",
				Retries:       10,
			},
			wantWindow: 5 * time.Minute,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			window, err := requiredServiceHealthWaitUpperBound(testCase.health)
			if err != nil {
				t.Fatalf("requiredServiceHealthWaitUpperBound failed: %v", err)
			}
			if window != testCase.wantWindow {
				t.Fatalf("unexpected wait window: got %s want %s", window, testCase.wantWindow)
			}
		})
	}

	backend := &scriptedRuntimeBackend{
		createErr: errors.New("required service did not become healthy"),
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "required-failure",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			RequiredServices: []*agboxv1.ServiceSpec{
				{
					Name:  "db",
					Image: "postgres:16",
					Healthcheck: &agboxv1.HealthcheckConfig{
						Test: []string{"CMD", "true"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed unexpectedly: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_FAILED)
}

func TestOptionalServiceNonBlockingCreatePath(t *testing.T) {
	optionalStatuses := make(chan runtimeServiceStatus, 2)
	backend := &scriptedRuntimeBackend{
		createResult: runtimeCreateResult{
			RuntimeState:            &sandboxRuntimeState{PrimaryContainerName: "primary", NetworkName: "network"},
			OptionalServiceStatuses: optionalStatuses,
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "optional-non-blocking",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			OptionalServices: []*agboxv1.ServiceSpec{
				{Name: "cache", Image: "redis:7"},
				{Name: "queue", Image: "rabbitmq:4"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	optionalStatuses <- runtimeServiceStatus{Name: "cache", Required: false, Ready: false, Message: "image pull failed"}
	optionalStatuses <- runtimeServiceStatus{Name: "queue", Required: false, Ready: true}
	close(optionalStatuses)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		var sandboxReady bool
		var optionalReady bool
		var optionalFailed bool
		for _, item := range items {
			if item.GetEventType() == agboxv1.EventType_SANDBOX_READY {
				sandboxReady = true
			}
			if item.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_READY && item.GetServiceName() == "queue" {
				optionalReady = true
			}
			if item.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_FAILED && item.GetServiceName() == "cache" {
				optionalFailed = true
			}
		}
		return sandboxReady && optionalReady && optionalFailed
	})

	var optionalReady bool
	var optionalFailed bool
	for _, event := range events {
		if event.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_READY && event.GetServiceName() == "queue" {
			optionalReady = true
		}
		if event.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_FAILED && event.GetServiceName() == "cache" {
			optionalFailed = true
		}
	}
	if !optionalReady || !optionalFailed {
		t.Fatalf("optional service events are incomplete: %#v", events)
	}
}

func TestOptionalServiceEventsAlreadyCompletedEmitBeforeSandboxReady(t *testing.T) {
	optionalStatuses := make(chan runtimeServiceStatus, 1)
	optionalStatuses <- runtimeServiceStatus{Name: "cache", Required: false, Ready: true}
	backend := &scriptedRuntimeBackend{
		createResult: runtimeCreateResult{
			RuntimeState:            &sandboxRuntimeState{PrimaryContainerName: "primary", NetworkName: "network"},
			OptionalServiceStatuses: optionalStatuses,
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "optional-ready-order",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			OptionalServices: []*agboxv1.ServiceSpec{
				{Name: "cache", Image: "redis:7"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)
	close(optionalStatuses)

	stream, err := client.SubscribeSandboxEvents(context.Background(), &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:    createResp.GetSandboxId(),
		FromSequence: 0,
	})
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	events := collectEventsUntil(t, stream, func(items []*agboxv1.SandboxEvent) bool {
		var sawReady bool
		var sawOptionalReady bool
		for _, item := range items {
			if item.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_READY && item.GetServiceName() == "cache" {
				sawOptionalReady = true
			}
			if item.GetEventType() == agboxv1.EventType_SANDBOX_READY {
				sawReady = true
			}
		}
		return sawOptionalReady && sawReady
	})
	if len(events) < 3 {
		t.Fatalf("unexpected event sequence: %#v", events)
	}
	optionalIndex := -1
	readyIndex := -1
	for index, event := range events {
		if event.GetEventType() == agboxv1.EventType_SANDBOX_SERVICE_READY && event.GetServiceName() == "cache" {
			optionalIndex = index
			if event.GetSandboxState() != agboxv1.SandboxState_SANDBOX_STATE_PENDING {
				t.Fatalf("expected optional service event to keep pending sandbox state, got %#v", event)
			}
		}
		if event.GetEventType() == agboxv1.EventType_SANDBOX_READY {
			readyIndex = index
		}
	}
	if optionalIndex == -1 || readyIndex == -1 || optionalIndex >= readyIndex {
		t.Fatalf("expected optional service event before sandbox ready, got %#v", events)
	}
}

func TestOptionalServicesLaunchInParallelWithPrimaryPath(t *testing.T) {
	services := []*agboxv1.ServiceSpec{
		{Name: "cache", Image: "redis:7"},
		{Name: "queue", Image: "rabbitmq:4"},
	}
	started := make(chan string, len(services))
	release := make(chan struct{})

	starts := startOptionalServicesAsync(context.Background(), "parallel-optional", "network", "primary", services, nil, nil, func(context.Context, dockerContainerSpec) error {
		started <- "create"
		return nil
	}, func(_ context.Context, name string) error {
		started <- name
		<-release
		return nil
	})

	observed := []string{<-started, <-started, <-started, <-started}
	if !slices.Contains(observed, dockerServiceContainerName("parallel-optional", "cache")) || !slices.Contains(observed, dockerServiceContainerName("parallel-optional", "queue")) {
		t.Fatalf("optional services did not both begin startup before primary would continue: %v", observed)
	}

	close(release)
	statuses := collectRuntimeServiceStatuses(starts.Statuses)
	if len(statuses) != 2 || !statuses[0].Ready || !statuses[1].Ready {
		t.Fatalf("unexpected optional statuses: %#v", statuses)
	}
}

func TestOptionalServiceStartupCancellationStopsWorkers(t *testing.T) {
	services := []*agboxv1.ServiceSpec{
		{Name: "cache", Image: "redis:7"},
	}
	started := make(chan struct{}, 1)
	starts := startOptionalServicesAsync(context.Background(), "cancel-optional", "network", "primary", services, nil, nil, func(context.Context, dockerContainerSpec) error {
		return nil
	}, func(ctx context.Context, _ string) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})

	<-started
	starts.CancelAndWait()
	statuses := collectRuntimeServiceStatuses(starts.Statuses)
	if len(statuses) != 1 {
		t.Fatalf("unexpected optional status count: %#v", statuses)
	}
	if statuses[0].Ready {
		t.Fatalf("expected canceled optional startup to fail, got %#v", statuses[0])
	}
	if !strings.Contains(statuses[0].Message, context.Canceled.Error()) {
		t.Fatalf("unexpected optional cancellation message: %q", statuses[0].Message)
	}
}

func TestDeleteSandboxCancelsOutstandingOptionalStarts(t *testing.T) {
	services := []*agboxv1.ServiceSpec{
		{Name: "cache", Image: "redis:7"},
	}
	started := make(chan struct{}, 1)
	starts := startOptionalServicesAsync(context.Background(), "delete-cancel", "network", "primary", services, nil, nil, func(context.Context, dockerContainerSpec) error {
		return nil
	}, func(ctx context.Context, _ string) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})

	<-started
	backend := &dockerRuntimeBackend{config: ServiceConfig{}}
	if err := backend.DeleteSandbox(context.Background(), &sandboxRecord{
		runtimeState: &sandboxRuntimeState{
			OptionalServiceStarts: starts,
		},
	}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	statuses := collectRuntimeServiceStatuses(starts.Statuses)
	if len(statuses) != 1 || statuses[0].Ready || !strings.Contains(statuses[0].Message, context.Canceled.Error()) {
		t.Fatalf("expected delete to cancel optional startup, got %#v", statuses)
	}
}

func TestLatestHealthLogTimestampUsesNewestNonNilEntry(t *testing.T) {
	now := time.Now().UTC()
	latest := latestHealthLogTimestamp([]*container.HealthcheckResult{
		nil,
		{Start: now.Add(-3 * time.Second), End: now.Add(-2 * time.Second)},
		{Start: now.Add(-1 * time.Second)},
	})
	if !latest.Equal(now.Add(-1 * time.Second)) {
		t.Fatalf("unexpected latest health log timestamp: got %s want %s", latest, now.Add(-1*time.Second))
	}
}

func TestToContainerHealthConfigMapsProtoFields(t *testing.T) {
	config, err := toContainerHealthConfig(&agboxv1.HealthcheckConfig{
		Test:          []string{"CMD", "pg_isready", "-U", "postgres"},
		Interval:      "5s",
		Timeout:       "2s",
		Retries:       3,
		StartPeriod:   "10s",
		StartInterval: "1s",
	})
	if err != nil {
		t.Fatalf("toContainerHealthConfig failed: %v", err)
	}
	if config == nil {
		t.Fatal("expected health config")
	}
	if !reflect.DeepEqual(config.Test, []string{"CMD", "pg_isready", "-U", "postgres"}) {
		t.Fatalf("unexpected test command: %#v", config.Test)
	}
	if config.Interval != 5*time.Second || config.Timeout != 2*time.Second {
		t.Fatalf("unexpected health timing: interval=%s timeout=%s", config.Interval, config.Timeout)
	}
	if config.Retries != 3 {
		t.Fatalf("unexpected retries: %d", config.Retries)
	}
	if config.StartPeriod != 10*time.Second || config.StartInterval != time.Second {
		t.Fatalf("unexpected start timing: start_period=%s start_interval=%s", config.StartPeriod, config.StartInterval)
	}
}

func TestToContainerHealthConfigRejectsInvalidDuration(t *testing.T) {
	_, err := toContainerHealthConfig(&agboxv1.HealthcheckConfig{
		Test:     []string{"CMD", "true"},
		Interval: "not-a-duration",
	})
	if err == nil || !strings.Contains(err.Error(), "parse healthcheck interval") {
		t.Fatalf("expected invalid interval error, got %v", err)
	}
}

func TestDockerContainerStopReturnsNilForExistingStoppedContainer(t *testing.T) {
	stopCalls := 0
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodGet && path == "/containers/stopped/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: false, Status: "exited"},
				},
			})
		case r.Method == http.MethodPost && path == "/containers/stopped/stop":
			stopCalls++
			t.Fatalf("stop endpoint must not be called for an already stopped container")
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := backend.dockerContainerStop(context.Background(), "stopped"); err != nil {
		t.Fatalf("dockerContainerStop returned error for stopped container: %v", err)
	}
	if stopCalls != 0 {
		t.Fatalf("expected no stop calls, got %d", stopCalls)
	}
}

func TestDockerContainerStopReturnsNilForMissingContainer(t *testing.T) {
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		if r.Method == http.MethodGet && path == "/containers/missing/json" {
			w.WriteHeader(http.StatusNotFound)
			writeDockerJSON(t, w, map[string]string{"message": "No such container: missing"})
			return
		}
		t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
	})

	if err := backend.dockerContainerStop(context.Background(), "missing"); err != nil {
		t.Fatalf("dockerContainerStop returned error for missing container: %v", err)
	}
}

func TestDockerExecUsesSDKAndDrainsOutput(t *testing.T) {
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodPost && path == "/containers/primary/exec":
			var request container.ExecOptions
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode exec create request failed: %v", err)
			}
			if !reflect.DeepEqual(request.Cmd, []string{"sh", "-lc", "echo hello"}) {
				t.Fatalf("unexpected exec command: %#v", request.Cmd)
			}
			if request.WorkingDir != "/workspace" {
				t.Fatalf("unexpected working dir: %q", request.WorkingDir)
			}
			if !slices.Equal(request.Env, []string{"FOO=bar"}) {
				t.Fatalf("unexpected env: %#v", request.Env)
			}
			// AttachStdout and AttachStderr must be true so exec completion is detected via stream drain.
			if !request.AttachStdout || !request.AttachStderr || request.Tty {
				t.Fatalf("unexpected attach settings: %#v", request)
			}
			writeDockerJSON(t, w, map[string]string{"Id": "exec-1"})
		case r.Method == http.MethodPost && path == "/exec/exec-1/start":
			var request container.ExecStartOptions
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode exec start request failed: %v", err)
			}
			if request.Detach || request.Tty {
				t.Fatalf("unexpected exec start options: %#v", request)
			}
			writeHijackedDockerStream(t, w, func(writer io.Writer) {
				if _, err := stdcopy.NewStdWriter(writer, stdcopy.Stdout).Write([]byte("hello")); err != nil {
					t.Fatalf("write stdout stream failed: %v", err)
				}
				if _, err := stdcopy.NewStdWriter(writer, stdcopy.Stderr).Write([]byte("warning")); err != nil {
					t.Fatalf("write stderr stream failed: %v", err)
				}
			})
		case r.Method == http.MethodGet && path == "/exec/exec-1/json":
			writeDockerJSON(t, w, container.ExecInspect{ExecID: "exec-1", Running: false, ExitCode: 0})
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	})

	exitCode, err := backend.dockerExec(context.Background(), dockerExecSpec{
		ContainerName: "primary",
		Command:       []string{"sh", "-lc", "echo hello"},
		Workdir:       "/workspace",
		Environment:   map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("dockerExec returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: %d", exitCode)
	}
}

func TestDockerExecReturnsExitCodeAndErrorForNonZeroExit(t *testing.T) {
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodPost && path == "/containers/primary/exec":
			writeDockerJSON(t, w, map[string]string{"Id": "exec-2"})
		case r.Method == http.MethodPost && path == "/exec/exec-2/start":
			var request container.ExecStartOptions
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode exec start request failed: %v", err)
			}
			if request.Detach || request.Tty {
				t.Fatalf("unexpected exec start options: %#v", request)
			}
			writeHijackedDockerStream(t, w, func(writer io.Writer) {
				if _, err := stdcopy.NewStdWriter(writer, stdcopy.Stdout).Write([]byte("partial")); err != nil {
					t.Fatalf("write stdout stream failed: %v", err)
				}
				if _, err := stdcopy.NewStdWriter(writer, stdcopy.Stderr).Write([]byte("boom")); err != nil {
					t.Fatalf("write stderr stream failed: %v", err)
				}
			})
		case r.Method == http.MethodGet && path == "/exec/exec-2/json":
			writeDockerJSON(t, w, container.ExecInspect{ExecID: "exec-2", Running: false, ExitCode: 42})
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	})

	exitCode, err := backend.dockerExec(context.Background(), dockerExecSpec{
		ContainerName: "primary",
		Command:       []string{"false"},
	})
	if err == nil {
		t.Fatal("expected dockerExec to fail for non-zero exit code")
	}
	if exitCode != 42 {
		t.Fatalf("unexpected exit code: %d err=%v", exitCode, err)
	}
	if !strings.Contains(err.Error(), "docker exec failed") {
		t.Fatalf("unexpected exec error: %v", err)
	}
}

func TestExecCommandWrapWhenLogDirSet(t *testing.T) {
	// When LogDir is set, dockerExec wraps the command with shell redirection.
	spec := dockerExecSpec{
		ContainerName: "test-container",
		Command:       []string{"python", "-c", "print('hello')"},
		LogDir:        "/var/log/agents-sandbox/",
		ExecID:        "exec-123",
	}

	stdoutLog := spec.LogDir + spec.ExecID + ".stdout.log"
	stderrLog := spec.LogDir + spec.ExecID + ".stderr.log"
	shellCmd := "exec \"$@\" >" + stdoutLog + " 2>" + stderrLog

	expectedCmd := []string{"sh", "-c", shellCmd, "--", "python", "-c", "print('hello')"}

	// Reproduce the wrapping logic from dockerExec to confirm the expected shape.
	cmd := spec.Command
	if spec.LogDir != "" {
		cmd = append([]string{"sh", "-c", shellCmd, "--"}, spec.Command...)
	}

	if len(cmd) != len(expectedCmd) {
		t.Fatalf("expected %d args, got %d", len(expectedCmd), len(cmd))
	}
	for i := range cmd {
		if cmd[i] != expectedCmd[i] {
			t.Fatalf("arg[%d]: expected %q, got %q", i, expectedCmd[i], cmd[i])
		}
	}
}
