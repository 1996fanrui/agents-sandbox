package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

const (
	defaultOperationTimeout = 60 * time.Second
)

type rpcClient interface {
	Ping(context.Context) (*agboxv1.PingResponse, error)
	CreateSandbox(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	GetSandbox(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	ListSandboxes(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	ResumeSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	StopSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	DeleteSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	DeleteSandboxes(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
	SubscribeSandboxEvents(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)
	CreateExec(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	CancelExec(context.Context, string) (*agboxv1.AcceptedResponse, error)
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	ListActiveExecs(context.Context, string) (*agboxv1.ListActiveExecsResponse, error)
	Close() error
}

type rawClientFactory func(time.Duration) (rpcClient, error)

// Client is the public high-level Go SDK client.
type Client struct {
	rpcClient        rpcClient
	newStreamClient  rawClientFactory
	socketPath       string
	streamTimeout    time.Duration
	operationTimeout time.Duration
}

// New builds a high-level client with the default daemon socket path.
func New(opts ...Option) (*Client, error) {
	config := options{
		timeout:          5 * time.Second,
		operationTimeout: defaultOperationTimeout,
	}
	for _, opt := range opts {
		opt(&config)
	}

	socketPath := config.socketPath
	if !config.socketPathSet {
		resolved, err := rawclient.DefaultSocketPath()
		if err != nil {
			return nil, err
		}
		socketPath = resolved
	}

	streamTimeout := config.streamTimeout
	if !config.streamTimeoutSet {
		streamTimeout = config.timeout
	}

	baseClient, err := rawclient.New(
		socketPath,
		rawclient.WithTimeout(config.timeout),
		rawclient.WithDialOptions(config.dialOptions...),
	)
	if err != nil {
		return nil, err
	}

	newStreamClient := func(timeout time.Duration) (rpcClient, error) {
		return rawclient.New(
			socketPath,
			rawclient.WithTimeout(timeout),
			rawclient.WithDialOptions(config.dialOptions...),
		)
	}

	return &Client{
		rpcClient:        baseClient,
		newStreamClient:  newStreamClient,
		socketPath:       socketPath,
		streamTimeout:    streamTimeout,
		operationTimeout: config.operationTimeout,
	}, nil
}

// Close closes the underlying raw gRPC client.
func (c *Client) Close() error {
	if c == nil || c.rpcClient == nil {
		return nil
	}
	return c.rpcClient.Close()
}

// Ping checks daemon availability.
func (c *Client) Ping(ctx context.Context) (PingInfo, error) {
	response, err := c.rpcClient.Ping(ctx)
	if err != nil {
		return PingInfo{}, err
	}
	return toPingInfo(response), nil
}

// CreateSandbox creates a sandbox and optionally waits until it becomes ready.
// At least one of WithImage or WithConfigYAML must be provided.
func (c *Client) CreateSandbox(ctx context.Context, opts ...CreateSandboxOption) (SandboxHandle, error) {
	options := defaultCreateSandboxOptions()
	for _, opt := range opts {
		if err := opt.applyCreateSandbox(&options); err != nil {
			return SandboxHandle{}, err
		}
	}

	if options.image == nil && len(options.configYAML) == 0 {
		return SandboxHandle{}, fmt.Errorf("at least one of WithImage or WithConfigYAML must be provided")
	}

	image := ""
	if options.image != nil {
		image = *options.image
	}

	request := &agboxv1.CreateSandboxRequest{
		SandboxId:  valueOrEmpty(options.sandboxID),
		ConfigYaml: append([]byte(nil), options.configYAML...),
		CreateSpec: &agboxv1.CreateSpec{
			Image:            image,
			Mounts:           toProtoMounts(options.mounts),
			Copies:           toProtoCopies(options.copies),
			BuiltinTools: slicesClone(options.builtinTools),
			RequiredServices: toProtoServices(options.requiredServices),
			OptionalServices: toProtoServices(options.optionalServices),
			Labels:           cloneStringMap(options.labels),
			Envs:             mapToKeyValues(options.envs),
		},
	}
	response, err := c.rpcClient.CreateSandbox(ctx, request)
	if err != nil {
		return SandboxHandle{}, err
	}
	current, err := c.GetSandbox(ctx, response.GetSandboxId())
	if err != nil {
		return SandboxHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForSandboxState(ctx, response.GetSandboxId(), current, SandboxStateReady, "create_sandbox")
}

// GetSandbox fetches a sandbox handle.
func (c *Client) GetSandbox(ctx context.Context, sandboxID string) (SandboxHandle, error) {
	response, err := c.rpcClient.GetSandbox(ctx, sandboxID)
	if err != nil {
		return SandboxHandle{}, err
	}
	return toSandboxHandle(response.GetSandbox())
}

// ListSandboxes lists sandbox handles with optional filtering.
func (c *Client) ListSandboxes(ctx context.Context, opts ...ListSandboxesOption) ([]SandboxHandle, error) {
	options := defaultListSandboxesOptions()
	for _, opt := range opts {
		opt.applyListSandboxes(&options)
	}
	response, err := c.rpcClient.ListSandboxes(ctx, &agboxv1.ListSandboxesRequest{
		IncludeDeleted: options.includeDeleted,
		LabelSelector:  cloneStringMap(options.labelSelector),
	})
	if err != nil {
		return nil, err
	}
	handles := make([]SandboxHandle, 0, len(response.GetSandboxes()))
	for _, handle := range response.GetSandboxes() {
		converted, convErr := toSandboxHandle(handle)
		if convErr != nil {
			return nil, convErr
		}
		handles = append(handles, converted)
	}
	return handles, nil
}

// ResumeSandbox resumes a stopped sandbox.
func (c *Client) ResumeSandbox(ctx context.Context, sandboxID string, opts ...WaitOption) (SandboxHandle, error) {
	options := defaultWaitOptions()
	for _, opt := range opts {
		opt.applyWait(&options)
	}
	if _, err := c.rpcClient.ResumeSandbox(ctx, sandboxID); err != nil {
		return SandboxHandle{}, err
	}
	current, err := c.GetSandbox(ctx, sandboxID)
	if err != nil {
		return SandboxHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForSandboxState(ctx, sandboxID, current, SandboxStateReady, "resume_sandbox")
}

// StopSandbox stops a running sandbox.
func (c *Client) StopSandbox(ctx context.Context, sandboxID string, opts ...WaitOption) (SandboxHandle, error) {
	options := defaultWaitOptions()
	for _, opt := range opts {
		opt.applyWait(&options)
	}
	if _, err := c.rpcClient.StopSandbox(ctx, sandboxID); err != nil {
		return SandboxHandle{}, err
	}
	current, err := c.GetSandbox(ctx, sandboxID)
	if err != nil {
		return SandboxHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForSandboxState(ctx, sandboxID, current, SandboxStateStopped, "stop_sandbox")
}

// DeleteSandbox deletes a sandbox.
func (c *Client) DeleteSandbox(ctx context.Context, sandboxID string, opts ...WaitOption) (SandboxHandle, error) {
	options := defaultWaitOptions()
	for _, opt := range opts {
		opt.applyWait(&options)
	}
	if _, err := c.rpcClient.DeleteSandbox(ctx, sandboxID); err != nil {
		return SandboxHandle{}, err
	}
	current, err := c.GetSandbox(ctx, sandboxID)
	if err != nil {
		return SandboxHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForSandboxState(ctx, sandboxID, current, SandboxStateDeleted, "delete_sandbox")
}

// DeleteSandboxes deletes sandboxes matching the label selector.
func (c *Client) DeleteSandboxes(ctx context.Context, labelSelector map[string]string, opts ...WaitOption) (DeleteSandboxesResult, error) {
	options := defaultWaitOptions()
	for _, opt := range opts {
		opt.applyWait(&options)
	}
	response, err := c.rpcClient.DeleteSandboxes(ctx, &agboxv1.DeleteSandboxesRequest{
		LabelSelector: cloneStringMap(labelSelector),
	})
	if err != nil {
		return DeleteSandboxesResult{}, err
	}
	result := DeleteSandboxesResult{
		DeletedSandboxIDs: slicesClone(response.GetDeletedSandboxIds()),
		DeletedCount:      response.GetDeletedCount(),
	}
	if !options.wait || len(result.DeletedSandboxIDs) == 0 {
		return result, nil
	}

	errCh := make(chan error, len(result.DeletedSandboxIDs))
	var waitGroup sync.WaitGroup
	for _, sandboxID := range result.DeletedSandboxIDs {
		sandboxID := sandboxID
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			current, getErr := c.GetSandbox(ctx, sandboxID)
			if getErr != nil {
				errCh <- getErr
				return
			}
			if _, waitErr := c.waitForSandboxState(ctx, sandboxID, current, SandboxStateDeleted, "delete_sandboxes"); waitErr != nil {
				errCh <- waitErr
			}
		}()
	}
	waitGroup.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return DeleteSandboxesResult{}, err
		}
	}
	return result, nil
}

// CreateExec creates an exec in the target sandbox.
func (c *Client) CreateExec(ctx context.Context, sandboxID string, command []string, opts ...CreateExecOption) (ExecHandle, error) {
	options := defaultCreateExecOptions()
	for _, opt := range opts {
		if err := opt.applyCreateExec(&options); err != nil {
			return ExecHandle{}, err
		}
	}
	response, err := c.rpcClient.CreateExec(ctx, &agboxv1.CreateExecRequest{
		SandboxId:    sandboxID,
		Command:      slicesClone(command),
		ExecId:       valueOrEmpty(options.execID),
		Cwd:          options.cwd,
		EnvOverrides: mapToKeyValues(options.envOverrides),
	})
	if err != nil {
		return ExecHandle{}, err
	}
	stdoutLogPath := emptyStringPtr(response.GetStdoutLogPath())
	stderrLogPath := emptyStringPtr(response.GetStderrLogPath())
	current, err := c.getExecSnapshot(ctx, response.GetExecId())
	if err != nil {
		return ExecHandle{}, err
	}
	if !options.wait {
		current.handle.StdoutLogPath = stdoutLogPath
		current.handle.StderrLogPath = stderrLogPath
		return current.handle, nil
	}
	handle, err := c.waitForExecTerminal(ctx, response.GetExecId(), sandboxID, current, "create_exec")
	if err != nil {
		return ExecHandle{}, err
	}
	handle.StdoutLogPath = stdoutLogPath
	handle.StderrLogPath = stderrLogPath
	return handle, nil
}

// Run is a convenience wrapper around CreateExec(wait=true).
func (c *Client) Run(ctx context.Context, sandboxID string, command []string, opts ...RunOption) (ExecHandle, error) {
	options := defaultRunOptions()
	for _, opt := range opts {
		opt.applyRun(&options)
	}
	return c.CreateExec(
		ctx,
		sandboxID,
		command,
		withExecCwd(options.cwd),
		withExecEnvOverrides(options.envOverrides),
		waitOption(true),
	)
}

// CancelExec cancels a running exec.
func (c *Client) CancelExec(ctx context.Context, execID string, opts ...WaitOption) (ExecHandle, error) {
	options := defaultWaitOptions()
	for _, opt := range opts {
		opt.applyWait(&options)
	}
	if _, err := c.rpcClient.CancelExec(ctx, execID); err != nil {
		var invalidState *rawclient.SandboxInvalidStateError
		if errors.As(err, &invalidState) {
			return ExecHandle{}, rawclient.NewExecNotRunningError(execID, err)
		}
		return ExecHandle{}, err
	}
	current, err := c.getExecSnapshot(ctx, execID)
	if err != nil {
		return ExecHandle{}, err
	}
	if !options.wait {
		return current.handle, nil
	}
	return c.waitForExecTerminal(ctx, execID, current.handle.SandboxID, current, "cancel_exec")
}

// GetExec fetches an exec handle.
func (c *Client) GetExec(ctx context.Context, execID string) (ExecHandle, error) {
	snapshot, err := c.getExecSnapshot(ctx, execID)
	if err != nil {
		return ExecHandle{}, err
	}
	return snapshot.handle, nil
}

func (c *Client) getExecSnapshot(ctx context.Context, execID string) (execSnapshot, error) {
	response, err := c.rpcClient.GetExec(ctx, execID)
	if err != nil {
		return execSnapshot{}, err
	}
	return toExecSnapshot(response)
}

// ListActiveExecs lists active execs for a sandbox, or all sandboxes when sandboxID is empty.
func (c *Client) ListActiveExecs(ctx context.Context, sandboxID string) ([]ExecHandle, error) {
	response, err := c.rpcClient.ListActiveExecs(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	execs := make([]ExecHandle, 0, len(response.GetExecs()))
	for _, execStatus := range response.GetExecs() {
		execs = append(execs, toExecHandle(execStatus))
	}
	return execs, nil
}

// SubscribeSandboxEvents returns a channel-backed event stream.
func (c *Client) SubscribeSandboxEvents(ctx context.Context, sandboxID string, opts ...SubscribeOption) <-chan EventOrError {
	options := defaultSubscribeOptions()
	for _, opt := range opts {
		if err := opt.applySubscribe(&options); err != nil {
			ch := make(chan EventOrError, 1)
			ch <- EventOrError{Err: err}
			close(ch)
			return ch
		}
	}

	ch := make(chan EventOrError, 1)
	go c.consumeSandboxEvents(ctx, sandboxID, options.fromSequence, options.includeCurrentSnapshot, ch)
	return ch
}

func (c *Client) consumeSandboxEvents(
	ctx context.Context,
	sandboxID string,
	fromSequence uint64,
	includeCurrentSnapshot bool,
	ch chan<- EventOrError,
) {
	defer close(ch)

	streamClient, err := c.newStreamClient(c.streamTimeout)
	if err != nil {
		sendEventOrError(ch, ctx, EventOrError{Err: err})
		return
	}
	defer streamClient.Close()

	stream, err := streamClient.SubscribeSandboxEvents(ctx, sandboxID, fromSequence, includeCurrentSnapshot)
	if err != nil {
		sendEventOrError(ch, ctx, EventOrError{Err: err})
		return
	}
	defer stream.Close()

	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			if ctx.Err() != nil {
				return
			}
			if isEOF(recvErr) {
				return
			}
			sendEventOrError(ch, ctx, EventOrError{Err: recvErr})
			return
		}
		converted, convErr := toSandboxEvent(event)
		if convErr != nil {
			sendEventOrError(ch, ctx, EventOrError{Err: convErr})
			return
		}
		sendEventOrError(ch, ctx, EventOrError{Event: &converted})
	}
}

func sendEventOrError(ch chan<- EventOrError, ctx context.Context, item EventOrError) {
	select {
	case ch <- item:
	case <-ctx.Done():
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
