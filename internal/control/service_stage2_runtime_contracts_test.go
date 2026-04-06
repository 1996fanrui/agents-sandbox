package control

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestDockerLabelsPassthrough(t *testing.T) {
	sandboxID := "labels-pass-through"
	networkName := dockerNetworkName(sandboxID)
	userLabels := map[string]string{
		"owner": "team-a",
		"env":   "dev",
	}
	dbContainerName := dockerCompanionContainerName(sandboxID, "db")
	cacheContainerName := dockerCompanionContainerName(sandboxID, "cache")
	primaryContainerName := dockerPrimaryContainerName(sandboxID)

	var mu sync.Mutex
	networkLabels := map[string]string{}
	containerLabels := make(map[string]map[string]string)
	containerExtraHosts := make(map[string][]string)
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
		case r.Method == http.MethodGet && path == "/networks/"+networkName:
			writeDockerJSON(t, w, network.Inspect{
				ID: "abc123def456",
				IPAM: network.IPAM{
					Config: []network.IPAMConfig{
						{Subnet: "172.18.0.0/16", Gateway: "172.18.0.1"},
					},
				},
				Options: map[string]string{
					"com.docker.network.bridge.name": "br-abc123def456",
				},
			})
		case r.Method == http.MethodPost && path == "/containers/create":
			var request struct {
				Labels     map[string]string `json:"Labels"`
				HostConfig struct {
					ExtraHosts []string `json:"ExtraHosts"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			containerLabels[name] = request.Labels
			containerExtraHosts[name] = request.HostConfig.ExtraHosts
			mu.Unlock()
			writeDockerJSON(t, w, map[string]string{"Id": name})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/containers/"+dbContainerName+"/json":
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
		companionContainers: []*agboxv1.CompanionContainerSpec{
			{
				Name:  "db",
				Image: "postgres:16",
				Healthcheck: &agboxv1.HealthcheckConfig{
					Test: []string{"CMD", "true"},
				},
			},
			{
				Name:  "cache",
				Image: "redis:7",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	statuses := collectCompanionContainerStatuses(result.CompanionContainerStatuses)
	if len(statuses) != 2 {
		t.Fatalf("unexpected companion container statuses count: %#v", statuses)
	}

	assertUserDockerLabels(t, networkLabels, map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "primary",
		runtimedocker.LabelProfile:              "default",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})
	assertUserDockerLabels(t, containerLabels[dbContainerName], map[string]string{
		runtimedocker.LabelSandboxID:              sandboxID,
		runtimedocker.LabelComponent:              "companion",
		runtimedocker.LabelCompanionContainerName: "db",
		runtimedocker.LabelUserPrefix + "owner":   "team-a",
		runtimedocker.LabelUserPrefix + "env":     "dev",
	})
	assertUserDockerLabels(t, containerLabels[cacheContainerName], map[string]string{
		runtimedocker.LabelSandboxID:              sandboxID,
		runtimedocker.LabelComponent:              "companion",
		runtimedocker.LabelCompanionContainerName: "cache",
		runtimedocker.LabelUserPrefix + "owner":   "team-a",
		runtimedocker.LabelUserPrefix + "env":     "dev",
	})
	assertUserDockerLabels(t, containerLabels[primaryContainerName], map[string]string{
		runtimedocker.LabelSandboxID:            sandboxID,
		runtimedocker.LabelComponent:            "primary",
		runtimedocker.LabelProfile:              "default",
		runtimedocker.LabelUserPrefix + "owner": "team-a",
		runtimedocker.LabelUserPrefix + "env":   "dev",
	})

	// Verify all containers carry the Docker Desktop host-discovery overrides.
	wantExtraHosts := []string{
		"host.docker.internal:0.0.0.0",
		"gateway.docker.internal:0.0.0.0",
	}
	for _, name := range []string{dbContainerName, cacheContainerName, primaryContainerName} {
		mu.Lock()
		got := containerExtraHosts[name]
		mu.Unlock()
		if !reflect.DeepEqual(got, wantExtraHosts) {
			t.Fatalf("container %s: unexpected ExtraHosts: got=%v want=%v", name, got, wantExtraHosts)
		}
	}
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
		config:       ServiceConfig{Logger: slog.Default()},
		dockerClient: dockerClient,
		nftConn:      noopNftablesConnector{},
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

func TestCompanionContainerHealthcheckValidationAndPassthrough(t *testing.T) {
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
			CompanionContainers: []*agboxv1.CompanionContainerSpec{
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
	healthcheck := runtime.lastCreateSpec.GetCompanionContainers()[0].GetHealthcheck()
	if healthcheck.GetInterval().AsDuration() != 2*time.Second || healthcheck.GetTimeout().AsDuration() != time.Second || healthcheck.GetRetries() != 4 || healthcheck.GetStartPeriod().AsDuration() != 10*time.Second || healthcheck.GetStartInterval().AsDuration() != 2*time.Second {
		t.Fatalf("healthcheck was not passed through: %#v", healthcheck)
	}

	invalidCases := []struct {
		name   string
		spec   *agboxv1.CreateSpec
		errMsg string
	}{
		{
			name: "invalid_test_keyword",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				CompanionContainers: []*agboxv1.CompanionContainerSpec{
					{Name: "cache", Image: "redis:7", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"INVALID"}}},
				},
			},
			errMsg: "is invalid",
		},
		{
			name: "duplicate_name",
			spec: &agboxv1.CreateSpec{
				Image: "ghcr.io/agents-sandbox/coding-runtime:test",
				CompanionContainers: []*agboxv1.CompanionContainerSpec{
					{Name: "db", Image: "postgres:16"},
					{Name: "db", Image: "redis:7"},
				},
			},
			errMsg: "duplicate companion container name",
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

func TestPostStartOnPrimaryRequiresHealthcheck(t *testing.T) {
	validSpec := &agboxv1.CreateSpec{
		Image: "ghcr.io/agents-sandbox/coding-runtime:test",
		CompanionContainers: []*agboxv1.CompanionContainerSpec{
			{
				Name:               "db",
				Image:              "postgres:16",
				PostStartOnPrimary: []string{"echo ready"},
				Healthcheck:        &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}},
			},
		},
	}
	if err := validateCreateSpec(validSpec); err != nil {
		t.Fatalf("companion container post_start_on_primary with healthcheck should be valid: %v", err)
	}

	invalidSpec := &agboxv1.CreateSpec{
		Image: "ghcr.io/agents-sandbox/coding-runtime:test",
		CompanionContainers: []*agboxv1.CompanionContainerSpec{
			{
				Name:               "cache",
				Image:              "redis:7",
				PostStartOnPrimary: []string{"echo invalid"},
			},
		},
	}
	if err := validateCreateSpec(invalidSpec); err == nil || !strings.Contains(err.Error(), "with post_start_on_primary must define healthcheck") {
		t.Fatalf("companion container post_start_on_primary without healthcheck should be rejected, got %v", err)
	}
}

func TestCompanionContainerLifecycleForResumeStopAndDelete(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	createResp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "cc-lifecycle",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
			CompanionContainers: []*agboxv1.CompanionContainerSpec{
				{Name: "db", Image: "postgres:16", Healthcheck: &agboxv1.HealthcheckConfig{Test: []string{"CMD", "true"}}},
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
			Image:        "ghcr.io/agents-sandbox/coding-runtime:test",
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
				"companion_containers",
				"labels",
				"envs",
				"idle_ttl",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"image":                1,
				"mounts":               2,
				"copies":               3,
				"builtin_tools":        4,
				"companion_containers": 5,
				"labels":               6,
				"envs":                 7,
				"idle_ttl":             8,
			},
		},
		{
			name:       "SandboxHandle",
			descriptor: (&agboxv1.SandboxHandle{}).ProtoReflect().Descriptor(),
			fieldNames: []string{
				"sandbox_id",
				"state",
				"last_event_sequence",
				"companion_containers",
				"labels",
				"created_at",
				"image",
				"error_code",
				"error_message",
				"state_changed_at",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"sandbox_id":           1,
				"state":                2,
				"last_event_sequence":  3,
				"companion_containers": 4,
				"labels":               5,
				"created_at":           6,
				"image":                7,
				"error_code":           8,
				"error_message":        9,
				"state_changed_at":     10,
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
				"companion_container",
			},
			fieldNums: map[string]protoreflect.FieldNumber{
				"event_id":            1,
				"sequence":            2,
				"sandbox_id":          3,
				"event_type":          4,
				"occurred_at":         5,
				"replay":              6,
				"snapshot":            7,
				"sandbox_state":       8,
				"sandbox_phase":       9,
				"exec":                10,
				"companion_container": 11,
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

func TestBuiltinToolMountsPreserveSymlinks(t *testing.T) {
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

	// Prevent optional PulseAudio socket from being resolved during test.
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(homeDir, "nonexistent-runtime"))

	// Builtin resources are mounted directly from the host path; symlinks are preserved as-is.
	runtimeState := &sandboxRuntimeState{}
	backendWithoutState := &dockerRuntimeBackend{config: ServiceConfig{}}
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

func TestMaterializeBuiltinToolsSkipsOptionalWhenHostPathMissing(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	backendWithoutState := &dockerRuntimeBackend{
		config: ServiceConfig{},
	}

	// Request an optional tool (uv) whose host paths do not exist.
	// materializeBuiltinTools should skip them silently instead of failing.
	mounts, err := backendWithoutState.materializeBuiltinTools("sandbox-optional-skip", []string{"uv"}, &sandboxRuntimeState{})
	if err != nil {
		t.Fatalf("expected optional tool mounts to be skipped, got error: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("expected zero mounts when optional host paths are missing, got %d", len(mounts))
	}

	// When mixing required and optional tools, the required tool must still fail
	// if its path is missing.
	if _, err := backendWithoutState.materializeBuiltinTools("sandbox-mixed", []string{"uv", "claude"}, &sandboxRuntimeState{}); err == nil {
		t.Fatal("expected error for required tool (claude) with missing host path, got nil")
	}
}

func TestProtoEventTypesForCompanionContainers(t *testing.T) {
	values := agboxv1.EventType(0).Descriptor().Values()
	sandboxReady := values.ByName("SANDBOX_READY")
	ready := values.ByName("COMPANION_CONTAINER_READY")
	failed := values.ByName("COMPANION_CONTAINER_FAILED")
	if sandboxReady == nil || ready == nil || failed == nil {
		t.Fatalf("companion container event enums are missing: sandbox_ready=%v ready=%v failed=%v", sandboxReady, ready, failed)
	}
	if sandboxReady.Number() != 3 {
		t.Fatalf("unexpected sandbox ready enum number: got=%d want=3", sandboxReady.Number())
	}
	if ready.Number() != 13 || failed.Number() != 14 {
		t.Fatalf("unexpected companion container event enum numbers: ready=%d failed=%d", ready.Number(), failed.Number())
	}
	legacyName := strings.Join([]string{"SANDBOX", "SERVICE", "READY"}, "_")
	if values.ByName(protoreflect.Name(legacyName)) != nil {
		t.Fatal("legacy service event enum should not exist")
	}
}

func statusCode(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Convert(err).Code()
}
