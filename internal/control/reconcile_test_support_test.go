package control

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// sandboxRecordForTest constructs a minimal sandboxRecord with initialized crashloopState
// for use in watcher unit tests.
func sandboxRecordForTest(sandboxID string, companionNames ...string) *sandboxRecord {
	companions := make([]runtimeCompanionContainer, 0, len(companionNames))
	for _, name := range companionNames {
		companions = append(companions, runtimeCompanionContainer{
			Name:           name,
			ContainerName:  "fake-companion-" + sandboxID + "-" + name,
			CrashloopState: &crashloopState{},
		})
	}
	return &sandboxRecord{
		handle: &agboxv1.SandboxHandle{
			SandboxId: sandboxID,
			State:     agboxv1.SandboxState_SANDBOX_STATE_READY,
			CreatedAt: timestamppb.Now(),
		},
		createSpec:          &agboxv1.CreateSpec{Image: "test:latest"},
		companionContainers: nil,
		execs:               make(map[string]*agboxv1.ExecStatus),
		execCancel:          make(map[string]context.CancelFunc),
		runtimeState: &sandboxRuntimeState{
			NetworkName:           "fake-network-" + sandboxID,
			PrimaryContainerName:  "fake-primary-" + sandboxID,
			CompanionContainers:   companions,
			PrimaryCrashloopState: &crashloopState{},
		},
	}
}

// newWatcherForTest creates a dockerEventWatcher backed by a service with a single
// pre-loaded READY sandbox. The fake backend's inspectResults and nowFunc are injected.
func newWatcherForTest(t *testing.T, record *sandboxRecord, backend *fakeRuntimeBackend, nowFunc func() time.Time) *dockerEventWatcher {
	t.Helper()
	logger := slog.Default()
	cfg := ServiceConfig{
		Logger:         logger,
		runtimeBackend: backend,
		eventStore:     newMemoryEventStore(),
		idRegistry:     newMemoryIDRegistry(),
		NowFunc:        nowFunc,
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = time.Now
	}
	svc := &Service{
		config: cfg,
		boxes:  make(map[string]*sandboxRecord),
		execs:  make(map[string]string),
	}
	svc.boxes[record.handle.GetSandboxId()] = record
	return newDockerEventWatcher(svc, logger)
}

// fixedClock returns a nowFunc that always returns the given absolute time.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// collectSandboxEvents returns all events of the given type recorded for the sandbox.
func collectSandboxEvents(record *sandboxRecord, eventType agboxv1.EventType) []*agboxv1.SandboxEvent {
	var result []*agboxv1.SandboxEvent
	for _, e := range record.events {
		if e.GetEventType() == eventType {
			result = append(result, e)
		}
	}
	return result
}

// hasEvent returns true if the record has at least one event of the given type.
func hasEvent(record *sandboxRecord, eventType agboxv1.EventType) bool {
	return len(collectSandboxEvents(record, eventType)) > 0
}

// eventCompanionErrorCode extracts the error_code from a COMPANION_CONTAINER_FAILED event.
func eventCompanionErrorCode(event *agboxv1.SandboxEvent) string {
	if cc, ok := event.GetDetails().(*agboxv1.SandboxEvent_CompanionContainer); ok && cc != nil {
		return cc.CompanionContainer.GetErrorCode()
	}
	return ""
}

// errStopFailed is a sentinel error used in StopSandbox failure tests.
var errStopFailed = &testError{"stop sandbox failed for test"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// capturingLogHandler is a slog.Handler that captures Warn-level messages.
type capturingLogHandler struct {
	warnFn func(string)
}

func (h *capturingLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return true
}

func (h *capturingLogHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn && h.warnFn != nil {
		h.warnFn(r.Message)
	}
	return nil
}

func (h *capturingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingLogHandler) WithGroup(_ string) slog.Handler      { return h }

// countingInspectBackend wraps a fakeRuntimeBackend and counts InspectContainer calls per container.
type countingInspectBackend struct {
	mu    sync.Mutex
	calls map[string]int
	inner *fakeRuntimeBackend
}

func (b *countingInspectBackend) callCount(containerName string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[containerName]
}

func (b *countingInspectBackend) InspectContainer(ctx context.Context, containerName string) (ContainerInspectResult, error) {
	b.mu.Lock()
	if b.calls == nil {
		b.calls = make(map[string]int)
	}
	b.calls[containerName]++
	b.mu.Unlock()
	return b.inner.InspectContainer(ctx, containerName)
}

func (b *countingInspectBackend) CreateSandbox(ctx context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	return b.inner.CreateSandbox(ctx, record)
}
func (b *countingInspectBackend) ResumeSandbox(ctx context.Context, record *sandboxRecord) (runtimeResumeResult, error) {
	return b.inner.ResumeSandbox(ctx, record)
}
func (b *countingInspectBackend) StopSandbox(ctx context.Context, record *sandboxRecord) error {
	return b.inner.StopSandbox(ctx, record)
}
func (b *countingInspectBackend) DeleteSandbox(ctx context.Context, record *sandboxRecord) error {
	return b.inner.DeleteSandbox(ctx, record)
}
func (b *countingInspectBackend) RunExec(ctx context.Context, record *sandboxRecord, exec *agboxv1.ExecStatus) (runtimeExecResult, error) {
	return b.inner.RunExec(ctx, record, exec)
}
func (b *countingInspectBackend) WatchContainerEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	return b.inner.WatchContainerEvents(ctx)
}
func (b *countingInspectBackend) ReapplyNetworkIsolation(ctx context.Context, record *sandboxRecord) error {
	return b.inner.ReapplyNetworkIsolation(ctx, record)
}
