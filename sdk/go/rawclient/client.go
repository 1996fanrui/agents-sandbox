package rawclient

import (
	"context"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc"
)

// RawClient is a thin gRPC client wrapper around the sandbox control service.
type RawClient struct {
	client  agboxv1.SandboxServiceClient
	conn    *grpc.ClientConn
	timeout time.Duration
}

// New creates a new raw client over the given socket path.
func New(socketPath string, opts ...CallOption) (*RawClient, error) {
	options := newDefaultCallOptions()
	for _, opt := range opts {
		opt(&options)
	}

	conn, err := Dial(socketPath, options.dialOptions...)
	if err != nil {
		return nil, err
	}

	return &RawClient{
		client:  agboxv1.NewSandboxServiceClient(conn),
		conn:    conn,
		timeout: options.timeout,
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *RawClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Ping calls SandboxService.Ping.
func (c *RawClient) Ping(ctx context.Context) (*agboxv1.PingResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.PingResponse, error) {
		return c.client.Ping(callCtx, &agboxv1.PingRequest{})
	})
}

// CreateSandbox calls SandboxService.CreateSandbox.
func (c *RawClient) CreateSandbox(ctx context.Context, request *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.CreateSandboxResponse, error) {
		return c.client.CreateSandbox(callCtx, request)
	})
}

// GetSandbox calls SandboxService.GetSandbox.
func (c *RawClient) GetSandbox(ctx context.Context, sandboxID string) (*agboxv1.GetSandboxResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.GetSandboxResponse, error) {
		return c.client.GetSandbox(callCtx, &agboxv1.GetSandboxRequest{
			SandboxId: sandboxID,
		})
	})
}

// ListSandboxes calls SandboxService.ListSandboxes.
func (c *RawClient) ListSandboxes(ctx context.Context, request *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error) {
	if request == nil {
		request = &agboxv1.ListSandboxesRequest{}
	}
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.ListSandboxesResponse, error) {
		return c.client.ListSandboxes(callCtx, request)
	})
}

// ResumeSandbox calls SandboxService.ResumeSandbox.
func (c *RawClient) ResumeSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.AcceptedResponse, error) {
		return c.client.ResumeSandbox(callCtx, &agboxv1.ResumeSandboxRequest{
			SandboxId: sandboxID,
		})
	})
}

// StopSandbox calls SandboxService.StopSandbox.
func (c *RawClient) StopSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.AcceptedResponse, error) {
		return c.client.StopSandbox(callCtx, &agboxv1.StopSandboxRequest{
			SandboxId: sandboxID,
		})
	})
}

// DeleteSandbox calls SandboxService.DeleteSandbox.
func (c *RawClient) DeleteSandbox(ctx context.Context, sandboxID string) (*agboxv1.AcceptedResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.AcceptedResponse, error) {
		return c.client.DeleteSandbox(callCtx, &agboxv1.DeleteSandboxRequest{
			SandboxId: sandboxID,
		})
	})
}

// DeleteSandboxes calls SandboxService.DeleteSandboxes.
func (c *RawClient) DeleteSandboxes(ctx context.Context, request *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.DeleteSandboxesResponse, error) {
		return c.client.DeleteSandboxes(callCtx, request)
	})
}

// SubscribeSandboxEvents calls SandboxService.SubscribeSandboxEvents.
func (c *RawClient) SubscribeSandboxEvents(
	ctx context.Context,
	sandboxID string,
	fromSequence uint64,
	includeCurrentSnapshot bool,
) (SandboxEventStream, error) {
	callCtx, cancel := withTimeout(ctx, c.timeout)
	stream, err := c.client.SubscribeSandboxEvents(callCtx, &agboxv1.SubscribeSandboxEventsRequest{
		SandboxId:              sandboxID,
		FromSequence:           fromSequence,
		IncludeCurrentSnapshot: includeCurrentSnapshot,
	})
	if err != nil {
		cancel()
		return nil, translateRPCError(err)
	}
	return newSandboxEventStream(stream, cancel), nil
}

// CreateExec calls SandboxService.CreateExec.
func (c *RawClient) CreateExec(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.CreateExecResponse, error) {
		return c.client.CreateExec(callCtx, request)
	})
}

// CancelExec calls SandboxService.CancelExec.
func (c *RawClient) CancelExec(ctx context.Context, execID string) (*agboxv1.AcceptedResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.AcceptedResponse, error) {
		return c.client.CancelExec(callCtx, &agboxv1.CancelExecRequest{
			ExecId: execID,
		})
	})
}

// GetExec calls SandboxService.GetExec.
func (c *RawClient) GetExec(ctx context.Context, execID string) (*agboxv1.GetExecResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.GetExecResponse, error) {
		return c.client.GetExec(callCtx, &agboxv1.GetExecRequest{
			ExecId: execID,
		})
	})
}

// ListActiveExecs calls SandboxService.ListActiveExecs.
func (c *RawClient) ListActiveExecs(ctx context.Context, sandboxID string) (*agboxv1.ListActiveExecsResponse, error) {
	return callWithTimeoutUnary(ctx, c.timeout, func(callCtx context.Context) (*agboxv1.ListActiveExecsResponse, error) {
		req := &agboxv1.ListActiveExecsRequest{}
		if sandboxID != "" {
			req.SandboxId = &sandboxID
		}
		return c.client.ListActiveExecs(callCtx, req)
	})
}

func callWithTimeoutUnary[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	callCtx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	response, err := fn(callCtx)
	if err != nil {
		var zero T
		return zero, translateRPCError(err)
	}
	return response, nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	_, hasDeadline := ctx.Deadline()
	if hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
