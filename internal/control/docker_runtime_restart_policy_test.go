package control

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// TestDockerRestartPolicyUnlessStopped verifies that both primary and companion
// containers are created with RestartPolicy=unless-stopped so Docker auto-recovers
// them after host reboot / daemon restart while still honoring explicit stop.
func TestDockerRestartPolicyUnlessStopped(t *testing.T) {
	sandboxID := "restart-policy"
	networkName := dockerNetworkName(sandboxID)
	companionContainerName := dockerCompanionContainerName(sandboxID, "db")
	primaryContainerName := dockerPrimaryContainerName(sandboxID)

	var mu sync.Mutex
	restartPolicies := make(map[string]container.RestartPolicy)
	backend := newDockerRuntimeBackendForTest(t, func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1.44")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
			writeDockerJSON(t, w, map[string]string{"Id": "sha256:test"})
		case r.Method == http.MethodPost && path == "/networks/create":
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
				HostConfig struct {
					RestartPolicy container.RestartPolicy `json:"RestartPolicy"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			restartPolicies[name] = request.HostConfig.RestartPolicy
			mu.Unlock()
			writeDockerJSON(t, w, map[string]string{"Id": name})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/containers/"+companionContainerName+"/json":
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

	if _, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{
			Image: "ghcr.io/agents-sandbox/coding-runtime:test",
		},
		companionContainers: []*agboxv1.CompanionContainerSpec{
			{
				Name:  "db",
				Image: "postgres:16",
				Healthcheck: &agboxv1.HealthcheckConfig{
					Test: []string{"CMD", "true"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	want := container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	for _, name := range []string{primaryContainerName, companionContainerName} {
		mu.Lock()
		got, ok := restartPolicies[name]
		mu.Unlock()
		if !ok {
			t.Fatalf("container %s: no RestartPolicy captured", name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("container %s: unexpected RestartPolicy: got=%+v want=%+v", name, got, want)
		}
	}
}
