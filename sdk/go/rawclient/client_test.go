package rawclient

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control"
)

func TestDefaultSocketPath(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	socketPath, err := DefaultSocketPath()
	if err != nil {
		t.Fatalf("DefaultSocketPath failed: %v", err)
	}

	if socketPath == "" {
		t.Fatal("DefaultSocketPath returned empty path")
	}
	if !strings.HasPrefix(filepath.ToSlash(filepath.Dir(socketPath)), filepath.ToSlash(filepath.Join(runtimeDir))+string('/')) {
		t.Fatalf("unexpected socket directory: %q (expected under runtime dir %q)", filepath.Dir(socketPath), runtimeDir)
	}
	if filepath.Ext(socketPath) != ".sock" {
		t.Fatalf("unexpected socket extension: %q", socketPath)
	}
}

func TestRawClientConnection(t *testing.T) {
	t.Parallel()

	service := &fakeService{
		pingResp: &agboxv1.PingResponse{
			Daemon:  "AgentsSandbox",
			Version: "0.1.0",
		},
	}
	client := newRawClient(t, service)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	resp, err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if resp.GetDaemon() != "AgentsSandbox" || resp.GetVersion() != "0.1.0" {
		t.Fatalf("unexpected ping response: %#v", resp)
	}
}

func TestRPCMethods(t *testing.T) {
	t.Parallel()

	service := &fakeService{
		pingResp: &agboxv1.PingResponse{},
		createSandboxResp: &agboxv1.CreateSandboxResponse{
			SandboxId: "sbx",
		},
		getSandboxResp: &agboxv1.GetSandboxResponse{
			Sandbox: &agboxv1.SandboxHandle{
				SandboxId: "sbx",
			},
		},
		listSandboxesResp: &agboxv1.ListSandboxesResponse{},
		deleteSandboxesResp: &agboxv1.DeleteSandboxesResponse{
			DeletedCount: 1,
		},
		createExecResp: &agboxv1.CreateExecResponse{
			ExecId: "exec-1",
		},
		getExecResp: &agboxv1.GetExecResponse{
			Exec: &agboxv1.ExecStatus{
				ExecId:            "exec-1",
				LastEventSequence: 1,
			},
		},
		listActiveExecsResp: &agboxv1.ListActiveExecsResponse{},
		resumeSandboxResp:   &agboxv1.AcceptedResponse{Accepted: true},
		stopSandboxResp:     &agboxv1.AcceptedResponse{Accepted: true},
		deleteSandboxResp:   &agboxv1.AcceptedResponse{Accepted: true},
		cancelExecResp:      &agboxv1.AcceptedResponse{Accepted: true},
		subscribeEventsPayload: []*agboxv1.SandboxEvent{
			{EventId: "event-1"},
			{EventId: "event-2"},
		},
	}
	client := newRawClient(t, service)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	createReq := &agboxv1.CreateSandboxRequest{
		SandboxId: "sandbox-alpha",
		CreateSpec: &agboxv1.CreateSpec{
			Image: "alpine",
		},
	}
	listReq := &agboxv1.ListSandboxesRequest{
		IncludeDeleted: true,
		LabelSelector: map[string]string{
			"team": "sre",
		},
	}
	deleteSandboxesReq := &agboxv1.DeleteSandboxesRequest{
		LabelSelector: map[string]string{
			"team": "sre",
		},
	}
	createExecReq := &agboxv1.CreateExecRequest{
		SandboxId: "sandbox-alpha",
		ExecId:    "exec-1",
		Command:   []string{"echo", "hello"},
		Cwd:       "/workspace",
	}

	if _, err := client.CreateSandbox(context.Background(), createReq); err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	if _, err := client.GetSandbox(context.Background(), "sandbox-alpha"); err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if _, err := client.ListSandboxes(context.Background(), listReq); err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if _, err := client.ResumeSandbox(context.Background(), "sandbox-alpha"); err != nil {
		t.Fatalf("ResumeSandbox failed: %v", err)
	}
	if _, err := client.StopSandbox(context.Background(), "sandbox-alpha"); err != nil {
		t.Fatalf("StopSandbox failed: %v", err)
	}
	if _, err := client.DeleteSandbox(context.Background(), "sandbox-alpha"); err != nil {
		t.Fatalf("DeleteSandbox failed: %v", err)
	}
	if _, err := client.DeleteSandboxes(context.Background(), deleteSandboxesReq); err != nil {
		t.Fatalf("DeleteSandboxes failed: %v", err)
	}
	stream, err := client.SubscribeSandboxEvents(context.Background(), "sandbox-alpha", 42, true)
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv first event failed: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv second event failed: %v", err)
	}
	if _, err := client.CreateExec(context.Background(), createExecReq); err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	if _, err := client.CancelExec(context.Background(), "exec-1"); err != nil {
		t.Fatalf("CancelExec failed: %v", err)
	}
	if _, err := client.GetExec(context.Background(), "exec-1"); err != nil {
		t.Fatalf("GetExec failed: %v", err)
	}
	if _, err := client.ListActiveExecs(context.Background(), "sandbox-alpha"); err != nil {
		t.Fatalf("ListActiveExecs failed: %v", err)
	}

	if !proto.Equal(service.createSandboxReq, createReq) {
		t.Fatalf("create request mismatch: %#v != %#v", service.createSandboxReq, createReq)
	}
	if service.getSandboxReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("get request mismatch: %q", service.getSandboxReq.GetSandboxId())
	}
	if !proto.Equal(service.listSandboxesReq, listReq) {
		t.Fatalf("list request mismatch: %#v != %#v", service.listSandboxesReq, listReq)
	}
	if !proto.Equal(service.deleteSandboxesReq, deleteSandboxesReq) {
		t.Fatalf("delete sandboxes request mismatch: %#v != %#v", service.deleteSandboxesReq, deleteSandboxesReq)
	}
	if service.subscribeSandboxEventsReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("subscribe request mismatch: %#v", service.subscribeSandboxEventsReq)
	}
	if service.subscribeSandboxEventsReq.GetFromSequence() != 42 {
		t.Fatalf("subscribe from_sequence mismatch: %d", service.subscribeSandboxEventsReq.GetFromSequence())
	}
	if !service.subscribeSandboxEventsReq.GetIncludeCurrentSnapshot() {
		t.Fatalf("subscribe include_current_snapshot mismatch: %v", service.subscribeSandboxEventsReq.GetIncludeCurrentSnapshot())
	}
	if !proto.Equal(service.createExecReq, createExecReq) {
		t.Fatalf("create exec request mismatch: %#v != %#v", service.createExecReq, createExecReq)
	}
	if service.cancelExecReq.GetExecId() != "exec-1" {
		t.Fatalf("cancel request mismatch: %q", service.cancelExecReq.GetExecId())
	}
	if service.getExecReq.GetExecId() != "exec-1" {
		t.Fatalf("get exec request mismatch: %q", service.getExecReq.GetExecId())
	}
	if service.listActiveExecsReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("list active execs request mismatch: %q", service.listActiveExecsReq.GetSandboxId())
	}
	if service.resumeSandboxReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("resume request mismatch: %q", service.resumeSandboxReq.GetSandboxId())
	}
	if service.stopSandboxReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("stop request mismatch: %q", service.stopSandboxReq.GetSandboxId())
	}
	if service.deleteSandboxReq.GetSandboxId() != "sandbox-alpha" {
		t.Fatalf("delete request mismatch: %q", service.deleteSandboxReq.GetSandboxId())
	}
}

func TestErrorTranslationKnownAndUnknownReason(t *testing.T) {
	t.Parallel()

	service := &fakeService{
		getSandboxErr:    newStatusError(t, control.ReasonSandboxNotFound, "sandbox sandbox-missing was not found"),
		cancelExecErr:    newStatusError(t, control.ReasonExecAlreadyTerminal, "exec exec-123 is already terminal"),
		deleteSandboxErr: newStatusError(t, "UNKNOWN_REASON_FOR_TEST", "not mapped here"),
	}
	client := newRawClient(t, service)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	_, err := client.GetSandbox(context.Background(), "sandbox-missing")
	if err == nil {
		t.Fatal("GetSandbox expected error")
	}
	var notFound *SandboxNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected SandboxNotFoundError, got %T", err)
	}
	if notFound.SandboxID != "" {
		t.Fatalf("expected provider-style message to keep sandbox id empty, got %q", notFound.SandboxID)
	}
	if notFound.Error() != "sandbox sandbox-missing was not found" {
		t.Fatalf("unexpected not-found message: %q", notFound.Error())
	}

	_, err = client.CancelExec(context.Background(), "exec-123")
	if err == nil {
		t.Fatal("CancelExec expected error")
	}
	var terminal *ExecAlreadyTerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("expected ExecAlreadyTerminalError, got %T", err)
	}
	if terminal.ExecID != "" {
		t.Fatalf("expected provider-style message to keep exec id empty, got %q", terminal.ExecID)
	}
	if terminal.Error() != "exec exec-123 is already terminal" {
		t.Fatalf("unexpected already-terminal message: %q", terminal.Error())
	}

	sequenceErr := translateRPCError(
		newStatusError(
			t,
			control.ReasonSandboxEventSequenceExpired,
			"sandbox sandbox-alpha event sequence 42 is outside retained history",
		),
	)
	var expired *SandboxSequenceExpiredError
	if !errors.As(sequenceErr, &expired) {
		t.Fatalf("expected SandboxSequenceExpiredError, got %T", sequenceErr)
	}
	if expired.SandboxID != "sandbox-alpha" {
		t.Fatalf("unexpected sandbox id: %q", expired.SandboxID)
	}
	if expired.FromSequence == nil || *expired.FromSequence != 42 {
		t.Fatalf("unexpected from sequence: %#v", expired.FromSequence)
	}
	if expired.OldestSequence != nil {
		t.Fatalf("unexpected oldest sequence: %#v", expired.OldestSequence)
	}

	_, err = client.DeleteSandbox(context.Background(), "sandbox-alpha")
	if err == nil {
		t.Fatal("DeleteSandbox expected error")
	}
	var baseErr *SandboxClientError
	if !errors.As(err, &baseErr) {
		t.Fatalf("expected base SandboxClientError, got %T", err)
	}
	var known *SandboxNotFoundError
	if errors.As(err, &known) {
		t.Fatalf("expected unknown reason to map to base only, got %T", err)
	}
}

func TestSubscribeEvents(t *testing.T) {
	t.Parallel()

	event1 := &agboxv1.SandboxEvent{EventId: "event-1", SandboxId: "sandbox-1"}
	event2 := &agboxv1.SandboxEvent{EventId: "event-2", SandboxId: "sandbox-1"}
	service := &fakeService{
		pingResp: &agboxv1.PingResponse{},
		subscribeEventsPayload: []*agboxv1.SandboxEvent{
			event1,
			event2,
		},
	}
	client := newRawClient(t, service)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	stream, err := client.SubscribeSandboxEvents(context.Background(), "sandbox-1", 7, true)
	if err != nil {
		t.Fatalf("SubscribeSandboxEvents failed: %v", err)
	}
	t.Cleanup(func() {
		if err := stream.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	got1, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv first event failed: %v", err)
	}
	got2, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv second event failed: %v", err)
	}
	if got1.GetEventId() != event1.GetEventId() || got2.GetEventId() != event2.GetEventId() {
		t.Fatalf("unexpected events: %#v %#v", got1, got2)
	}

	if service.subscribeSandboxEventsReq.GetSandboxId() != "sandbox-1" {
		t.Fatalf("unexpected sandbox id: %q", service.subscribeSandboxEventsReq.GetSandboxId())
	}
	if service.subscribeSandboxEventsReq.GetFromSequence() != 7 {
		t.Fatalf("unexpected from_sequence: %d", service.subscribeSandboxEventsReq.GetFromSequence())
	}
	if !service.subscribeSandboxEventsReq.GetIncludeCurrentSnapshot() {
		t.Fatalf("include_current_snapshot should be true")
	}
}

func TestSubscribeEventStreamClosesOnEOF(t *testing.T) {
	t.Parallel()

	var cancelCalls int
	stream := newSandboxEventStream(&subscribeStreamStub{
		events: []*agboxv1.SandboxEvent{
			{EventId: "event-1"},
		},
	}, func() {
		cancelCalls++
	})

	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv first event failed: %v", err)
	}
	if got.GetEventId() != "event-1" {
		t.Fatalf("unexpected first event: %#v", got)
	}

	got, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got event=%#v err=%v", got, err)
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel called %d times, want 1", cancelCalls)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel called %d times after Close, want 1", cancelCalls)
	}
}

func TestSubscribeEventStreamCloseReleasesResources(t *testing.T) {
	t.Parallel()

	var cancelCalls int
	subscribeStream := &subscribeStreamStub{}
	stream := newSandboxEventStream(subscribeStream, func() {
		cancelCalls++
	})

	if err := stream.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel called %d times, want 1", cancelCalls)
	}
	if subscribeStream.closeSendCalls != 1 {
		t.Fatalf("CloseSend called %d times, want 1", subscribeStream.closeSendCalls)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("cancel called %d times after second Close, want 1", cancelCalls)
	}
	if subscribeStream.closeSendCalls != 1 {
		t.Fatalf("CloseSend called %d times after second Close, want 1", subscribeStream.closeSendCalls)
	}
}

func TestTimeoutPolicyInjectedOnlyWhenMissingDeadline(t *testing.T) {
	t.Parallel()

	service := &fakeService{pingResp: &agboxv1.PingResponse{}}
	client := newRawClient(t, service)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})

	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if service.pingCtx == nil {
		t.Fatal("ping context not captured")
	}
	deadlineWithoutCaller, ok := service.pingCtx.Deadline()
	if !ok {
		t.Fatal("default ping call should receive deadline")
	}
	if time.Until(deadlineWithoutCaller) < 4*time.Second || time.Until(deadlineWithoutCaller) > 6*time.Second {
		t.Fatalf("unexpected injected timeout: %v", time.Until(deadlineWithoutCaller))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping with timeout failed: %v", err)
	}
	deadlineWithCaller, ok := service.pingCtx.Deadline()
	if !ok {
		t.Fatal("caller deadline should pass through")
	}
	if time.Until(deadlineWithCaller) > 80*time.Millisecond || time.Until(deadlineWithCaller) < 0 {
		t.Fatalf("unexpected caller deadline propagation: %v", time.Until(deadlineWithCaller))
	}
}

func newRawClient(t *testing.T, service *fakeService) *RawClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	agboxv1.RegisterSandboxServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client, err := New("unused.sock", WithDialOptions(
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return client
}

func newStatusError(t *testing.T, reason string, message string) error {
	t.Helper()
	st := status.New(codes.Unknown, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
	})
	if err != nil {
		t.Fatalf("status.WithDetails failed: %v", err)
	}
	return withDetails.Err()
}

type subscribeStreamStub struct {
	events         []*agboxv1.SandboxEvent
	closeSendCalls int
}

func (s *subscribeStreamStub) Recv() (*agboxv1.SandboxEvent, error) {
	if len(s.events) == 0 {
		return nil, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}

func (s *subscribeStreamStub) CloseSend() error {
	s.closeSendCalls++
	return nil
}

type fakeService struct {
	agboxv1.UnimplementedSandboxServiceServer

	pingCtx                   context.Context
	createSandboxReq          *agboxv1.CreateSandboxRequest
	getSandboxReq             *agboxv1.GetSandboxRequest
	listSandboxesReq          *agboxv1.ListSandboxesRequest
	resumeSandboxReq          *agboxv1.ResumeSandboxRequest
	stopSandboxReq            *agboxv1.StopSandboxRequest
	deleteSandboxReq          *agboxv1.DeleteSandboxRequest
	deleteSandboxesReq        *agboxv1.DeleteSandboxesRequest
	subscribeSandboxEventsReq *agboxv1.SubscribeSandboxEventsRequest
	createExecReq             *agboxv1.CreateExecRequest
	cancelExecReq             *agboxv1.CancelExecRequest
	getExecReq                *agboxv1.GetExecRequest
	listActiveExecsReq        *agboxv1.ListActiveExecsRequest

	pingResp               *agboxv1.PingResponse
	pingErr                error
	createSandboxResp      *agboxv1.CreateSandboxResponse
	createSandboxErr       error
	getSandboxResp         *agboxv1.GetSandboxResponse
	getSandboxErr          error
	listSandboxesResp      *agboxv1.ListSandboxesResponse
	listSandboxesErr       error
	resumeSandboxResp      *agboxv1.AcceptedResponse
	resumeSandboxErr       error
	stopSandboxResp        *agboxv1.AcceptedResponse
	stopSandboxErr         error
	deleteSandboxResp      *agboxv1.AcceptedResponse
	deleteSandboxErr       error
	deleteSandboxesResp    *agboxv1.DeleteSandboxesResponse
	deleteSandboxesErr     error
	subscribeEventsPayload []*agboxv1.SandboxEvent
	subscribeErr           error
	createExecResp         *agboxv1.CreateExecResponse
	createExecErr          error
	cancelExecResp         *agboxv1.AcceptedResponse
	cancelExecErr          error
	getExecResp            *agboxv1.GetExecResponse
	getExecErr             error
	listActiveExecsResp    *agboxv1.ListActiveExecsResponse
	listActiveExecsErr     error
}

func (s *fakeService) Ping(ctx context.Context, _ *agboxv1.PingRequest) (*agboxv1.PingResponse, error) {
	s.pingCtx = ctx
	if s.pingErr != nil {
		return nil, s.pingErr
	}
	if s.pingResp == nil {
		return &agboxv1.PingResponse{}, nil
	}
	return s.pingResp, nil
}

func (s *fakeService) CreateSandbox(ctx context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	s.createSandboxReq = request
	if s.createSandboxErr != nil {
		return nil, s.createSandboxErr
	}
	if s.createSandboxResp == nil {
		return &agboxv1.CreateSandboxResponse{}, nil
	}
	return s.createSandboxResp, nil
}

func (s *fakeService) GetSandbox(ctx context.Context, request *agboxv1.GetSandboxRequest) (*agboxv1.GetSandboxResponse, error) {
	s.getSandboxReq = request
	if s.getSandboxErr != nil {
		return nil, s.getSandboxErr
	}
	if s.getSandboxResp == nil {
		return &agboxv1.GetSandboxResponse{}, nil
	}
	return s.getSandboxResp, nil
}

func (s *fakeService) ListSandboxes(ctx context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	s.listSandboxesReq = request
	if s.listSandboxesErr != nil {
		return nil, s.listSandboxesErr
	}
	if s.listSandboxesResp == nil {
		return &agboxv1.ListSandboxesResponse{}, nil
	}
	return s.listSandboxesResp, nil
}

func (s *fakeService) ResumeSandbox(ctx context.Context, request *agboxv1.ResumeSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.resumeSandboxReq = request
	if s.resumeSandboxErr != nil {
		return nil, s.resumeSandboxErr
	}
	if s.resumeSandboxResp == nil {
		return &agboxv1.AcceptedResponse{}, nil
	}
	return s.resumeSandboxResp, nil
}

func (s *fakeService) StopSandbox(ctx context.Context, request *agboxv1.StopSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.stopSandboxReq = request
	if s.stopSandboxErr != nil {
		return nil, s.stopSandboxErr
	}
	if s.stopSandboxResp == nil {
		return &agboxv1.AcceptedResponse{}, nil
	}
	return s.stopSandboxResp, nil
}

func (s *fakeService) DeleteSandbox(ctx context.Context, request *agboxv1.DeleteSandboxRequest) (*agboxv1.AcceptedResponse, error) {
	s.deleteSandboxReq = request
	if s.deleteSandboxErr != nil {
		return nil, s.deleteSandboxErr
	}
	if s.deleteSandboxResp == nil {
		return &agboxv1.AcceptedResponse{}, nil
	}
	return s.deleteSandboxResp, nil
}

func (s *fakeService) DeleteSandboxes(ctx context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	s.deleteSandboxesReq = request
	if s.deleteSandboxesErr != nil {
		return nil, s.deleteSandboxesErr
	}
	if s.deleteSandboxesResp == nil {
		return &agboxv1.DeleteSandboxesResponse{}, nil
	}
	return s.deleteSandboxesResp, nil
}

func (s *fakeService) SubscribeSandboxEvents(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
	s.subscribeSandboxEventsReq = request
	for _, event := range s.subscribeEventsPayload {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return s.subscribeErr
}

func (s *fakeService) CreateExec(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	s.createExecReq = request
	if s.createExecErr != nil {
		return nil, s.createExecErr
	}
	if s.createExecResp == nil {
		return &agboxv1.CreateExecResponse{}, nil
	}
	return s.createExecResp, nil
}

func (s *fakeService) CancelExec(ctx context.Context, request *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
	s.cancelExecReq = request
	if s.cancelExecErr != nil {
		return nil, s.cancelExecErr
	}
	if s.cancelExecResp == nil {
		return &agboxv1.AcceptedResponse{}, nil
	}
	return s.cancelExecResp, nil
}

func (s *fakeService) GetExec(ctx context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
	s.getExecReq = request
	if s.getExecErr != nil {
		return nil, s.getExecErr
	}
	if s.getExecResp == nil {
		return &agboxv1.GetExecResponse{}, nil
	}
	return s.getExecResp, nil
}

func (s *fakeService) ListActiveExecs(ctx context.Context, request *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
	s.listActiveExecsReq = request
	if s.listActiveExecsErr != nil {
		return nil, s.listActiveExecsErr
	}
	if s.listActiveExecsResp == nil {
		return &agboxv1.ListActiveExecsResponse{}, nil
	}
	return s.listActiveExecsResp, nil
}
