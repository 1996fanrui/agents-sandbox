package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sddbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/coreos/go-systemd/v22/unit"
	godbus "github.com/godbus/dbus/v5"
)

// parentSliceName is the single parent slice under which all per-sandbox
// transient slices live. It is created lazily on first EnsureSandboxSlice.
const parentSliceName = "agbox.slice"

// sliceManager abstracts systemd transient slice operations so that the
// Docker runtime backend can be tested without a live system bus.
type sliceManager interface {
	EnsureSandboxSlice(ctx context.Context, sandboxID string, cpuMillicores, memoryBytes int64) error
	RemoveSandboxSlice(ctx context.Context, sandboxID string) error
	SliceNameFor(sandboxID string) string
	ListSandboxSlices(ctx context.Context) ([]string, error)
}

// systemdSliceManager is the production sliceManager backed by a D-Bus
// connection to the system instance of systemd.
type systemdSliceManager struct {
	mu          sync.Mutex
	conn        *sddbus.Conn
	parentReady bool
}

// newSystemdSliceManager opens a connection to the system bus. The returned
// manager owns the connection and must be closed via Close on shutdown.
func newSystemdSliceManager(ctx context.Context) (*systemdSliceManager, error) {
	conn, err := sddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd system bus: %w", err)
	}
	return &systemdSliceManager{conn: conn}, nil
}

// Close releases the underlying D-Bus connection.
func (m *systemdSliceManager) Close() {
	if m == nil || m.conn == nil {
		return
	}
	m.conn.Close()
}

// SliceNameFor is a pure function of sandboxID and requires no state.
func (*systemdSliceManager) SliceNameFor(sandboxID string) string {
	return "agbox-" + unit.UnitNameEscape(sandboxID) + ".slice"
}

func (m *systemdSliceManager) EnsureSandboxSlice(ctx context.Context, sandboxID string, cpuMillicores, memoryBytes int64) error {
	// Callers must not pass --cgroup-parent when both limits are zero; keep
	// this as a defensive no-op so tests and reconcile paths stay safe.
	if cpuMillicores == 0 && memoryBytes == 0 {
		return nil
	}
	if err := m.ensureParentSlice(ctx); err != nil {
		return err
	}

	childName := m.SliceNameFor(sandboxID)
	props := make([]sddbus.Property, 0, 2)
	if memoryBytes > 0 {
		props = append(props, sddbus.Property{
			Name:  "MemoryMax",
			Value: godbus.MakeVariant(uint64(memoryBytes)),
		})
	}
	if cpuMillicores > 0 {
		// 1 millicore = 1000 μs/s of CPU time per wall-clock second.
		props = append(props, sddbus.Property{
			Name:  "CPUQuotaPerSecUSec",
			Value: godbus.MakeVariant(uint64(cpuMillicores) * 1000),
		})
	}

	if err := m.startTransientUnit(ctx, childName, props); err != nil {
		if !isAlreadyExistsErr(err) {
			return fmt.Errorf("start transient unit %s: %w", childName, err)
		}
		// Slice already exists (e.g. daemon restart reconcile); refresh
		// runtime properties in place.
		if err := m.conn.SetUnitPropertiesContext(ctx, childName, true, props...); err != nil {
			return fmt.Errorf("set properties on %s: %w", childName, err)
		}
	}
	return nil
}

func (m *systemdSliceManager) RemoveSandboxSlice(ctx context.Context, sandboxID string) error {
	childName := m.SliceNameFor(sandboxID)
	ch := make(chan string, 1)
	if _, err := m.conn.StopUnitContext(ctx, childName, "replace", ch); err != nil {
		if isNotLoadedErr(err) {
			return nil
		}
		return fmt.Errorf("stop unit %s: %w", childName, err)
	}
	return waitUnitJob(ctx, ch, "stop", childName)
}

func (m *systemdSliceManager) ListSandboxSlices(ctx context.Context) ([]string, error) {
	units, err := m.conn.ListUnitsByPatternsContext(ctx,
		[]string{"active", "loaded", "failed"},
		[]string{"agbox-*.slice"})
	if err != nil {
		return nil, fmt.Errorf("list agbox slices: %w", err)
	}
	out := make([]string, 0, len(units))
	for _, u := range units {
		if u.Name == parentSliceName {
			continue
		}
		out = append(out, u.Name)
	}
	return out, nil
}

func (m *systemdSliceManager) ensureParentSlice(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parentReady {
		return nil
	}
	if err := m.startTransientUnit(ctx, parentSliceName, nil); err != nil && !isAlreadyExistsErr(err) {
		return fmt.Errorf("start parent slice %s: %w", parentSliceName, err)
	}
	m.parentReady = true
	return nil
}

// startTransientUnit starts a transient unit and waits for the job to
// complete. A non-"done" job result (e.g. "failed", "timeout", "canceled")
// is surfaced as an error instead of being silently dropped.
func (m *systemdSliceManager) startTransientUnit(ctx context.Context, name string, props []sddbus.Property) error {
	ch := make(chan string, 1)
	if _, err := m.conn.StartTransientUnitContext(ctx, name, "replace", props, ch); err != nil {
		return err
	}
	return waitUnitJob(ctx, ch, "start", name)
}

// waitUnitJob blocks until systemd reports the job result for the unit,
// translating non-success results into errors.
func waitUnitJob(ctx context.Context, ch <-chan string, op, name string) error {
	select {
	case result := <-ch:
		if result != "done" {
			return fmt.Errorf("%s %s: systemd job result %q", op, name, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isAlreadyExistsErr matches the systemd D-Bus error returned when a unit
// with the same name is already loaded.
func isAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr godbus.Error
	if errors.As(err, &dbusErr) {
		if strings.Contains(dbusErr.Name, "AlreadyExists") || strings.Contains(dbusErr.Name, "UnitExists") {
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "AlreadyExists") || strings.Contains(msg, "already exists")
}

// isNotLoadedErr matches systemd errors for "no such unit" / "not loaded".
func isNotLoadedErr(err error) bool {
	if err == nil {
		return false
	}
	var dbusErr godbus.Error
	if errors.As(err, &dbusErr) {
		if strings.Contains(dbusErr.Name, "NoSuchUnit") || strings.Contains(dbusErr.Name, "LoadFailed") {
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "not loaded") || strings.Contains(msg, "NoSuchUnit")
}

// noopSliceManager is used on hosts where the systemd system bus is
// unavailable (macOS, CI sandboxes, non-systemd Linux). validateCreateSpec
// rejects cpu/memory limits on such hosts via hostCapabilities, so this impl
// only needs to satisfy the interface while keeping disk_limit working.
type noopSliceManager struct{}

func (noopSliceManager) EnsureSandboxSlice(context.Context, string, int64, int64) error {
	return nil
}

func (noopSliceManager) RemoveSandboxSlice(context.Context, string) error { return nil }

func (noopSliceManager) SliceNameFor(string) string { return "" }

func (noopSliceManager) ListSandboxSlices(context.Context) ([]string, error) { return nil, nil }

var (
	_ sliceManager = (*systemdSliceManager)(nil)
	_ sliceManager = noopSliceManager{}
)
