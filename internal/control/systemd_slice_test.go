package control

import (
	"context"
	"strings"
	"testing"

	"github.com/coreos/go-systemd/v22/unit"
)

// TestSliceManagerIdempotent covers AT-4R5O: repeated Ensure/Remove calls
// must not produce errors and must preserve the sliceManager contract.
func TestSliceManagerIdempotent(t *testing.T) {
	ctx := context.Background()

	t.Run("ensure twice with same params", func(t *testing.T) {
		f := newFakeSliceManager()
		if err := f.EnsureSandboxSlice(ctx, "sb1", 500, 536870912); err != nil {
			t.Fatalf("first ensure: %v", err)
		}
		if err := f.EnsureSandboxSlice(ctx, "sb1", 500, 536870912); err != nil {
			t.Fatalf("second ensure: %v", err)
		}
		if got := f.ensureCount("sb1"); got != 2 {
			t.Fatalf("expected 2 ensure calls, got %d", got)
		}
		lim, ok := f.lastLimits("sb1")
		if !ok || lim.cpu != 500 || lim.mem != 536870912 {
			t.Fatalf("unexpected limits: %+v ok=%v", lim, ok)
		}
	})

	t.Run("remove twice is idempotent", func(t *testing.T) {
		f := newFakeSliceManager()
		if err := f.EnsureSandboxSlice(ctx, "sb1", 500, 0); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		if err := f.RemoveSandboxSlice(ctx, "sb1"); err != nil {
			t.Fatalf("first remove: %v", err)
		}
		if err := f.RemoveSandboxSlice(ctx, "sb1"); err != nil {
			t.Fatalf("second remove returned error: %v", err)
		}
		if got := f.removeCount("sb1"); got != 2 {
			t.Fatalf("expected 2 remove calls, got %d", got)
		}
	})

	t.Run("remove never-existed is nil", func(t *testing.T) {
		f := newFakeSliceManager()
		if err := f.RemoveSandboxSlice(ctx, "ghost"); err != nil {
			t.Fatalf("remove of unknown slice: %v", err)
		}
	})

	t.Run("zero limits are a no-op on the real impl", func(t *testing.T) {
		// The real manager documents this contract; exercise it here with
		// a zero-value struct - no D-Bus connection is touched because the
		// early return fires before any conn usage.
		var m systemdSliceManager
		if err := m.EnsureSandboxSlice(ctx, "sb2", 0, 0); err != nil {
			t.Fatalf("zero-limit ensure should be no-op, got: %v", err)
		}
		// SliceNameFor remains deterministic even when zero-limit sandboxes
		// never hit the real slice path.
		name := m.SliceNameFor("sb2")
		if name != "agbox-sb2.slice" {
			t.Fatalf("unexpected slice name: %q", name)
		}
	})
}

// TestWaitUnitJobFailureSurfacesError guards against silent downgrade of
// non-"done" systemd job results (failed, timeout, canceled, skipped).
func TestWaitUnitJobFailureSurfacesError(t *testing.T) {
	for _, result := range []string{"failed", "timeout", "canceled", "skipped"} {
		ch := make(chan string, 1)
		ch <- result
		if err := waitUnitJob(context.Background(), ch, "start", "test.slice"); err == nil {
			t.Fatalf("result %q should have produced an error", result)
		}
	}

	ch := make(chan string, 1)
	ch <- "done"
	if err := waitUnitJob(context.Background(), ch, "start", "test.slice"); err != nil {
		t.Fatalf("result \"done\" unexpectedly errored: %v", err)
	}
}

// TestSliceNameEscaping verifies that SliceNameFor escapes sandbox ids
// containing "-" so that systemd does not misinterpret them as hierarchy
// separators. Pure function - no D-Bus connection required.
func TestSliceNameEscaping(t *testing.T) {
	var m systemdSliceManager
	got := m.SliceNameFor("abc-123")

	expectedEscape := unit.UnitNameEscape("abc-123")
	expected := "agbox-" + expectedEscape + ".slice"
	if got != expected {
		t.Fatalf("SliceNameFor(abc-123) = %q, want %q", got, expected)
	}
	// The raw "-" must not survive into the slice leaf segment (it would
	// be interpreted as a parent/child separator by systemd).
	leaf := strings.TrimPrefix(strings.TrimSuffix(got, ".slice"), "agbox-")
	if strings.Contains(leaf, "-") {
		t.Fatalf("escaped leaf still contains '-': %q", leaf)
	}
}
