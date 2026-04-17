package control

import (
	"context"
	"log/slog"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// AT-RC88: reconcileSandboxSlices must rebuild slices for active sandboxes
// with cpu/memory limits and garbage-collect slices whose sandbox id is no
// longer tracked.
func TestReconcileSandboxSlices(t *testing.T) {
	t.Run("rebuild_active", func(t *testing.T) {
		fake := newFakeSliceManager()
		service := &Service{
			config: ServiceConfig{
				Logger:       slog.Default(),
				sliceManager: fake,
			},
			boxes: map[string]*sandboxRecord{
				"alive-1": {
					handle: &agboxv1.SandboxHandle{
						SandboxId: "alive-1",
						State:     agboxv1.SandboxState_SANDBOX_STATE_READY,
					},
					createSpec: &agboxv1.CreateSpec{Image: "img:test", CpuLimit: "1"},
				},
			},
		}
		if err := service.reconcileSandboxSlices(context.Background()); err != nil {
			t.Fatalf("reconcileSandboxSlices failed: %v", err)
		}
		limits, ok := fake.lastLimits("alive-1")
		if !ok {
			t.Fatal("expected EnsureSandboxSlice for alive-1")
		}
		if limits.cpu != 1000 {
			t.Fatalf("expected cpu millicores=1000, got %d", limits.cpu)
		}
	})

	t.Run("orphan_cleanup_after_rebuild", func(t *testing.T) {
		fake := newFakeSliceManager()
		fake.listed = []string{fake.SliceNameFor("orphan-xyz"), fake.SliceNameFor("alive-1")}
		service := &Service{
			config: ServiceConfig{
				Logger:       slog.Default(),
				sliceManager: fake,
			},
			boxes: map[string]*sandboxRecord{
				"alive-1": {
					handle: &agboxv1.SandboxHandle{
						SandboxId: "alive-1",
						State:     agboxv1.SandboxState_SANDBOX_STATE_READY,
					},
					createSpec: &agboxv1.CreateSpec{Image: "img:test", CpuLimit: "2"},
				},
			},
		}
		if err := service.reconcileSandboxSlices(context.Background()); err != nil {
			t.Fatalf("reconcileSandboxSlices failed: %v", err)
		}
		if fake.removeCount("orphan-xyz") != 1 {
			t.Fatalf("expected orphan-xyz to be removed, got removeCount=%d", fake.removeCount("orphan-xyz"))
		}
		if fake.removeCount("alive-1") != 0 {
			t.Fatalf("alive sandbox must not be removed, got removeCount=%d", fake.removeCount("alive-1"))
		}
		// Ensure orders: Ensure must run before the orphan remove.
		ensureIdx, removeIdx := -1, -1
		for i, c := range fake.calls {
			if ensureIdx < 0 && c == "Ensure alive-1" {
				ensureIdx = i
			}
			if removeIdx < 0 && c == "Remove orphan-xyz" {
				removeIdx = i
			}
		}
		if ensureIdx < 0 || removeIdx < 0 {
			t.Fatalf("missing calls: %v", fake.calls)
		}
		if ensureIdx > removeIdx {
			t.Fatalf("Ensure must precede orphan Remove: calls=%v", fake.calls)
		}
	})
}
