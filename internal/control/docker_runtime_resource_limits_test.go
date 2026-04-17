package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"google.golang.org/grpc/codes"
)

// capturedCreate records the HostConfig fragments we assert on.
type capturedCreate struct {
	CgroupParent string
	StorageOpt   map[string]string
}

// dockerResourceLimitHandler returns an http handler that responds to the
// minimum set of Docker endpoints needed to reach ContainerCreate on the
// primary container and records every create body.
func dockerResourceLimitHandler(t *testing.T, sandboxID string, captures map[string]capturedCreate, mu *sync.Mutex, createFail func(w http.ResponseWriter) bool) http.HandlerFunc {
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
				HostConfig struct {
					CgroupParent string            `json:"CgroupParent"`
					StorageOpt   map[string]string `json:"StorageOpt"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			captures[name] = capturedCreate{
				CgroupParent: request.HostConfig.CgroupParent,
				StorageOpt:   request.HostConfig.StorageOpt,
			}
			mu.Unlock()
			if createFail != nil && createFail(w) {
				return
			}
			writeDockerJSON(t, w, map[string]string{"Id": name})
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/networks/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && path == "/containers/"+primaryContainerName+"/json":
			writeDockerJSON(t, w, container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{
					State: &container.State{Running: true, Status: "running"},
				},
			})
		default:
			t.Fatalf("unexpected Docker API request: %s %s", r.Method, r.URL.Path)
		}
	}
}

// AT-XWYD: cpu/memory/disk limits must drive HostConfig.CgroupParent +
// StorageOpt["size"] on primary container create.
func TestDockerCreateResourceLimitsInjected(t *testing.T) {
	sandboxID := "res-inject"
	captures := make(map[string]capturedCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))
	fake := newFakeSliceManager()
	backend.slice = fake

	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{
			Image:       "img:test",
			CpuLimit:    "2",
			MemoryLimit: "4g",
			DiskLimit:   "10g",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	got := captures[dockerPrimaryContainerName(sandboxID)]
	wantCgroup := fake.SliceNameFor(sandboxID)
	if got.CgroupParent != wantCgroup {
		t.Fatalf("CgroupParent: got=%q want=%q", got.CgroupParent, wantCgroup)
	}
	if got.StorageOpt["size"] == "" {
		t.Fatalf("StorageOpt[size] missing: %+v", got.StorageOpt)
	}
	// 10g = 10 * 1024^3 = 10737418240 bytes.
	if got.StorageOpt["size"] != "10737418240" {
		t.Fatalf("StorageOpt[size] got=%q want=10737418240", got.StorageOpt["size"])
	}
	if fake.ensureCount(sandboxID) != 1 {
		t.Fatalf("EnsureSandboxSlice call count: got=%d want=1", fake.ensureCount(sandboxID))
	}
}

// AT-AVS4: with no limits, hostConfig must not advertise CgroupParent /
// StorageOpt; disk-only sandboxes still get StorageOpt but no slice.
func TestDockerCreateNoResourceLimits(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		sandboxID := "res-empty"
		captures := make(map[string]capturedCreate)
		var mu sync.Mutex
		backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))
		fake := newFakeSliceManager()
		backend.slice = fake
		_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
			handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
			createSpec: &agboxv1.CreateSpec{Image: "img:test"},
		})
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
		got := captures[dockerPrimaryContainerName(sandboxID)]
		if got.CgroupParent != "" {
			t.Fatalf("CgroupParent should be empty, got %q", got.CgroupParent)
		}
		if len(got.StorageOpt) != 0 {
			t.Fatalf("StorageOpt should be empty, got %+v", got.StorageOpt)
		}
		if fake.ensureCount(sandboxID) != 0 {
			t.Fatalf("EnsureSandboxSlice must not be called when cpu/mem are unset")
		}
	})
	t.Run("disk_only", func(t *testing.T) {
		sandboxID := "res-disk-only"
		captures := make(map[string]capturedCreate)
		var mu sync.Mutex
		backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))
		fake := newFakeSliceManager()
		backend.slice = fake
		_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
			handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
			createSpec: &agboxv1.CreateSpec{Image: "img:test", DiskLimit: "5g"},
		})
		if err != nil {
			t.Fatalf("CreateSandbox failed: %v", err)
		}
		got := captures[dockerPrimaryContainerName(sandboxID)]
		if got.CgroupParent != "" {
			t.Fatalf("disk-only sandbox must not set CgroupParent, got %q", got.CgroupParent)
		}
		if got.StorageOpt["size"] != "5368709120" {
			t.Fatalf("StorageOpt[size] got=%q want=5368709120", got.StorageOpt["size"])
		}
		if fake.ensureCount(sandboxID) != 0 {
			t.Fatalf("EnsureSandboxSlice must not be called for disk-only sandbox")
		}
	})
}

// AT-BEVF: Docker storage-opt prerequisite errors must be translated to
// FailedPrecondition with diagnostic hints.
func TestDiskLimitErrorTranslation(t *testing.T) {
	sandboxID := "res-disk-err"
	captures := make(map[string]capturedCreate)
	var mu sync.Mutex
	failOnce := false
	fail := func(w http.ResponseWriter) bool {
		if failOnce {
			return false
		}
		failOnce = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"--storage-opt is supported only for overlay over xfs with 'pquota' mount option"}`))
		return true
	}
	backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, fail))
	fake := newFakeSliceManager()
	backend.slice = fake
	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{Image: "img:test", DiskLimit: "10g"},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := statusCode(err); got != codes.FailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %s: %v", got, err)
	}
	if !strings.Contains(err.Error(), "Prerequisites") {
		t.Fatalf("error should mention 'Prerequisites', got %v", err)
	}
	// Failed create path must have triggered slice cleanup attempt; since no
	// slice was created (disk-only), ensure nothing leaked.
	if fake.ensureCount(sandboxID) != 0 {
		t.Fatal("disk-only sandbox should not create a slice")
	}
}
