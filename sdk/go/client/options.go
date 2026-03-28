package client

import (
	"fmt"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc"
)

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

type CreateSandboxOption interface {
	applyCreateSandbox(*createSandboxOptions) error
}

type createSandboxOptions struct {
	image            *string
	configYAML       []byte
	sandboxID        *string
	mounts           []MountSpec
	copies           []CopySpec
	builtinResources []string
	requiredServices []ServiceSpec
	optionalServices []ServiceSpec
	labels           map[string]string
	envs             map[string]string
	wait             bool
}

func defaultCreateSandboxOptions() createSandboxOptions {
	return createSandboxOptions{
		labels: map[string]string{},
		envs:   map[string]string{},
		wait:   true,
	}
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

type WaitOption interface {
	applyWait(*waitOptions)
}

type waitOptions struct {
	wait bool
}

func defaultWaitOptions() waitOptions {
	return waitOptions{wait: true}
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

type SubscribeOption interface {
	applySubscribe(*subscribeOptions) error
}

type subscribeOptions struct {
	fromSequence           uint64
	includeCurrentSnapshot bool
}

func defaultSubscribeOptions() subscribeOptions {
	return subscribeOptions{
		fromSequence: 0,
	}
}

type imageOption string

// WithImage sets the sandbox container image.
func WithImage(image string) imageOption {
	return imageOption(image)
}

func (o imageOption) applyCreateSandbox(opts *createSandboxOptions) error {
	value := string(o)
	if value == "" {
		return fmt.Errorf("image must not be empty")
	}
	opts.image = &value
	return nil
}

type configYAMLOption []byte

// WithConfigYAML sets raw YAML content for sandbox creation.
func WithConfigYAML(configYAML []byte) configYAMLOption {
	return configYAMLOption(append([]byte(nil), configYAML...))
}

func (o configYAMLOption) applyCreateSandbox(opts *createSandboxOptions) error {
	if len(o) == 0 {
		return fmt.Errorf("config_yaml must not be empty")
	}
	opts.configYAML = append([]byte(nil), o...)
	return nil
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

type envsOption map[string]string

// WithEnvs sets sandbox-level environment variables applied to the primary container
// and inherited by all exec commands.
func WithEnvs(envs map[string]string) envsOption {
	return envsOption(cloneStringMap(envs))
}

func (o envsOption) applyCreateSandbox(opts *createSandboxOptions) error {
	opts.envs = cloneStringMap(map[string]string(o))
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

type fromSequenceOption uint64

// WithFromSequence sets the subscription start sequence.
func WithFromSequence(sequence uint64) fromSequenceOption {
	return fromSequenceOption(sequence)
}

func (o fromSequenceOption) applySubscribe(opts *subscribeOptions) error {
	opts.fromSequence = uint64(o)
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
