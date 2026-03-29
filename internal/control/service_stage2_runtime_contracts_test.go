package control

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
)

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

	serverAddr := server.Listener.Addr().String()
	// Use WithDialContext to route all connections (including hijacked exec
	// streams) to the test server. WithHTTPClient alone does not cover
	// hijacked connections because the Docker client dials them directly.
	dockerClient, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+serverAddr),
		client.WithDialContext(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", serverAddr)
		}),
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
						Interval:      durationpb.New(2 * time.Second),
						Timeout:       durationpb.New(1 * time.Second),
						Retries:       4,
						StartPeriod:   durationpb.New(10 * time.Second),
						StartInterval: durationpb.New(2 * time.Second),
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
	if healthcheck.GetInterval().AsDuration() != 2*time.Second || healthcheck.GetTimeout().AsDuration() != time.Second || healthcheck.GetRetries() != 4 || healthcheck.GetStartPeriod().AsDuration() != 10*time.Second || healthcheck.GetStartInterval().AsDuration() != 2*time.Second {
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
	if err := validateCreateSpec(optionalSpec); err == nil || !strings.Contains(err.Error(), "with post_start_on_primary must define healthcheck") {
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
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.StopSandbox(context.Background(), &agboxv1.StopSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_STOPPED)

	if _, err := client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("ResumeSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if _, err := client.DeleteSandbox(context.Background(), &agboxv1.DeleteSandboxRequest{SandboxId: createResp.GetSandbox().GetSandboxId()}); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_DELETED)
}

func TestBuiltinToolsForwardedToRuntime(t *testing.T) {
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
			BuiltinTools: []string{"claude"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	if runtime.lastCreateSpec == nil || len(runtime.lastCreateSpec.GetBuiltinTools()) != 1 || runtime.lastCreateSpec.GetBuiltinTools()[0] != "claude" {
		t.Fatalf("builtin_tools were not forwarded to runtime: %#v", runtime.lastCreateSpec)
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
				"builtin_tools",
				"required_services",
				"optional_services",
				"labels",
				"envs",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"image":             1,
				"mounts":            2,
				"copies":            3,
				"builtin_tools": 4,
				"required_services": 5,
				"optional_services": 6,
				"labels":            7,
				"envs":              8,
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
				"created_at",
				"image",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"sandbox_id":          1,
				"state":               2,
				"last_event_sequence": 3,
				"required_services":   4,
				"optional_services":   5,
				"labels":              6,
				"created_at":          7,
				"image":               8,
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
				"sandbox_state",
				"sandbox_phase",
				"exec",
				"service",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"event_id":      1,
				"sequence":      2,
				"sandbox_id":    3,
				"event_type":    4,
				"occurred_at":   5,
				"replay":        6,
				"snapshot":      7,
				"sandbox_state": 8,
				"sandbox_phase": 9,
				"exec":          10,
				"service":       11,
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
				"last_event_sequence": 9,
			},
		},
		{
			name:       "CreateSandboxRequest",
			descriptor: (&agboxv1.CreateSandboxRequest{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"create_spec",
				"sandbox_id",
				"config_yaml",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"create_spec": 1,
				"sandbox_id":  2,
				"config_yaml": 3,
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
	claudeJSONSource := filepath.Join(homeDir, ".claude.json")
	if err := os.WriteFile(claudeJSONSource, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile .claude.json failed: %v", err)
	}
	externalRoot := t.TempDir()
	externalFile := filepath.Join(externalRoot, "secret.txt")
	if err := os.WriteFile(externalFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile external file failed: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(builtinSource, "escape-link")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	// Builtin resources are mounted directly from the host path without shadow
	// copies; StateRoot is not required and symlinks are preserved as-is.
	runtimeState := &sandboxRuntimeState{}
	mounts, err := backendWithoutState.materializeBuiltinTools("sandbox-builtin", []string{"claude"}, runtimeState)
	if err != nil {
		t.Fatalf("materializeBuiltinTools failed: %v", err)
	}
	if len(mounts) != 2 {
		t.Fatalf("expected two builtin mounts, got %d", len(mounts))
	}
	if mounts[0].Source != builtinSource {
		t.Fatalf("expected builtin source to be %q, got %q", builtinSource, mounts[0].Source)
	}
	if mounts[0].ReadOnly {
		t.Fatal("expected writable builtin mount to preserve capability mode")
	}
	if mounts[1].Source != claudeJSONSource {
		t.Fatalf("expected .claude.json source to be %q, got %q", claudeJSONSource, mounts[1].Source)
	}
	if mounts[1].ReadOnly {
		t.Fatal("expected writable .claude.json mount to preserve capability mode")
	}

	// Negative case: when ~/.claude.json does not exist on host, materializeBuiltinTools
	// must fail because os.Stat will return an error for the missing file.
	if err := os.Remove(claudeJSONSource); err != nil {
		t.Fatalf("Remove .claude.json failed: %v", err)
	}
	if _, err := backendWithoutState.materializeBuiltinTools("sandbox-builtin-missing", []string{"claude"}, &sandboxRuntimeState{}); err == nil {
		t.Fatal("expected error when ~/.claude.json does not exist, got nil")
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
