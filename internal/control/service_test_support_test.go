package control

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type scriptedEventStore struct {
	appendFn              func(string, *agboxv1.SandboxEvent) error
	loadEventsFn          func(string) ([]*agboxv1.SandboxEvent, error)
	loadAllSandboxIDsFn   func() ([]string, error)
	maxSequenceFn         func(string) (uint64, error)
	deletedAtFn           func(string) (time.Time, bool, error)
	markDeletedFn         func(string, time.Time) error
	cleanupFn             func(time.Duration) ([]string, error)
	saveSandboxConfigFn   func(string, *agboxv1.CreateSpec) error
	loadSandboxConfigFn   func(string) (*agboxv1.CreateSpec, error)
	loadAllSandboxConfigsFn func() (map[string]*agboxv1.CreateSpec, error)
	deleteSandboxConfigFn func(string) error
	saveExecConfigFn      func(string, *agboxv1.CreateExecRequest) error
	loadExecConfigsFn     func(string) ([]*agboxv1.CreateExecRequest, error)
}

type persistentBufconnHarness struct {
	service *Service
	client  agboxv1.SandboxServiceClient
	close   func()
}

func (store scriptedEventStore) Append(sandboxID string, event *agboxv1.SandboxEvent) error {
	if store.appendFn != nil {
		return store.appendFn(sandboxID, event)
	}
	return nil
}

func (store scriptedEventStore) LoadEvents(sandboxID string) ([]*agboxv1.SandboxEvent, error) {
	if store.loadEventsFn != nil {
		return store.loadEventsFn(sandboxID)
	}
	return nil, nil
}

func (store scriptedEventStore) LoadAllSandboxIDs() ([]string, error) {
	if store.loadAllSandboxIDsFn != nil {
		return store.loadAllSandboxIDsFn()
	}
	return nil, nil
}

func (store scriptedEventStore) MaxSequence(sandboxID string) (uint64, error) {
	if store.maxSequenceFn != nil {
		return store.maxSequenceFn(sandboxID)
	}
	return 0, nil
}

func (store scriptedEventStore) DeletedAt(sandboxID string) (time.Time, bool, error) {
	if store.deletedAtFn != nil {
		return store.deletedAtFn(sandboxID)
	}
	return time.Time{}, false, nil
}

func (store scriptedEventStore) MarkDeleted(sandboxID string, deletedAt time.Time) error {
	if store.markDeletedFn != nil {
		return store.markDeletedFn(sandboxID, deletedAt)
	}
	return nil
}

func (store scriptedEventStore) Cleanup(retentionTTL time.Duration) ([]string, error) {
	if store.cleanupFn != nil {
		return store.cleanupFn(retentionTTL)
	}
	return nil, nil
}

func (store scriptedEventStore) SaveSandboxConfig(sandboxID string, spec *agboxv1.CreateSpec) error {
	if store.saveSandboxConfigFn != nil {
		return store.saveSandboxConfigFn(sandboxID, spec)
	}
	return nil
}

func (store scriptedEventStore) LoadSandboxConfig(sandboxID string) (*agboxv1.CreateSpec, error) {
	if store.loadSandboxConfigFn != nil {
		return store.loadSandboxConfigFn(sandboxID)
	}
	return nil, nil
}

func (store scriptedEventStore) LoadAllSandboxConfigs() (map[string]*agboxv1.CreateSpec, error) {
	if store.loadAllSandboxConfigsFn != nil {
		return store.loadAllSandboxConfigsFn()
	}
	return nil, nil
}

func (store scriptedEventStore) DeleteSandboxConfig(sandboxID string) error {
	if store.deleteSandboxConfigFn != nil {
		return store.deleteSandboxConfigFn(sandboxID)
	}
	return nil
}

func (store scriptedEventStore) SaveExecConfig(sandboxID string, req *agboxv1.CreateExecRequest) error {
	if store.saveExecConfigFn != nil {
		return store.saveExecConfigFn(sandboxID, req)
	}
	return nil
}

func (store scriptedEventStore) LoadExecConfigs(sandboxID string) ([]*agboxv1.CreateExecRequest, error) {
	if store.loadExecConfigsFn != nil {
		return store.loadExecConfigsFn(sandboxID)
	}
	return nil, nil
}

func newBufconnClient(t *testing.T, config ServiceConfig) agboxv1.SandboxServiceClient {
	t.Helper()
	if config.runtimeBackend == nil {
		config.runtimeBackend = fakeRuntimeBackend{}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	service, closer, err := NewService(config)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if closer != nil {
		t.Cleanup(func() {
			if closeErr := closer.Close(); closeErr != nil {
				t.Fatalf("service closer failed: %v", closeErr)
			}
		})
	}
	agboxv1.RegisterSandboxServiceServer(server, service)
	ctx, cancel := context.WithCancel(context.Background())
	go service.cleanupLoop(ctx)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		cancel()
		server.Stop()
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agboxv1.NewSandboxServiceClient(conn)
}

func newPersistentBufconnHarness(t *testing.T, ctx context.Context, config ServiceConfig, dbPath string) persistentBufconnHarness {
	t.Helper()
	if config.runtimeBackend == nil {
		config.runtimeBackend = fakeRuntimeBackend{}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	service, closer, err := NewServiceWithPersistentIDStore(ctx, config, dbPath)
	if err != nil {
		t.Fatalf("NewServiceWithPersistentIDStore failed: %v", err)
	}
	agboxv1.RegisterSandboxServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient failed: %v", err)
	}

	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			_ = conn.Close()
			server.Stop()
			if closer != nil {
				if closeErr := closer.Close(); closeErr != nil {
					t.Fatalf("service closer failed: %v", closeErr)
				}
			}
		})
	}
	t.Cleanup(closeFn)

	return persistentBufconnHarness{
		service: service,
		client:  agboxv1.NewSandboxServiceClient(conn),
		close:   closeFn,
	}
}

func collectEventsUntil(t *testing.T, stream agboxv1.SandboxService_SubscribeSandboxEventsClient, done func([]*agboxv1.SandboxEvent) bool) []*agboxv1.SandboxEvent {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	var events []*agboxv1.SandboxEvent
	for time.Now().Before(deadline) {
		event, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Recv failed: %v", err)
		}
		events = append(events, event)
		if done(events) {
			return events
		}
	}
	t.Fatalf("timed out waiting for events: %#v", events)
	return nil
}

func assertStatusCode(t *testing.T, err error, want codes.Code) {
	t.Helper()

	got := status.Code(err)
	if got != want {
		t.Fatalf("unexpected gRPC code: got %s want %s", got, want)
	}
}

func assertStatusErrorReason(t *testing.T, err error, wantCode codes.Code, wantReason string) {
	t.Helper()

	assertStatusCode(t, err, wantCode)
	st := status.Convert(err)
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetReason() == wantReason {
			return
		}
	}
	t.Fatalf("expected reason %q in error details, got %#v", wantReason, st.Details())
}

type recordingCloser struct {
	name  string
	order *[]string
	err   error
}

func (closer recordingCloser) Close() error {
	*closer.order = append(*closer.order, closer.name)
	return closer.err
}

func waitForSandboxState(t *testing.T, client agboxv1.SandboxServiceClient, sandboxID string, expected agboxv1.SandboxState) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
		if err != nil {
			t.Fatalf("GetSandbox failed: %v", err)
		}
		if resp.GetSandbox().GetState() == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("sandbox %s did not reach state %s", sandboxID, expected)
}

func waitForExecState(t *testing.T, client agboxv1.SandboxServiceClient, execID string, expected agboxv1.ExecState) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execID})
		if err != nil {
			t.Fatalf("GetExec failed: %v", err)
		}
		if resp.GetExec().GetState() == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("exec %s did not reach state %s", execID, expected)
}

type capturingRuntimeBackend struct {
	lastCreateImage string
	lastCreateSpec  *agboxv1.CreateSpec
	execResult      runtimeExecResult
	inspectResults  map[string]ContainerInspectResult
	watchEventCh    chan ContainerEvent
	watchErrCh      chan error
}

func (backend *capturingRuntimeBackend) CreateSandbox(_ context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	backend.lastCreateImage = record.createSpec.GetImage()
	backend.lastCreateSpec = cloneCreateSpec(record.createSpec)
	return fakeRuntimeBackend{}.CreateSandbox(context.Background(), record)
}

func (backend *capturingRuntimeBackend) ResumeSandbox(context.Context, *sandboxRecord) (runtimeResumeResult, error) {
	return runtimeResumeResult{
		ServiceStatuses: []runtimeServiceStatus{
			{Name: "default", Required: true, Ready: true},
		},
	}, nil
}

func (*capturingRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error {
	return nil
}

func (*capturingRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error {
	return nil
}

func (backend *capturingRuntimeBackend) RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error) {
	if backend.execResult == (runtimeExecResult{}) {
		return runtimeExecResult{ExitCode: 0}, nil
	}
	return backend.execResult, nil
}

func (backend *capturingRuntimeBackend) InspectContainer(_ context.Context, containerName string) (ContainerInspectResult, error) {
	if backend.inspectResults != nil {
		if result, ok := backend.inspectResults[containerName]; ok {
			return result, nil
		}
	}
	return ContainerInspectResult{}, nil
}

func (backend *capturingRuntimeBackend) WatchContainerEvents(_ context.Context) (<-chan ContainerEvent, <-chan error) {
	if backend.watchEventCh != nil {
		return backend.watchEventCh, backend.watchErrCh
	}
	return make(chan ContainerEvent), make(chan error)
}

func createSandboxRequest(sandboxID string, image string) *agboxv1.CreateSandboxRequest {
	return &agboxv1.CreateSandboxRequest{
		SandboxId:  sandboxID,
		CreateSpec: createSpecWithImage(image),
	}
}

func createSpecWithImage(image string) *agboxv1.CreateSpec {
	return &agboxv1.CreateSpec{Image: image}
}

func sandboxIDs(handles []*agboxv1.SandboxHandle) []string {
	ids := make([]string, 0, len(handles))
	for _, handle := range handles {
		ids = append(ids, handle.GetSandboxId())
	}
	return ids
}

// eventExecID extracts the exec_id from a SandboxEvent's exec details.
func eventExecID(event *agboxv1.SandboxEvent) string {
	if exec, ok := event.GetDetails().(*agboxv1.SandboxEvent_Exec); ok && exec != nil {
		return exec.Exec.GetExecId()
	}
	return ""
}

// eventExitCode extracts the exit_code from a SandboxEvent's exec details.
func eventExitCode(event *agboxv1.SandboxEvent) int32 {
	if exec, ok := event.GetDetails().(*agboxv1.SandboxEvent_Exec); ok && exec != nil {
		return exec.Exec.GetExitCode()
	}
	return 0
}

// eventReason extracts the reason from a SandboxEvent's sandbox_phase details.
func eventReason(event *agboxv1.SandboxEvent) string {
	if phase, ok := event.GetDetails().(*agboxv1.SandboxEvent_SandboxPhase); ok && phase != nil {
		return phase.SandboxPhase.GetReason()
	}
	return ""
}

// eventErrorCode extracts the error_code from a SandboxEvent's details (exec or phase).
func eventErrorCode(event *agboxv1.SandboxEvent) string {
	if exec, ok := event.GetDetails().(*agboxv1.SandboxEvent_Exec); ok && exec != nil {
		return exec.Exec.GetErrorCode()
	}
	if phase, ok := event.GetDetails().(*agboxv1.SandboxEvent_SandboxPhase); ok && phase != nil {
		return phase.SandboxPhase.GetErrorCode()
	}
	return ""
}

// eventServiceName extracts the service_name from a SandboxEvent's service details.
func eventServiceName(event *agboxv1.SandboxEvent) string {
	if svc, ok := event.GetDetails().(*agboxv1.SandboxEvent_Service); ok && svc != nil {
		return svc.Service.GetServiceName()
	}
	return ""
}

func assertErrorReason(t *testing.T, err error, want string) {
	t.Helper()

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetReason() == want {
			return
		}
	}
	t.Fatalf("expected reason %q in error details, got %v", want, st.Details())
}
