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
	NanoCPUs   int64
	Memory     int64
	MemorySwap int64
	StorageOpt map[string]string
	// ExtraResourceKeys records any HostConfig.* key the daemon set beyond the
	// documented per-container contract. Non-empty means a regression.
	ExtraResourceKeys []string
}

// dockerResourceLimitHandler responds to the minimum Docker endpoints needed
// to reach ContainerCreate for primary + companion containers and captures
// the HostConfig fragments relevant to the per-container resource-limit
// contract.
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
			// Decode the HostConfig as a free-form map so any legacy
			// sandbox-level cgroup keys (e.g. a parent-cgroup override) would
			// surface as extras instead of being silently accepted.
			var request struct {
				HostConfig map[string]any `json:"HostConfig"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode container create request failed: %v", err)
			}
			hc := request.HostConfig
			// Any sandbox-scoped cgroup key leaking into HostConfig is a
			// regression.
			disallowed := []string{"CgroupParent"}
			var extras []string
			for _, key := range disallowed {
				if v, ok := hc[key]; ok {
					if s, isStr := v.(string); isStr && s == "" {
						continue
					}
					extras = append(extras, key)
				}
			}
			nanoCPUs := int64(0)
			if v, ok := hc["NanoCpus"]; ok {
				if f, isF := v.(float64); isF {
					nanoCPUs = int64(f)
				}
			}
			memory := int64(0)
			if v, ok := hc["Memory"]; ok {
				if f, isF := v.(float64); isF {
					memory = int64(f)
				}
			}
			memSwap := int64(0)
			if v, ok := hc["MemorySwap"]; ok {
				if f, isF := v.(float64); isF {
					memSwap = int64(f)
				}
			}
			var storageOpt map[string]string
			if raw, ok := hc["StorageOpt"]; ok {
				if m, isM := raw.(map[string]any); isM {
					storageOpt = make(map[string]string, len(m))
					for k, v := range m {
						if s, isS := v.(string); isS {
							storageOpt[k] = s
						}
					}
				}
			}
			name := r.URL.Query().Get("name")
			mu.Lock()
			captures[name] = capturedCreate{
				NanoCPUs:          nanoCPUs,
				Memory:            memory,
				MemorySwap:        memSwap,
				StorageOpt:        storageOpt,
				ExtraResourceKeys: extras,
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

// AT-R8VN: all three per-container limits must land as HostConfig.NanoCPUs /
// HostConfig.Memory / HostConfig.StorageOpt["size"] without writing any
// sandbox-scoped cgroup parent key. primary and companion symmetric.
func TestDockerCreateResourceLimitsInjected(t *testing.T) {
	sandboxID := "res-inject"
	captures := make(map[string]capturedCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))

	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle: &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{
			Image:       "img:test",
			CpuLimit:    "2",
			MemoryLimit: "4g",
			DiskLimit:   "10g",
			CompanionContainers: []*agboxv1.CompanionContainerSpec{
				{
					Name:        "db",
					Image:       "postgres:16",
					CpuLimit:    "1",
					MemoryLimit: "512m",
					DiskLimit:   "5g",
				},
			},
		},
		companionContainers: []*agboxv1.CompanionContainerSpec{
			{
				Name:        "db",
				Image:       "postgres:16",
				CpuLimit:    "1",
				MemoryLimit: "512m",
				DiskLimit:   "5g",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	primary := captures[dockerPrimaryContainerName(sandboxID)]
	if len(primary.ExtraResourceKeys) != 0 {
		t.Fatalf("primary HostConfig must not set sandbox-scoped cgroup keys, got %v", primary.ExtraResourceKeys)
	}
	// 2 cores = 2000 millicores = 2_000_000_000 NanoCPUs.
	if primary.NanoCPUs != 2_000_000_000 {
		t.Fatalf("primary NanoCPUs: got=%d want=2000000000", primary.NanoCPUs)
	}
	// 4g = 4 * 1024^3 = 4294967296 bytes.
	if primary.Memory != 4294967296 {
		t.Fatalf("primary Memory: got=%d want=4294967296", primary.Memory)
	}
	// MemorySwap must stay at Docker's default (zero => 2 * Memory).
	if primary.MemorySwap != 0 {
		t.Fatalf("primary MemorySwap must stay unset, got=%d", primary.MemorySwap)
	}
	// 10g = 10 * 1024^3 = 10737418240 bytes.
	if primary.StorageOpt["size"] != "10737418240" {
		t.Fatalf("primary StorageOpt[size]: got=%q want=10737418240", primary.StorageOpt["size"])
	}

	companion := captures[dockerCompanionContainerName(sandboxID, "db")]
	if len(companion.ExtraResourceKeys) != 0 {
		t.Fatalf("companion HostConfig must not set sandbox-scoped cgroup keys, got %v", companion.ExtraResourceKeys)
	}
	if companion.NanoCPUs != 1_000_000_000 {
		t.Fatalf("companion NanoCPUs: got=%d want=1000000000", companion.NanoCPUs)
	}
	if companion.Memory != 536870912 {
		t.Fatalf("companion Memory: got=%d want=536870912", companion.Memory)
	}
	if companion.StorageOpt["size"] != "5368709120" {
		t.Fatalf("companion StorageOpt[size]: got=%q want=5368709120", companion.StorageOpt["size"])
	}
}

// AT-R8VN (disk-only): with only disk_limit set on primary, StorageOpt["size"]
// is populated while NanoCPUs and Memory stay zero.
func TestDockerCreateDiskOnlyPrimary(t *testing.T) {
	sandboxID := "res-disk-only"
	captures := make(map[string]capturedCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))
	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{Image: "img:test", DiskLimit: "10g"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	got := captures[dockerPrimaryContainerName(sandboxID)]
	if len(got.ExtraResourceKeys) != 0 {
		t.Fatalf("HostConfig must not set sandbox-scoped cgroup keys, got %v", got.ExtraResourceKeys)
	}
	if got.NanoCPUs != 0 {
		t.Fatalf("NanoCPUs should be 0, got %d", got.NanoCPUs)
	}
	if got.Memory != 0 {
		t.Fatalf("Memory should be 0, got %d", got.Memory)
	}
	if got.StorageOpt["size"] != "10737418240" {
		t.Fatalf("StorageOpt[size]: got=%q want=10737418240", got.StorageOpt["size"])
	}
}

// AT-R8VN (negative): with no limits, every HostConfig resource field must be
// zero-valued and no sandbox-scoped cgroup key may leak in.
func TestDockerCreateNoResourceLimits(t *testing.T) {
	sandboxID := "res-empty"
	captures := make(map[string]capturedCreate)
	var mu sync.Mutex
	backend := newDockerRuntimeBackendForTest(t, dockerResourceLimitHandler(t, sandboxID, captures, &mu, nil))
	_, err := backend.CreateSandbox(context.Background(), &sandboxRecord{
		handle:     &agboxv1.SandboxHandle{SandboxId: sandboxID},
		createSpec: &agboxv1.CreateSpec{Image: "img:test"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	got := captures[dockerPrimaryContainerName(sandboxID)]
	if len(got.ExtraResourceKeys) != 0 {
		t.Fatalf("no-limits HostConfig must not set sandbox-scoped cgroup keys, got %v", got.ExtraResourceKeys)
	}
	if got.NanoCPUs != 0 {
		t.Fatalf("NanoCPUs should be 0, got %d", got.NanoCPUs)
	}
	if got.Memory != 0 {
		t.Fatalf("Memory should be 0, got %d", got.Memory)
	}
	if len(got.StorageOpt) != 0 {
		t.Fatalf("StorageOpt should be empty, got %+v", got.StorageOpt)
	}
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
}
