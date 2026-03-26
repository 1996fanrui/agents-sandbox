package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

	starts := startOptionalServicesAsync(context.Background(), "parallel-optional", "network", services, nil, func(context.Context, dockerContainerSpec) error {
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
	starts := startOptionalServicesAsync(context.Background(), "cancel-optional", "network", services, nil, func(context.Context, dockerContainerSpec) error {
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
	starts := startOptionalServicesAsync(context.Background(), "delete-cancel", "network", services, nil, func(context.Context, dockerContainerSpec) error {
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

func TestDockerExecUsesSDKAndPreservesStreams(t *testing.T) {
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

	output, exitCode, err := backend.dockerExec(context.Background(), dockerExecSpec{
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
	if output.Stdout != "hello" || output.Stderr != "warning" {
		t.Fatalf("unexpected exec output: %#v", output)
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

	output, exitCode, err := backend.dockerExec(context.Background(), dockerExecSpec{
		ContainerName: "primary",
		Command:       []string{"false"},
	})
	if err == nil {
		t.Fatal("expected dockerExec to fail for non-zero exit code")
	}
	if exitCode != 42 {
		t.Fatalf("unexpected exit code: %d err=%v output=%#v", exitCode, err, output)
	}
	if output.Stdout != "partial" || output.Stderr != "boom" {
		t.Fatalf("unexpected exec output: %#v", output)
	}
	if !strings.Contains(err.Error(), "docker exec failed") {
		t.Fatalf("unexpected exec error: %v", err)
	}
}

func TestDockerLabelsPassthrough(t *testing.T) {
	sandboxID := "labels-pass-through"
	userLabels := map[string]string{
		"owner": "team-a",
		"env":   "dev",
	}
	requiredContainerName := dockerServiceContainerName(sandboxID, "db")
	optionalContainerName := dockerServiceContainerName(sandboxID, "cache")
	primaryContainerName := dockerPrimaryContainerName(sandboxID)

	var mu sync.Mutex
	networkLabels := map[string]string{}
	containerLabels := make(map[string]map[string]string)
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			writeDockerJSON(t, w, map[string]string{"Id": "sha256:test"})
		case r.Method == http.MethodPost && path == "/networks/create":
			var request struct {
				Labels map[string]string `json:"Labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode network create request failed: %v", err)
			}
			mu.Lock()
			networkLabels = request.Labels
			mu.Unlock()
			writeDockerJSON(t, w, map[string]string{"Id": "network-1"})
		case r.Method == http.MethodPost && path == "/containers/create":
			var request struct {
				Labels map[string]string `json:"Labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			containerLabels[name] = request.Labels
			mu.Unlock()
			writeDockerJSON(t, w, map[string]string{"Id": name})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/containers/"+requiredContainerName+"/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{
						Running: true,
						Status:  "running",
						Health:  &container.Health{Status: "healthy"},
					},
				},
			})
		case r.Method == http.MethodGet && path == "/containers/"+primaryContainerName+"/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: true, Status: "running"},
				},
			})
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	})

	result, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId: sandboxID,
			Labels:    userLabels,
		},
		createSpec: &agboxv1.CreateSpec{
			Image:  "ghcr.io/agents-sandbox/coding-runtime:test",
			Labels: userLabels,
		},
		requiredServices: []*agboxv1.ServiceSpec{
			{
				Name:  "db",
				Image: "postgres:16",
				Healthcheck: &agboxv1.HealthcheckConfig{
					Test: []string{"CMD", "true"},
				},
			},
		},
		optionalServices: []*agboxv1.ServiceSpec{
			{
				Name:  "cache",
				Image: "redis:7",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	statuses := collectRuntimeServiceStatuses(result.OptionalServiceStatuses)
	if len(statuses) != 1 || !statuses[0].Ready || statuses[0].Name != "cache" {
		t.Fatalf("unexpected optional service statuses: %#v", statuses)
	}

	assertUserDockerLabels(t, networkLabels, map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "primary",
		runtimedocker.LabelProfile:              "default",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})
	assertUserDockerLabels(t, containerLabels[requiredContainerName], map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "service",
		runtimedocker.LabelServiceName:          "db",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})
	assertUserDockerLabels(t, containerLabels[optionalContainerName], map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "service",
		runtimedocker.LabelServiceName:          "cache",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})
	assertUserDockerLabels(t, containerLabels[primaryContainerName], map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "primary",
		runtimedocker.LabelProfile:              "default",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})
}

func newDockerRuntimeBackendForTest(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *dockerRuntimeBackend {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)

	host := strings.Replace(server.URL, "http://", "tcp://", 1)
	dockerClient, err := client.NewClientWithOpts(
		client.WithHost(host),
		client.WithHTTPClient(server.Client()),
		client.WithVersion("1.44"),
	)
	if err != nil {
		t.Fatalf("NewClientWithOpts failed: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := dockerClient.Close(); closeErr != nil {
			t.Fatalf("docker client close failed: %v", closeErr)
		}
	})

	return &dockerRuntimeBackend{
		config:       ServiceConfig{},
		dockerClient: dockerClient,
	}
}

func writeDockerJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode Docker API response failed: %v", err)
	}
}

func writeHijackedDockerStream(t *testing.T, w http.ResponseWriter, writePayload func(io.Writer)) {
	t.Helper()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("response writer does not support hijacking")
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("hijack failed: %v", err)
	}
	defer conn.Close()

	if _, err := buffer.WriteString(
		"HTTP/1.1 101 UPGRADED\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: tcp\r\n" +
			"Content-Type: application/vnd.docker.raw-stream\r\n\r\n",
	); err != nil {
		t.Fatalf("write hijack headers failed: %v", err)
	}
	if err := buffer.Flush(); err != nil {
		t.Fatalf("flush hijack headers failed: %v", err)
	}

	writePayload(conn)
}

func assertUserDockerLabels(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected docker labels: got=%#v want=%#v", got, want)
	}
}

func TestServiceHealthcheckValidationAndPassthrough(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "health-pass-through",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			RequiredServices: []*agboxv1.ServiceSpec{
				{
					Name:  "db",
					Image: "postgres:16",
					Healthcheck: &agboxv1.HealthcheckConfig{
						Test:          []string{"CMD", "pg_isready", "-U", "postgres"},
						Interval:      "2s",
						Timeout:       "1s",
						Retries:       4,
						StartPeriod:   "10s",
						StartInterval: "2s",
					},
				},
			},
			OptionalServices: []*agboxv1.ServiceSpec{
				{
					Name:  "cache",
					Image: "redis:7",
					Healthcheck: &agboxv1.HealthcheckConfig{
						Test: []string{"NONE"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, "health-pass-through", agboxv1.SandboxState_SANDBOX_STATE_READY)
	if runtime.lastCreateSpec == nil {
		t.Fatal("runtime did not receive create spec")
	}
	healthcheck := runtime.lastCreateSpec.GetRequiredServices()[0].GetHealthcheck()
	if healthcheck.GetInterval() != "2s" || healthcheck.GetTimeout() != "1s" || healthcheck.GetRetries() != 4 || healthcheck.GetStartPeriod() != "10s" || healthcheck.GetStartInterval() != "2s" {
		t.Fatalf("healthcheck was not passed through: %#v", healthcheck)
	}

	invalidCases := []struct {
		name   string
		spec   *agboxv1.CreateSpec
		errMsg string
	}{
		{
			name: "required_missing_healthcheck",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				RequiredServices: []*agboxv1.ServiceSpec{
					{Name: "db", Image: "postgres:16"},
				},
			},
			errMsg: "must define healthcheck",
		},
		{
			name: "required_invalid_none_test",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				RequiredServices: []*agboxv1.ServiceSpec{
					{Name: "db", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"NONE"}}},
				},
			},
			errMsg: "is invalid",
		},
		{
			name: "optional_invalid_test_keyword",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				OptionalServices: []*agboxv1.ServiceSpec{
					{Name: "cache", Image: "redis:7", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"INVALID"}}},
				},
			},
			errMsg: "is invalid",
		},
		{
			name: "duplicate_service_name_across_sets",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				RequiredServices: []*agboxv1.ServiceSpec{
					{Name: "db", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}}},
				},
				OptionalServices: []*agboxv1.ServiceSpec{
					{Name: "db", Image: "redis:7"},
				},
			},
			errMsg: "duplicate service name",
		},
	}
	for _, testCase := range invalidCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId:  "invalid-" + testCase.name,
				CreateSpec: testCase.spec,
			})
			if err == nil || statusCode(err) != codes.InvalidArgument || !strings.Contains(err.Error(), testCase.errMsg) {
				t.Fatalf("expected invalid argument containing %q, got %v", testCase.errMsg, err)
			}
		})
	}
}

func TestPostStartOnPrimaryRequiredOnly(t *testing.T) {
	requiredSpec := &agboxv1.CreateSpec{
		Image: "ghcr.io/agents-sandbox/coding-runtime:test",
		RequiredServices: []*agboxv1.ServiceSpec{
			{
				Name:               "db",
				Image:              "postgres:16",
				PostStartOnPrimary: []string{"echo ready"},
				Healthcheck:        &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}},
			},
		},
	}
	if err := validateCreateSpec(requiredSpec); err != nil {
		t.Fatalf("required service post_start_on_primary should be valid: %v", err)
	}

	optionalSpec := &agboxv1.CreateSpec{
		Image: "ghcr.io/agents-sandbox/coding-runtime:test",
		OptionalServices: []*agboxv1.ServiceSpec{
			{
				Name:               "cache",
				Image:              "redis:7",
				PostStartOnPrimary: []string{"echo invalid"},
			},
		},
	}
	if err := validateCreateSpec(optionalSpec); err == nil || !strings.Contains(err.Error(), "must not define post_start_on_primary") {
		t.Fatalf("optional service post_start_on_primary should be rejected, got %v", err)
	}
}

func TestServiceLifecycleForResumeStopAndDelete(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "service-lifecycle",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			RequiredServices: []*agboxv1.ServiceSpec{
				{Name: "db", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}}},
			},
			OptionalServices: []*agboxv1.ServiceSpec{
				{Name: "cache", Image: "redis:7"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)

	if _, err := client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("ResumeSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)
}

func TestBuiltinResourcesForwardedToRuntime(t *testing.T) {
	runtime := &capturingRuntimeBackend{}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "builtin-without-legacy",
		CreateSpec: &agboxv1.CreateSpec{
			Image:            "ghcr.io/agents-sandbox/coding-runtime:test",
			BuiltinResources: []string{".claude"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if runtime.lastCreateSpec == nil || len(runtime.lastCreateSpec.GetBuiltinResources()) != 1 || runtime.lastCreateSpec.GetBuiltinResources()[0] != ".claude" {
		t.Fatalf("builtin_resources were not forwarded to runtime: %#v", runtime.lastCreateSpec)
	}
}

func TestProtoMessageFieldContracts(t *testing.T) {
	testCases := []struct {
		name       string
		descriptor protoreflect.MessageDescriptor
		fieldNames []string
		fieldNums  map[string]protoreflect.FieldNumber
	}{
		{
			name:       "CreateSpec",
			descriptor: (&agboxv1.CreateSpec{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"image",
				"mounts",
				"copies",
				"builtin_resources",
				"required_services",
				"optional_services",
				"labels",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"image":             1,
				"mounts":            2,
				"copies":            3,
				"builtin_resources": 4,
				"required_services": 5,
				"optional_services": 6,
				"labels":            7,
			},
		},
		{
			name:       "SandboxHandle",
			descriptor: (&agboxv1.SandboxHandle{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"sandbox_id",
				"state",
				"last_event_sequence",
				"required_services",
				"optional_services",
				"labels",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"sandbox_id":          1,
				"state":               2,
				"last_event_sequence": 3,
				"required_services":   4,
				"optional_services":   5,
				"labels":              6,
			},
		},
		{
			name:       "SandboxEvent",
			descriptor: (&agboxv1.SandboxEvent{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"event_id",
				"sequence",
				"sandbox_id",
				"event_type",
				"occurred_at",
				"replay",
				"snapshot",
				"phase",
				"error_code",
				"error_message",
				"reason",
				"exec_id",
				"exit_code",
				"sandbox_state",
				"exec_state",
				"service_name",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"event_id":      1,
				"sequence":      2,
				"sandbox_id":    3,
				"event_type":    4,
				"occurred_at":   5,
				"replay":        6,
				"snapshot":      7,
				"phase":         8,
				"error_code":    9,
				"error_message": 10,
				"reason":        11,
				"exec_id":       12,
				"exit_code":     13,
				"sandbox_state": 14,
				"exec_state":    15,
				"service_name":  16,
			},
		},
		{
			name:       "ExecStatus",
			descriptor: (&agboxv1.ExecStatus{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"exec_id",
				"sandbox_id",
				"state",
				"command",
				"cwd",
				"env_overrides",
				"exit_code",
				"error",
				"stdout",
				"stderr",
				"last_event_sequence",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"exec_id":             1,
				"sandbox_id":          2,
				"state":               3,
				"command":             4,
				"cwd":                 5,
				"env_overrides":       6,
				"exit_code":           7,
				"error":               8,
				"stdout":              9,
				"stderr":              10,
				"last_event_sequence": 11,
			},
		},
		{
			name:       "CreateSandboxRequest",
			descriptor: (&agboxv1.CreateSandboxRequest{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"create_spec",
				"caller_metadata",
				"sandbox_id",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"create_spec":     1,
				"caller_metadata": 2,
				"sandbox_id":      3,
			},
		},
		{
			name:       "ListSandboxesRequest",
			descriptor: (&agboxv1.ListSandboxesRequest{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"include_deleted",
				"label_selector",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"include_deleted": 1,
				"label_selector":  2,
			},
		},
		{
			name:       "GetExecResponse",
			descriptor: (&agboxv1.GetExecResponse{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"exec",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"exec": 1,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assertMessageFieldNames(t, testCase.descriptor, testCase.fieldNames)
			assertMessageFieldNumbers(t, testCase.descriptor, testCase.fieldNums)
		})
	}
}

func TestStateRootOnlyServesCopiesAndBuiltinShadowCopy(t *testing.T) {
	sourceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	backendWithoutState := &dockerRuntimeBackend{config: ServiceConfig{}}
	if _, err := backendWithoutState.materializeGenericCopies(
		"sandbox-copy",
		[]*agboxv1.CopySpec{{Source: sourceRoot, Target: "/workspace/project"}},
		&sandboxRuntimeState{},
	); err == nil || !strings.Contains(err.Error(), "runtime.state_root is required for generic copy inputs") {
		t.Fatalf("expected generic copy state_root error, got %v", err)
	}

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	builtinSource := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(builtinSource, 0o755); err != nil {
		t.Fatalf("MkdirAll builtin source failed: %v", err)
	}
	externalRoot := t.TempDir()
	externalFile := filepath.Join(externalRoot, "secret.txt")
	if err := os.WriteFile(externalFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile external file failed: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(builtinSource, "escape-link")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	if _, err := backendWithoutState.materializeBuiltinResources("sandbox-builtin", []string{".claude"}, &sandboxRuntimeState{}); err == nil || !strings.Contains(err.Error(), "runtime.state_root is required for builtin resource shadow copies") {
		t.Fatalf("expected builtin shadow copy state_root error, got %v", err)
	}

	backendWithState := &dockerRuntimeBackend{config: ServiceConfig{StateRoot: t.TempDir()}}
	runtimeState := &sandboxRuntimeState{}
	mounts, err := backendWithState.materializeBuiltinResources("sandbox-builtin", []string{".claude"}, runtimeState)
	if err != nil {
		t.Fatalf("materializeBuiltinResources failed: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected one builtin mount, got %d", len(mounts))
	}
	if runtimeState.ShadowRoot == "" {
		t.Fatal("expected shadow root for builtin shadow copy")
	}
	if !strings.Contains(mounts[0].Source, runtimeState.ShadowRoot) {
		t.Fatalf("expected builtin source to be under shadow root, got source=%q shadow_root=%q", mounts[0].Source, runtimeState.ShadowRoot)
	}
	if mounts[0].ReadOnly {
		t.Fatal("expected writable builtin shadow copy mount to preserve capability mode")
	}
}

func TestProtoEventTypesForServices(t *testing.T) {
	values := agboxv1.EventType(0).Descriptor().Values()
	sandboxReady := values.ByName("SANDBOX_READY")
	ready := values.ByName("SANDBOX_SERVICE_READY")
	failed := values.ByName("SANDBOX_SERVICE_FAILED")
	if sandboxReady == nil || ready == nil || failed == nil {
		t.Fatalf("service event enums are missing: sandbox_ready=%v ready=%v failed=%v", sandboxReady, ready, failed)
	}
	if sandboxReady.Number() != 3 {
		t.Fatalf("unexpected sandbox ready enum number: got=%d want=3", sandboxReady.Number())
	}
	if ready.Number() != 13 || failed.Number() != 14 {
		t.Fatalf("unexpected service event enum numbers: ready=%d failed=%d", ready.Number(), failed.Number())
	}
	legacyName := strings.Join([]string{"SANDBOX", "DEPENDENCY", "READY"}, "_")
	if values.ByName(protoreflect.Name(legacyName)) != nil {
		t.Fatal("legacy dependency event enum should not exist")
	}
}

func statusCode(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Convert(err).Code()
}
