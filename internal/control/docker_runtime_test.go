package control

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestDockerRuntimeNamesPreserveSandboxIDUnderscores(t *testing.T) {
	sandboxID := "paseo-ai_tools-1234"

	if got, want := dockerNetworkName(sandboxID), "agbox-net-paseo-ai_tools-1234"; got != want {
		t.Fatalf("network name mismatch: got %q want %q", got, want)
	}
	if got, want := dockerPrimaryContainerName(sandboxID), "agbox-primary-paseo-ai_tools-1234"; got != want {
		t.Fatalf("primary container name mismatch: got %q want %q", got, want)
	}
	if got, want := dockerCompanionContainerName(sandboxID, "side_car"), "agbox-cc-paseo-ai_tools-1234-side_car"; got != want {
		t.Fatalf("companion container name mismatch: got %q want %q", got, want)
	}
}

func TestPrimaryContainerEnvironmentIncludesHostIdentity(t *testing.T) {
	environment := primaryContainerEnvironment(nil)

	if got, want := environment["HOST_UID"], strconv.Itoa(os.Getuid()); got != want {
		t.Fatalf("unexpected HOST_UID: got %q want %q", got, want)
	}
	if got, want := environment["HOST_GID"], strconv.Itoa(os.Getgid()); got != want {
		t.Fatalf("unexpected HOST_GID: got %q want %q", got, want)
	}
	if _, exists := environment["SSH_AUTH_SOCK"]; exists {
		t.Fatalf("unexpected SSH_AUTH_SOCK without ssh-agent mount: %#v", environment)
	}
	if _, exists := environment["PULSE_SERVER"]; exists {
		t.Fatalf("unexpected PULSE_SERVER without pulse-audio mount: %#v", environment)
	}
}

func TestPrimaryContainerEnvironmentIncludesPulseServerWhenMounted(t *testing.T) {
	environment := primaryContainerEnvironment([]dockerMount{
		{Target: "/pulse-audio"},
	})
	if got, want := environment["PULSE_SERVER"], "unix:/pulse-audio"; got != want {
		t.Fatalf("unexpected PULSE_SERVER: got %q want %q", got, want)
	}
}

// TestPrimaryCommandDefaultsToSleepLoop covers AT-HGWY.
func TestPrimaryCommandDefaultsToSleepLoop(t *testing.T) {
	defaultCmd := primaryContainerCommand(nil)
	want := []string{
		"sh",
		"-lc",
		"trap 'exit 0' TERM INT; while sleep 3600; do :; done",
	}
	if !reflect.DeepEqual(defaultCmd, want) {
		t.Fatalf("sleep-loop default mismatch: got %#v want %#v", defaultCmd, want)
	}

	userCmd := primaryContainerCommand([]string{"myworker", "serve"})
	if !reflect.DeepEqual(userCmd, []string{"myworker", "serve"}) {
		t.Fatalf("user command not propagated: got %#v", userCmd)
	}
}

// TestCompanionCommandDefaultsToImageCMD covers AT-DN45 by driving
// startCompanionContainersAsync with a fake createContainer capture.
func TestCompanionCommandDefaultsToImageCMD(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    []string
	}{
		{name: "nil_command_falls_back_to_image_cmd", command: nil, want: nil},
		{name: "user_command_forwarded", command: []string{"redis-server", "--appendonly", "yes"}, want: []string{"redis-server", "--appendonly", "yes"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				captured dockerContainerSpec
			)
			create := func(_ context.Context, spec dockerContainerSpec) error {
				mu.Lock()
				defer mu.Unlock()
				captured = spec
				return nil
			}
			start := func(_ context.Context, _ string) error { return nil }
			cc := &agboxv1.CompanionContainerSpec{
				Name:    "cache",
				Image:   "redis:7",
				Command: tc.command,
			}
			starts := startCompanionContainersAsync(
				context.Background(),
				"sandbox-test",
				"net-test",
				"primary-test",
				[]*agboxv1.CompanionContainerSpec{cc},
				nil,
				nil,
				nil,
				nil,
				nil,
				create,
				start,
			)
			for range starts.Statuses {
			}
			<-starts.done
			mu.Lock()
			got := captured.Command
			mu.Unlock()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("companion command mismatch: got %#v want %#v", got, tc.want)
			}
		})
	}
}

// TestExecPathUnaffectedByPrimaryCommand covers AT-TYRU: the docker exec
// wiring must not consume CreateSpec.Command, so two otherwise identical
// sandboxes (one with command, one without) should emit the same exec
// event sequence.
func TestExecPathUnaffectedByPrimaryCommand(t *testing.T) {
	// This test exercises the bufconn-backed service twice and confirms
	// that the events produced by CreateExec -> GetExec are the same
	// regardless of whether CreateSpec.Command was set.
	scenarios := []struct {
		name    string
		command []string
	}{
		{name: "no_command", command: nil},
		{name: "with_command", command: []string{"sleep", "60"}},
	}
	type observation struct {
		execState agboxv1.ExecState
		exitCode  int32
	}
	var results []observation
	for _, sc := range scenarios {
		runtime := &capturingRuntimeBackend{}
		client := newBufconnClient(t, ServiceConfig{
			TransitionDelay: 5 * time.Millisecond,
			PollInterval:    2 * time.Millisecond,
			runtimeBackend:  runtime,
		})
		req := createSandboxRequest("sandbox-"+sc.name, "img:test")
		req.CreateSpec.Command = sc.command
		createResp, err := client.CreateSandbox(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateSandbox[%s] failed: %v", sc.name, err)
		}
		waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

		execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
			SandboxId: createResp.GetSandbox().GetSandboxId(),
			Command:   []string{"echo", "hi"},
		})
		if err != nil {
			t.Fatalf("CreateExec[%s] failed: %v", sc.name, err)
		}
		waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)
		execStatus, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execResp.GetExecId()})
		if err != nil {
			t.Fatalf("GetExec[%s] failed: %v", sc.name, err)
		}
		results = append(results, observation{
			execState: execStatus.GetExec().GetState(),
			exitCode:  execStatus.GetExec().GetExitCode(),
		})
	}
	if results[0] != results[1] {
		t.Fatalf("exec observations differ between command scenarios: %#v vs %#v", results[0], results[1])
	}
}

// TestEntrypointNotExposed covers AT-19J8: neither proto nor YAML surface
// an entrypoint field.
func TestEntrypointNotExposed(t *testing.T) {
	createSpecFields := (&agboxv1.CreateSpec{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < createSpecFields.Len(); i++ {
		name := string(createSpecFields.Get(i).Name())
		if name == "entrypoint" || name == "Entrypoint" {
			t.Fatalf("CreateSpec unexpectedly exposes field %q", name)
		}
	}
	ccSpecFields := (&agboxv1.CompanionContainerSpec{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < ccSpecFields.Len(); i++ {
		name := string(ccSpecFields.Get(i).Name())
		if name == "entrypoint" || name == "Entrypoint" {
			t.Fatalf("CompanionContainerSpec unexpectedly exposes field %q", name)
		}
	}

	yamlType := reflect.TypeOf(YAMLConfig{})
	for i := 0; i < yamlType.NumField(); i++ {
		f := yamlType.Field(i)
		if f.Name == "Entrypoint" {
			t.Fatalf("YAMLConfig unexpectedly has Entrypoint field")
		}
		if tag := f.Tag.Get("yaml"); tag == "entrypoint" {
			t.Fatalf("YAMLConfig unexpectedly has yaml tag 'entrypoint' on %s", f.Name)
		}
	}
	ccType := reflect.TypeOf(YAMLCompanionContainerSpec{})
	for i := 0; i < ccType.NumField(); i++ {
		f := ccType.Field(i)
		if f.Name == "Entrypoint" {
			t.Fatalf("YAMLCompanionContainerSpec unexpectedly has Entrypoint field")
		}
		if tag := f.Tag.Get("yaml"); tag == "entrypoint" {
			t.Fatalf("YAMLCompanionContainerSpec unexpectedly has yaml tag 'entrypoint' on %s", f.Name)
		}
	}

	// yaml.v3 strict KnownFields(true) rejects unknown top-level keys, so
	// 'entrypoint' in user YAML is surfaced as a parse error. That is the
	// expected behavior: no silent acceptance, and no CreateSpec entrypoint
	// to land on.
	raw := []byte("image: img:test\nentrypoint: [\"foo\"]\n")
	if _, err := parseYAMLConfig(raw); err == nil {
		t.Fatal("expected strict YAML parser to reject unknown 'entrypoint' key")
	}
}

func TestPrimaryContainerEnvironmentIncludesSshAuthSockWhenMounted(t *testing.T) {
	environment := primaryContainerEnvironment([]dockerMount{
		{Target: "/ssh-agent"},
	})

	if got, want := environment["SSH_AUTH_SOCK"], "/ssh-agent"; got != want {
		t.Fatalf("unexpected SSH_AUTH_SOCK: got %q want %q", got, want)
	}
	if got, want := environment["HOST_UID"], strconv.Itoa(os.Getuid()); got != want {
		t.Fatalf("unexpected HOST_UID: got %q want %q", got, want)
	}
	if got, want := environment["HOST_GID"], strconv.Itoa(os.Getgid()); got != want {
		t.Fatalf("unexpected HOST_GID: got %q want %q", got, want)
	}
}

// TestPortBindingsAppendsSharedContainerPort guards against a regression
// where direct map assignment dropped earlier bindings whenever multiple
// PortMappings shared the same (container_port, protocol) but differed in
// host_port. After the fix, both host ports must be present in the
// resulting nat.PortMap entry.
func TestPortBindingsAppendsSharedContainerPort(t *testing.T) {
	exposed, bindings, err := buildPortBindings([]dockerPortMapping{
		{ContainerPort: 8080, HostPort: 18789, Protocol: "tcp"},
		{ContainerPort: 8080, HostPort: 9999, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatalf("buildPortBindings: %v", err)
	}
	if len(exposed) != 1 {
		t.Fatalf("expected 1 exposed port (deduped by key), got %d", len(exposed))
	}
	var entries []string
	for natPort, list := range bindings {
		if natPort.Port() != "8080" || natPort.Proto() != "tcp" {
			continue
		}
		for _, b := range list {
			entries = append(entries, b.HostPort)
		}
	}
	hostPorts := map[string]bool{}
	for _, e := range entries {
		hostPorts[e] = true
	}
	if !hostPorts["18789"] || !hostPorts["9999"] {
		t.Fatalf("expected both 18789 and 9999 host_port bindings preserved, got %v", entries)
	}
}
