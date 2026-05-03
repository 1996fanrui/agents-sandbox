package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

type capturedGPUCreate struct {
	DeviceRequests []container.DeviceRequest
	GroupAdd       []string
	Env            []string
	Privileged     bool
	Devices        []container.DeviceMapping
}

type fakeGPUDeviceInfo struct {
	name string
	gid  uint32
}

func (info fakeGPUDeviceInfo) Name() string       { return info.name }
func (info fakeGPUDeviceInfo) Size() int64        { return 0 }
func (info fakeGPUDeviceInfo) Mode() os.FileMode  { return os.ModeDevice | os.ModeCharDevice | 0o660 }
func (info fakeGPUDeviceInfo) ModTime() time.Time { return time.Time{} }
func (info fakeGPUDeviceInfo) IsDir() bool        { return false }
func (info fakeGPUDeviceInfo) Sys() any           { return &syscall.Stat_t{Gid: info.gid} }

func withFakeGPUDevices(t *testing.T, groups map[string]uint32) {
	t.Helper()

	deviceRoot := t.TempDir()
	driRoot := filepath.Join(deviceRoot, "dri")
	if err := os.MkdirAll(driRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll fake dri root failed: %v", err)
	}
	paths := make(map[string]uint32, len(groups))
	for name, gid := range groups {
		path := filepath.Join(deviceRoot, name)
		if strings.HasPrefix(name, "dri/") {
			path = filepath.Join(deviceRoot, name)
		}
		if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
			t.Fatalf("WriteFile fake GPU device failed: %v", err)
		}
		paths[path] = gid
	}

	oldPatterns := gpuDeviceGroupGlobPatterns
	oldStat := gpuDeviceStat
	gpuDeviceGroupGlobPatterns = []string{
		filepath.Join(deviceRoot, "nvidia*"),
		filepath.Join(driRoot, "renderD*"),
	}
	gpuDeviceStat = func(path string) (os.FileInfo, error) {
		gid, ok := paths[path]
		if !ok {
			return oldStat(path)
		}
		return fakeGPUDeviceInfo{name: filepath.Base(path), gid: gid}, nil
	}
	t.Cleanup(func() {
		gpuDeviceGroupGlobPatterns = oldPatterns
		gpuDeviceStat = oldStat
	})
}

func dockerGPUHandler(t *testing.T, sandboxID string, captures map[string]capturedGPUCreate, mu *sync.Mutex) http.HandlerFunc {
	t.Helper()

	networkName := dockerNetworkName(sandboxID)
	primaryContainerName := dockerPrimaryContainerName(sandboxID)
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			writeDockerJSON(t, w, map[string]string{"Id": "sha256:test"})
		case r.Method == http.MethodPost && path == "/networks/create":
			writeDockerJSON(t, w, map[string]string{"Id": "network-1"})
		case r.Method == http.MethodGet && path == "/networks/"+networkName:
			writeDockerJSON(t, w, network.Inspect{
				ID:      "abc123",
				IPAM:    network.IPAM{Config: []network.IPAMConfig{{Subnet: "172.18.0.0/16", Gateway: "172.18.0.1"}}},
				Options: map[string]string{"com.docker.network.bridge.name": "br-abc123"},
			})
		case r.Method == http.MethodPost && path == "/containers/create":
			var request struct {
				Env        []string `json:"Env"`
				HostConfig struct {
					DeviceRequests []container.DeviceRequest `json:"DeviceRequests"`
					GroupAdd       []string                  `json:"GroupAdd"`
					Privileged     bool                      `json:"Privileged"`
					Devices        []container.DeviceMapping `json:"Devices"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			captures[name] = capturedGPUCreate{
				DeviceRequests: request.HostConfig.DeviceRequests,
				GroupAdd:       request.HostConfig.GroupAdd,
				Env:            request.Env,
				Privileged:     request.HostConfig.Privileged,
				Devices:        request.HostConfig.Devices,
			}
			mu.Unlock()
			writeDockerJSON(t, w, map[string]string{"Id": name})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/containers/"+primaryContainerName+"/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: true, Status: "running"},
				},
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func assertAllGPUDeviceRequest(t *testing.T, requests []container.DeviceRequest) {
	t.Helper()

	if len(requests) != 1 {
		t.Fatalf("expected one GPU device request, got %#v", requests)
	}
	request := requests[0]
	if request.Driver != "nvidia" {
		t.Fatalf("unexpected GPU driver: %q", request.Driver)
	}
	if request.Count != -1 {
		t.Fatalf("unexpected GPU count: %d", request.Count)
	}
	if !reflect.DeepEqual(request.Capabilities, [][]string{{"gpu"}}) {
		t.Fatalf("unexpected GPU capabilities: %#v", request.Capabilities)
	}
	if len(request.DeviceIDs) != 0 {
		t.Fatalf("primary all-GPU request must not select device IDs: %#v", request.DeviceIDs)
	}
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

func TestDockerCreateGPUDeviceRequestInjected(t *testing.T) {
	withFakeGPUDevices(t, map[string]uint32{"nvidiactl": 107})
	sandboxID := "gpu-inject"
	captures := make(map[string]capturedGPUCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerGPUHandler(t, sandboxID, captures, &mu))

	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{Image: "img:test", Gpus: "all"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	primary := captures[dockerPrimaryContainerName(sandboxID)]
	assertAllGPUDeviceRequest(t, primary.DeviceRequests)
	if primary.Privileged {
		t.Fatal("GPU support must not enable privileged mode")
	}
	if len(primary.Devices) != 0 {
		t.Fatalf("GPU support must not hand-map host devices, got %#v", primary.Devices)
	}
}

func TestDockerCreateNoGPUDeviceRequest(t *testing.T) {
	withFakeGPUDevices(t, map[string]uint32{"nvidiactl": 107})
	sandboxID := "gpu-empty"
	captures := make(map[string]capturedGPUCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerGPUHandler(t, sandboxID, captures, &mu))

	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{
			Image: "img:test",
			Envs: map[string]string{
				supplementalGroupsEnv: "999",
				"USER_ENV":            "kept",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	primary := captures[dockerPrimaryContainerName(sandboxID)]
	if len(primary.DeviceRequests) != 0 {
		t.Fatalf("empty gpus must not set DeviceRequests, got %#v", primary.DeviceRequests)
	}
	if len(primary.GroupAdd) != 0 {
		t.Fatalf("empty gpus must not set GPU supplemental groups, got %#v", primary.GroupAdd)
	}
	if _, ok := envValue(primary.Env, supplementalGroupsEnv); ok {
		t.Fatalf("empty gpus must not set %s, env=%#v", supplementalGroupsEnv, primary.Env)
	}
	if got, ok := envValue(primary.Env, "USER_ENV"); !ok || got != "kept" {
		t.Fatalf("unrelated user env missing: got=%q ok=%v env=%#v", got, ok, primary.Env)
	}
}

func TestDockerCreateGPUDeviceRequestPrimaryOnly(t *testing.T) {
	withFakeGPUDevices(t, map[string]uint32{"nvidiactl": 107})
	sandboxID := "gpu-primary-only"
	captures := make(map[string]capturedGPUCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerGPUHandler(t, sandboxID, captures, &mu))

	companion := &agboxv1.CompanionContainerSpec{Name: "db", Image: "postgres:16"}
	result, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle:              &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec:          &agboxv1.CreateSpec{Image: "img:test", Gpus: "all", CompanionContainers: []*agboxv1.CompanionContainerSpec{companion}},
		companionContainers: []*agboxv1.CompanionContainerSpec{companion},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	_ = collectCompanionContainerStatuses(result.CompanionContainerStatuses)

	primary := captures[dockerPrimaryContainerName(sandboxID)]
	assertAllGPUDeviceRequest(t, primary.DeviceRequests)

	companionCapture := captures[dockerCompanionContainerName(sandboxID, "db")]
	if len(companionCapture.DeviceRequests) != 0 {
		t.Fatalf("companion container must not inherit primary GPU request, got %#v", companionCapture.DeviceRequests)
	}
	if len(companionCapture.GroupAdd) != 0 {
		t.Fatalf("companion container must not inherit primary GPU groups, got %#v", companionCapture.GroupAdd)
	}
}

func TestDockerCreateGPUDeviceGroupsForNonRootRuntimeUser(t *testing.T) {
	withFakeGPUDevices(t, map[string]uint32{
		"nvidiactl":    107,
		"nvidia0":      107,
		"dri/renderD1": 44,
	})
	sandboxID := "gpu-groups"
	captures := make(map[string]capturedGPUCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerGPUHandler(t, sandboxID, captures, &mu))

	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{
			Image: "img:test",
			Gpus:  "all",
			Envs: map[string]string{
				supplementalGroupsEnv: "999",
				"USER_ENV":            "kept",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	primary := captures[dockerPrimaryContainerName(sandboxID)]
	if !reflect.DeepEqual(primary.GroupAdd, []string{"44", "107"}) {
		t.Fatalf("unexpected GPU supplemental groups: got=%#v want=%#v", primary.GroupAdd, []string{"44", "107"})
	}
	if !slices.Contains(primary.Env, supplementalGroupsEnv+"=44,107") {
		t.Fatalf("%s env missing from primary container env: %#v", supplementalGroupsEnv, primary.Env)
	}
	if got, ok := envValue(primary.Env, supplementalGroupsEnv); !ok || got != "44,107" {
		t.Fatalf("user env must not override GPU supplemental groups: got=%q ok=%v env=%#v", got, ok, primary.Env)
	}
	if got, ok := envValue(primary.Env, "USER_ENV"); !ok || got != "kept" {
		t.Fatalf("unrelated user env missing: got=%q ok=%v env=%#v", got, ok, primary.Env)
	}
}

func TestDockerExecGPUDeviceGroupsAvailableToAgboxUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dockerClient := newDockerIntegrationClient(t, ctx)
	imageRef := selectDockerExecGroupTestImage(t, ctx, dockerClient)
	containerName := dockerIntegrationContainerName("agbox-gpu-exec-groups")
	targetGroupID := "42442"
	setupScript := fmt.Sprintf(`
set -eu
printf 'agbox-primary:x:42441:\n' >> /etc/group
printf 'agbox-gpu:x:%[1]s:agbox\n' >> /etc/group
printf 'agbox:x:42441:42441:agbox:/home/agbox:/bin/sh\n' >> /etc/passwd
mkdir -p /home/agbox
chmod 755 /home/agbox
sleep 300
`, targetGroupID)

	createResponse, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image:      imageRef,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{setupScript},
	}, &container.HostConfig{
		GroupAdd:    []string{targetGroupID},
		NetworkMode: "none",
	}, nil, nil, containerName)
	if err != nil {
		t.Fatalf("create Docker integration container failed: %v", err)
	}
	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer removeCancel()
		if err := dockerClient.ContainerRemove(removeCtx, createResponse.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !errdefs.IsNotFound(err) {
			t.Fatalf("remove Docker integration container failed: %v", err)
		}
	})
	if err := dockerClient.ContainerStart(ctx, createResponse.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start Docker integration container failed: %v", err)
	}

	backend := &dockerRuntimeBackend{
		config:       ServiceConfig{PollInterval: 10 * time.Millisecond},
		dockerClient: dockerClient,
	}
	waitForAgboxUserGroupRegistration(t, ctx, backend, createResponse.ID, targetGroupID)

	var execStdout bytes.Buffer
	_, err = backend.dockerExec(ctx, dockerExecSpec{
		ContainerName: createResponse.ID,
		Command:       []string{"id", "-G"},
		User:          "agbox",
		Stdout:        &execStdout,
	})
	if err != nil {
		t.Fatalf("dockerExec id -G failed: %v", err)
	}

	if !slices.Contains(strings.Fields(execStdout.String()), targetGroupID) {
		t.Fatalf("docker exec --user agbox group list must include supplemental group %s, stdout=%q", targetGroupID, execStdout.String())
	}
}

func newDockerIntegrationClient(t *testing.T, ctx context.Context) *client.Client {
	t.Helper()

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker integration skipped: Docker client cannot be initialized: %v", err)
	}
	if _, err := dockerClient.Ping(ctx); err != nil {
		if closeErr := dockerClient.Close(); closeErr != nil {
			t.Logf("close Docker client after failed ping: %v", closeErr)
		}
		t.Skipf("Docker integration skipped: Docker daemon is unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Fatalf("Docker client close failed: %v", err)
		}
	})
	return dockerClient
}

func selectDockerExecGroupTestImage(t *testing.T, ctx context.Context, dockerClient *client.Client) string {
	t.Helper()

	for _, imageRef := range dockerExecGroupImageCandidates(t, ctx, dockerClient) {
		ok, message := dockerImageSupportsExecGroupTest(ctx, dockerClient, imageRef)
		if ok {
			return imageRef
		}
		t.Logf("Docker integration image %q is not suitable: %s", imageRef, message)
	}
	t.Skip("Docker integration skipped: no local Docker image with sh and id is available; set AGENTS_SANDBOX_DOCKER_TEST_IMAGE to a suitable local image")
	return ""
}

func dockerExecGroupImageCandidates(t *testing.T, ctx context.Context, dockerClient *client.Client) []string {
	t.Helper()

	candidates := make([]string, 0)
	seen := make(map[string]struct{})
	add := func(imageRef string) {
		if imageRef == "" || imageRef == "<none>:<none>" {
			return
		}
		if _, exists := seen[imageRef]; exists {
			return
		}
		seen[imageRef] = struct{}{}
		candidates = append(candidates, imageRef)
	}
	add(os.Getenv("AGENTS_SANDBOX_DOCKER_TEST_IMAGE"))
	for _, imageRef := range []string{
		"busybox:latest",
		"alpine:latest",
		"debian:bookworm-slim",
		"ubuntu:latest",
		"ghcr.io/agents-sandbox/coding-runtime:test",
		"ghcr.io/agents-sandbox/coding-runtime:latest",
	} {
		add(imageRef)
	}
	images, err := dockerClient.ImageList(ctx, imagetypes.ListOptions{})
	if err != nil {
		t.Logf("list local Docker images failed: %v", err)
		return candidates
	}
	for _, imageSummary := range images {
		for _, repoTag := range imageSummary.RepoTags {
			add(repoTag)
		}
	}
	return candidates
}

func dockerImageSupportsExecGroupTest(ctx context.Context, dockerClient *client.Client, imageRef string) (bool, string) {
	if _, _, err := dockerClient.ImageInspectWithRaw(ctx, imageRef); err != nil {
		if errdefs.IsNotFound(err) {
			return false, "image is not present locally"
		}
		return false, err.Error()
	}
	containerName := dockerIntegrationContainerName("agbox-gpu-exec-probe")
	createResponse, err := dockerClient.ContainerCreate(ctx, &container.Config{
		Image:      imageRef,
		Entrypoint: []string{"sh", "-c"},
		Cmd:        []string{"id -G >/dev/null"},
	}, &container.HostConfig{NetworkMode: "none"}, nil, nil, containerName)
	if err != nil {
		return false, err.Error()
	}
	defer func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = dockerClient.ContainerRemove(removeCtx, createResponse.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	}()
	if err := dockerClient.ContainerStart(ctx, createResponse.ID, container.StartOptions{}); err != nil {
		return false, err.Error()
	}
	statusCh, errCh := dockerClient.ContainerWait(ctx, createResponse.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return false, err.Error()
		}
		return false, "container wait ended without an exit status"
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return false, fmt.Sprintf("probe exited with status %d", status.StatusCode)
		}
		return true, ""
	case <-ctx.Done():
		return false, ctx.Err().Error()
	}
}

func waitForAgboxUserGroupRegistration(t *testing.T, ctx context.Context, backend *dockerRuntimeBackend, containerID string, targetGroupID string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var stdout bytes.Buffer
		_, err := backend.dockerExec(ctx, dockerExecSpec{
			ContainerName: containerID,
			Command:       []string{"id", "-G"},
			User:          "agbox",
			Stdout:        &stdout,
		})
		if err == nil && slices.Contains(strings.Fields(stdout.String()), targetGroupID) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agbox user supplemental group registration did not complete: err=%v stdout=%q", err, stdout.String())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("agbox user supplemental group registration canceled: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func dockerIntegrationContainerName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano())
}
