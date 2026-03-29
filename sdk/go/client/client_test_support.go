package client

import (
	"context"
	"io"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeRPCClient struct {
	pingFn                   func(context.Context) (*agboxv1.PingResponse, error)
	createSandboxFn          func(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	getSandboxFn             func(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	listSandboxesFn          func(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	resumeSandboxFn          func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	stopSandboxFn            func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	deleteSandboxFn          func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	deleteSandboxesFn        func(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
	subscribeSandboxEventsFn func(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)
	createExecFn             func(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	cancelExecFn             func(context.Context, string) (*agboxv1.AcceptedResponse, error)
	getExecFn                func(context.Context, string) (*agboxv1.GetExecResponse, error)
	listActiveExecsFn        func(context.Context, string) (*agboxv1.ListActiveExecsResponse, error)
	closeFn                  func() error
}

func (f *fakeRPCClient) Ping(ctx context.Context) (*agboxv1.PingResponse, error) {
	if f.pingFn != nil {
		return f.pingFn(ctx)
	}
	return &agboxv1.PingResponse{}, nil
}

func (f *fakeRPCClient) CreateSandbox(ctx context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	if f.createSandboxFn != nil {
		return f.createSandboxFn(ctx, request)
	}
	return &agboxv1.CreateSandboxResponse{}, nil
}

func (f *fakeRPCClient) GetSandbox(ctx context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
	if f.getSandboxFn != nil {
		return f.getSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.GetSandboxResponse{}, nil
}

func (f *fakeRPCClient) ListSandboxes(ctx context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	if f.listSandboxesFn != nil {
		return f.listSandboxesFn(ctx, request)
	}
	return &agboxv1.ListSandboxesResponse{}, nil
}

func (f *fakeRPCClient) ResumeSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.resumeSandboxFn != nil {
		return f.resumeSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) StopSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.stopSandboxFn != nil {
		return f.stopSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) DeleteSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	if f.deleteSandboxFn != nil {
		return f.deleteSandboxFn(ctx, sandboxID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) DeleteSandboxes(ctx context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	if f.deleteSandboxesFn != nil {
		return f.deleteSandboxesFn(ctx, request)
	}
	return &agboxv1.DeleteSandboxesResponse{}, nil
}

func (f *fakeRPCClient) SubscribeSandboxEvents(ctx context.Context, sandboxID string, fromSequence uint64, includeCurrentSnapshot bool) (rawclient.SandboxEventStream, error) {
	if f.subscribeSandboxEventsFn != nil {
		return f.subscribeSandboxEventsFn(ctx, sandboxID, fromSequence, includeCurrentSnapshot)
	}
	return streamFromEvents(nil, nil), nil
}

func (f *fakeRPCClient) CreateExec(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	if f.createExecFn != nil {
		return f.createExecFn(ctx, request)
	}
	return &agboxv1.CreateExecResponse{}, nil
}

func (f *fakeRPCClient) CancelExec(ctx context.Context, execID string) (*agboxv1.AcceptedResponse, error) {
	if f.cancelExecFn != nil {
		return f.cancelExecFn(ctx, execID)
	}
	return &agboxv1.AcceptedResponse{}, nil
}

func (f *fakeRPCClient) GetExec(ctx context.Context, execID string) (*agboxv1.GetExecResponse, error) {
	if f.getExecFn != nil {
		return f.getExecFn(ctx, execID)
	}
	return &agboxv1.GetExecResponse{}, nil
}

func (f *fakeRPCClient) ListActiveExecs(ctx context.Context, sandboxID string) (*agboxv1.ListActiveExecsResponse, error) {
	if f.listActiveExecsFn != nil {
		return f.listActiveExecsFn(ctx, sandboxID)
	}
	return &agboxv1.ListActiveExecsResponse{}, nil
}

func (f *fakeRPCClient) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

type fakeStream struct {
	recvFn  func() (*agboxv1.SandboxEvent, error)
	closeFn func() error
	closeMu sync.Mutex
	closed  bool
}

func (s *fakeStream) Recv() (*agboxv1.SandboxEvent, error) {
	if s.recvFn == nil {
		return nil, io.EOF
	}
	return s.recvFn()
}

func (s *fakeStream) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.closed = true
	if s.closeFn != nil {
		return s.closeFn()
	}
	return nil
}

func newTestClient(base rpcClient, streamFactory rawClientFactory) *Client {
	if streamFactory == nil {
		streamFactory = func(time.Duration) (rpcClient, error) {
			return &fakeRPCClient{}, nil
		}
	}
	return &Client{
		rpcClient:        base,
		newStreamClient:  streamFactory,
		streamTimeout:    time.Second,
		operationTimeout: 50 * time.Millisecond,
	}
}

func streamFromEvents(events []*agboxv1.SandboxEvent, finalErr error) rawclient.SandboxEventStream {
	index := 0
	return &fakeStream{
		recvFn: func() (*agboxv1.SandboxEvent, error) {
			if index < len(events) {
				event := events[index]
				index++
				return event, nil
			}
			if finalErr == nil {
				return nil, io.EOF
			}
			return nil, finalErr
		},
	}
}

func blockingStream(ctx context.Context) rawclient.SandboxEventStream {
	return &fakeStream{
		recvFn: func() (*agboxv1.SandboxEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

func eventPB(sandboxID string, sequence uint64, _ string, eventType agboxv1.EventType) *agboxv1.SandboxEvent {
	return &agboxv1.SandboxEvent{
		EventId:    "event",
		Sequence:   sequence,
		SandboxId:  sandboxID,
		EventType:  eventType,
		OccurredAt: timestamppb.New(time.Now()),
	}
}

func withExecEvent(event *agboxv1.SandboxEvent, execID string) *agboxv1.SandboxEvent {
	event.Details = &agboxv1.SandboxEvent_Exec{
		Exec: &agboxv1.ExecEventDetails{
			ExecId:    execID,
			ExecState: agboxv1.ExecState_EXEC_STATE_FINISHED,
		},
	}
	return event
}
