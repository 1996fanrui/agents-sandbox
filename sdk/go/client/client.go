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
	"google.golang.org/grpc"
)

const (
	defaultOperationTimeout = 60 * time.Second
	defaultExecPollInterval = 250 * time.Millisecond
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
	SubscribeSandboxEvents(context.Context, string, string, bool) (rawclient.SandboxEventStream, error)
	CreateExec(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	CancelExec(context.Context, string) (*agboxv1.AcceptedResponse, error)
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	ListActiveExecs(context.Context, string) (*agboxv1.ListActiveExecsResponse, error)
	Close() error
}

type rawClientFactory func(time.Duration) (rpcClient, error)

type options struct {
	timeout          time.Duration
	streamTimeout    time.Duration
	streamTimeoutSet bool
	operationTimeout time.Duration
	socketPath       string
	socketPathSet    bool
	dialOptions      []grpc.DialOption
}

// Option configures Client construction.
type Option func(*options)

// WithTimeout sets the default unary RPC timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(opts *options) {
		opts.timeout = timeout
	}
}

// WithStreamTimeout sets the default stream timeout used by event subscriptions.
func WithStreamTimeout(timeout time.Duration) Option {
	return func(opts *options) {
		opts.streamTimeout = timeout
		opts.streamTimeoutSet = true
	}
}

// WithOperationTimeout sets the overall wait timeout.
func WithOperationTimeout(timeout time.Duration) Option {
	return func(opts *options) {
		opts.operationTimeout = timeout
	}
}

// WithSocketPath overrides the resolved daemon socket path.
func WithSocketPath(socketPath string) Option {
	return func(opts *options) {
		opts.socketPath = socketPath
		opts.socketPathSet = true
	}
}

// WithDialOptions appends additional gRPC dial options.
func WithDialOptions(dialOptions ...grpc.DialOption) Option {
	return func(opts *options) {
		opts.dialOptions = append(opts.dialOptions, dialOptions...)
	}
}

// Client is the public high-level Go SDK client.
type Client struct {
	rpcClient        rpcClient
	newStreamClient  rawClientFactory
	socketPath       string
	streamTimeout    time.Duration
	operationTimeout time.Duration
	execPollInterval time.Duration
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
		execPollInterval: defaultExecPollInterval,
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

type CreateSandboxOption interface {
	applyCreateSandbox(*createSandboxOptions) error
}

type createSandboxOptions struct {
	sandboxID        *string
	mounts           []MountSpec
	copies           []CopySpec
	builtinResources []string
	requiredServices []ServiceSpec
	optionalServices []ServiceSpec
	labels           map[string]string
	wait             bool
}

func defaultCreateSandboxOptions() createSandboxOptions {
	return createSandboxOptions{
		labels: map[string]string{},
		wait:   true,
	}
}

// CreateSandbox creates a sandbox and optionally waits until it becomes ready.
func (c *Client) CreateSandbox(ctx context.Context, image string, opts ...CreateSandboxOption) (SandboxHandle, error) {
	options := defaultCreateSandboxOptions()
	for _, opt := range opts {
		if err := opt.applyCreateSandbox(&options); err != nil {
			return SandboxHandle{}, err
		}
	}

	request := &agboxv1.CreateSandboxRequest{
		SandboxId: valueOrEmpty(options.sandboxID),
		CreateSpec: &agboxv1.CreateSpec{
			Image:            image,
			Mounts:           toProtoMounts(options.mounts),
			Copies:           toProtoCopies(options.copies),
			BuiltinResources: slicesClone(options.builtinResources),
			RequiredServices: toProtoServices(options.requiredServices),
			OptionalServices: toProtoServices(options.optionalServices),
			Labels:           cloneStringMap(options.labels),
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

type ListSandboxesOption interface {
	applyListSandboxes(*listSandboxesOptions)
}

type listSandboxesOptions struct {
	includeDeleted bool
	labelSelector  map[string]string
}

func defaultListSandboxesOptions() listSandboxesOptions {
	return listSandboxesOptions{
		labelSelector: map[string]string{},
	}
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

type WaitOption interface {
	applyWait(*waitOptions)
}

type waitOptions struct {
	wait bool
}

func defaultWaitOptions() waitOptions {
	return waitOptions{wait: true}
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

type CreateExecOption interface {
	applyCreateExec(*createExecOptions) error
}

type createExecOptions struct {
	execID       *string
	cwd          string
	envOverrides map[string]string
	wait         bool
}

func defaultCreateExecOptions() createExecOptions {
	return createExecOptions{
		cwd:          "/workspace",
		envOverrides: map[string]string{},
	}
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
	current, err := c.GetExec(ctx, response.GetExecId())
	if err != nil {
		return ExecHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForExecTerminal(ctx, response.GetExecId(), sandboxID, current, "create_exec")
}

type RunOption interface {
	applyRun(*runOptions)
}

type runOptions struct {
	cwd          string
	envOverrides map[string]string
}

func defaultRunOptions() runOptions {
	return runOptions{
		cwd:          "/workspace",
		envOverrides: map[string]string{},
	}
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
	current, err := c.GetExec(ctx, execID)
	if err != nil {
		return ExecHandle{}, err
	}
	if !options.wait {
		return current, nil
	}
	return c.waitForExecTerminal(ctx, execID, current.SandboxID, current, "cancel_exec")
}

// GetExec fetches an exec handle.
func (c *Client) GetExec(ctx context.Context, execID string) (ExecHandle, error) {
	response, err := c.rpcClient.GetExec(ctx, execID)
	if err != nil {
		return ExecHandle{}, err
	}
	return toExecHandle(response.GetExec()), nil
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

type SubscribeOption interface {
	applySubscribe(*subscribeOptions) error
}

type subscribeOptions struct {
	fromCursor             string
	includeCurrentSnapshot bool
}

func defaultSubscribeOptions() subscribeOptions {
	return subscribeOptions{
		fromCursor: "0",
	}
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

	normalizedCursor, err := normalizeFromCursor(sandboxID, options.fromCursor)
	if err != nil {
		ch := make(chan EventOrError, 1)
		ch <- EventOrError{Err: err}
		close(ch)
		return ch
	}

	ch := make(chan EventOrError, 1)
	go c.consumeSandboxEvents(ctx, sandboxID, normalizedCursor, options.includeCurrentSnapshot, ch)
	return ch
}

func (c *Client) consumeSandboxEvents(
	ctx context.Context,
	sandboxID string,
	fromCursor string,
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

	stream, err := streamClient.SubscribeSandboxEvents(ctx, sandboxID, fromCursor, includeCurrentSnapshot)
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

type waitOption bool

// WithWait sets the wait behavior for operations that support it.
func WithWait(wait bool) waitOption {
	return waitOption(wait)
}

func (o waitOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.wait = bool(o)
	return nil
}

func (o waitOption) applyWait(opts *waitOptions) {
	opts.wait = bool(o)
}

func (o waitOption) applyCreateExec(opts *createExecOptions) error {
	opts.wait = bool(o)
	return nil
}

type sandboxIDOption string

// WithSandboxID sets the caller-provided sandbox identifier.
func WithSandboxID(id string) sandboxIDOption {
	return sandboxIDOption(id)
}

func (o sandboxIDOption) applyCreateSandbox(opts *createSandboxOptions) error {
	value := string(o)
	if value == "" {
		return fmt.Errorf("sandbox_id must not be empty")
	}
	opts.sandboxID = &value
	return nil
}

type mountsOption []MountSpec

// WithMounts sets the sandbox mounts.
func WithMounts(mounts ...MountSpec) mountsOption {
	return mountsOption(slicesClone(mounts))
}

func (o mountsOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.mounts = slicesClone([]MountSpec(o))
	return nil
}

type copiesOption []CopySpec

// WithCopies sets the sandbox copies.
func WithCopies(copies ...CopySpec) copiesOption {
	return copiesOption(slicesClone(copies))
}

func (o copiesOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.copies = slicesClone([]CopySpec(o))
	return nil
}

type builtinResourcesOption []string

// WithBuiltinResources sets the built-in resources.
func WithBuiltinResources(resources ...string) builtinResourcesOption {
	return builtinResourcesOption(slicesClone(resources))
}

func (o builtinResourcesOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.builtinResources = slicesClone([]string(o))
	return nil
}

type requiredServicesOption []ServiceSpec

// WithRequiredServices sets required services.
func WithRequiredServices(services ...ServiceSpec) requiredServicesOption {
	return requiredServicesOption(slicesClone(services))
}

func (o requiredServicesOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.requiredServices = slicesClone([]ServiceSpec(o))
	return nil
}

type optionalServicesOption []ServiceSpec

// WithOptionalServices sets optional services.
func WithOptionalServices(services ...ServiceSpec) optionalServicesOption {
	return optionalServicesOption(slicesClone(services))
}

func (o optionalServicesOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.optionalServices = slicesClone([]ServiceSpec(o))
	return nil
}

type labelsOption map[string]string

// WithLabels sets sandbox labels.
func WithLabels(labels map[string]string) labelsOption {
	return labelsOption(cloneStringMap(labels))
}

func (o labelsOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.labels = cloneStringMap(map[string]string(o))
	return nil
}

type includeDeletedOption bool

// WithIncludeDeleted toggles deleted sandboxes in list results.
func WithIncludeDeleted(include bool) includeDeletedOption {
	return includeDeletedOption(include)
}

func (o includeDeletedOption) applyListSandboxes(opts *listSandboxesOptions) {
	opts.includeDeleted = bool(o)
}

type labelSelectorOption map[string]string

// WithLabelSelector sets the label selector for list operations.
func WithLabelSelector(selector map[string]string) labelSelectorOption {
	return labelSelectorOption(cloneStringMap(selector))
}

func (o labelSelectorOption) applyListSandboxes(opts *listSandboxesOptions) {
	opts.labelSelector = cloneStringMap(map[string]string(o))
}

type execIDOption string

// WithExecID sets the caller-provided exec identifier.
func WithExecID(id string) execIDOption {
	return execIDOption(id)
}

func (o execIDOption) applyCreateExec(opts *createExecOptions) error {
	value := string(o)
	if value == "" {
		return fmt.Errorf("exec_id must not be empty")
	}
	opts.execID = &value
	return nil
}

type cwdOption string

// WithCwd overrides the exec working directory.
func WithCwd(cwd string) cwdOption {
	return cwdOption(cwd)
}

func (o cwdOption) applyCreateExec(opts *createExecOptions) error {
	opts.cwd = string(o)
	return nil
}

func (o cwdOption) applyRun(opts *runOptions) {
	opts.cwd = string(o)
}

func withExecCwd(cwd string) cwdOption {
	return cwdOption(cwd)
}

type envOverridesOption map[string]string

// WithEnvOverrides sets exec environment overrides.
func WithEnvOverrides(values map[string]string) envOverridesOption {
	return envOverridesOption(cloneStringMap(values))
}

func (o envOverridesOption) applyCreateExec(opts *createExecOptions) error {
	opts.envOverrides = cloneStringMap(map[string]string(o))
	return nil
}

func (o envOverridesOption) applyRun(opts *runOptions) {
	opts.envOverrides = cloneStringMap(map[string]string(o))
}

func withExecEnvOverrides(values map[string]string) envOverridesOption {
	return envOverridesOption(cloneStringMap(values))
}

type fromCursorOption string

// WithFromCursor sets the subscription start cursor.
func WithFromCursor(cursor string) fromCursorOption {
	return fromCursorOption(cursor)
}

func (o fromCursorOption) applySubscribe(opts *subscribeOptions) error {
	opts.fromCursor = string(o)
	return nil
}

type includeCurrentSnapshotOption bool

// WithIncludeCurrentSnapshot toggles snapshot replay for subscriptions.
func WithIncludeCurrentSnapshot(include bool) includeCurrentSnapshotOption {
	return includeCurrentSnapshotOption(include)
}

func (o includeCurrentSnapshotOption) applySubscribe(opts *subscribeOptions) error {
	opts.includeCurrentSnapshot = bool(o)
	return nil
}

func toProtoMounts(mounts []MountSpec) []*agboxv1.MountSpec {
	result := make([]*agboxv1.MountSpec, 0, len(mounts))
	for _, mount := range mounts {
		result = append(result, toProtoMount(mount))
	}
	return result
}

func toProtoCopies(copies []CopySpec) []*agboxv1.CopySpec {
	result := make([]*agboxv1.CopySpec, 0, len(copies))
	for _, copySpec := range copies {
		result = append(result, toProtoCopy(copySpec))
	}
	return result
}

func toProtoServices(services []ServiceSpec) []*agboxv1.ServiceSpec {
	result := make([]*agboxv1.ServiceSpec, 0, len(services))
	for _, service := range services {
		result = append(result, toProtoService(service))
	}
	return result
}

func slicesClone[T any](values []T) []T {
	return append([]T(nil), values...)
}
