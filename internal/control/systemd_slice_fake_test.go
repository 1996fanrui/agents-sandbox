package control

import (
	"context"
	"fmt"
	"sync"

	"github.com/coreos/go-systemd/v22/unit"
)

type fakeSliceLimits struct {
	cpu int64
	mem int64
}

type fakeSliceManager struct {
	mu      sync.Mutex
	ensured map[string]fakeSliceLimits
	removed map[string]int
	listed  []string
	calls   []string
	errs    map[string]error
}

func newFakeSliceManager() *fakeSliceManager {
	return &fakeSliceManager{
		ensured: map[string]fakeSliceLimits{},
		removed: map[string]int{},
		errs:    map[string]error{},
	}
}

func (f *fakeSliceManager) EnsureSandboxSlice(_ context.Context, sandboxID string, cpu, mem int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("Ensure %s", sandboxID))
	if err, ok := f.errs["Ensure "+sandboxID]; ok {
		return err
	}
	f.ensured[sandboxID] = fakeSliceLimits{cpu: cpu, mem: mem}
	return nil
}

func (f *fakeSliceManager) RemoveSandboxSlice(_ context.Context, sandboxID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("Remove %s", sandboxID))
	if err, ok := f.errs["Remove "+sandboxID]; ok {
		return err
	}
	f.removed[sandboxID]++
	delete(f.ensured, sandboxID)
	return nil
}

func (f *fakeSliceManager) SliceNameFor(sandboxID string) string {
	return "agbox-" + unit.UnitNameEscape(sandboxID) + ".slice"
}

func (f *fakeSliceManager) ListSandboxSlices(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "List")
	if err, ok := f.errs["List"]; ok {
		return nil, err
	}
	out := make([]string, len(f.listed))
	copy(out, f.listed)
	return out, nil
}

func (f *fakeSliceManager) ensureCount(sandboxID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	prefix := "Ensure " + sandboxID
	for _, c := range f.calls {
		if c == prefix {
			n++
		}
	}
	return n
}

func (f *fakeSliceManager) removeCount(sandboxID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed[sandboxID]
}

func (f *fakeSliceManager) lastLimits(sandboxID string) (fakeSliceLimits, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.ensured[sandboxID]
	return v, ok
}

var _ sliceManager = (*fakeSliceManager)(nil)
