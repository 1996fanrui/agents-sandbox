package main

import (
	"context"
	"fmt"
	"io"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	// defaultImage is the default container image used when --image is not specified.
	defaultImage = "ghcr.io/agents-sandbox/coding-runtime:latest"
)

// CLI wait output convention (all messages go to stderr):
//
//   Waiting for sandbox <id> to be <action>...
//   Sandbox <action> in <duration>.
//     <tip label>:  <command>
//
// On failure, a newline is printed after "..." and the error is returned.

type sandboxClient interface {
	CreateSandbox(context.Context, *agboxv1.CreateSandboxRequest) (*agboxv1.CreateSandboxResponse, error)
	ListSandboxes(context.Context, *agboxv1.ListSandboxesRequest) (*agboxv1.ListSandboxesResponse, error)
	GetSandbox(context.Context, string) (*agboxv1.GetSandboxResponse, error)
	DeleteSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	DeleteSandboxes(context.Context, *agboxv1.DeleteSandboxesRequest) (*agboxv1.DeleteSandboxesResponse, error)
}

type sandboxExecClient interface {
	sandboxClient
	CreateExec(context.Context, *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error)
	SubscribeSandboxEvents(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	CancelExec(context.Context, string) (*agboxv1.AcceptedResponse, error)
	StopSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
	ResumeSandbox(context.Context, string) (*agboxv1.AcceptedResponse, error)
}

type sandboxCreateArgs struct {
	image      string
	labels     map[string]string
	idleTTL    *time.Duration
	json       bool
	configYAML []byte
	sandboxID  string
}

type sandboxListArgs struct {
	includeDeleted bool
	labels         map[string]string
	json           bool
}

type sandboxGetArgs struct {
	sandboxID string
	json      bool
}

type sandboxDeleteArgs struct {
	sandboxID string
	labels    map[string]string
}

type sandboxExecArgs struct {
	sandboxID    string
	cwd          string
	envOverrides map[string]string
	command      []string
}

func runSandboxCreate(ctx context.Context, client sandboxExecClient, parsed sandboxCreateArgs, stdout io.Writer, stderr io.Writer) error {
	createSpec := &agboxv1.CreateSpec{
		Image:  parsed.image,
		Labels: parsed.labels,
	}
	if parsed.idleTTL != nil {
		createSpec.IdleTtl = durationpb.New(*parsed.idleTTL)
	}
	response, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		CreateSpec: createSpec,
		SandboxId:  parsed.sandboxID,
		ConfigYaml: parsed.configYAML,
	})
	if err != nil {
		return runtimeErrorf("create sandbox: %v", err)
	}

	sandbox := response.GetSandbox()
	sandboxID := sandbox.GetSandboxId()

	// JSON mode: return immediately without waiting.
	if parsed.json {
		data, err := formatSandboxCreateResponse(response)
		if err != nil {
			return runtimeErrorf("format create sandbox response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	// Wait for sandbox to become ready.
	_, _ = fmt.Fprintf(stderr, "Waiting for sandbox %s to be ready...", sandboxID)
	waitStart := time.Now()
	if err := waitForSandboxReady(ctx, client, sandboxID, sandbox.GetLastEventSequence(), nil, nil); err != nil {
		_, _ = fmt.Fprintln(stderr)
		return err
	}

	containerName := primaryContainerName(sandboxID)
	_, _ = fmt.Fprintf(stderr, "\nSandbox ready in %.1fs.\n", time.Since(waitStart).Seconds())
	_, _ = fmt.Fprintf(stderr, "  Open a shell:       docker exec -it --user agbox %s bash\n", containerName)
	_, _ = fmt.Fprintf(stderr, "  Delete sandbox:     agbox sandbox delete %s\n", sandboxID)
	return nil
}

func runSandboxList(ctx context.Context, client sandboxClient, parsed sandboxListArgs, stdout io.Writer) error {
	response, err := client.ListSandboxes(ctx, &agboxv1.ListSandboxesRequest{
		IncludeDeleted: parsed.includeDeleted,
		LabelSelector:  parsed.labels,
	})
	if err != nil {
		return runtimeErrorf("list sandboxes: %v", err)
	}

	if parsed.json {
		data, err := formatSandboxListResponse(response)
		if err != nil {
			return runtimeErrorf("format list sandboxes response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprint(stdout, formatSandboxListTable(response.GetSandboxes()))
	return nil
}

func runSandboxGet(ctx context.Context, client sandboxClient, parsed sandboxGetArgs, stdout io.Writer) error {
	response, err := client.GetSandbox(ctx, parsed.sandboxID)
	if err != nil {
		return runtimeErrorf("get sandbox: %v", err)
	}

	if parsed.json {
		data, err := formatSandboxGetResponse(response)
		if err != nil {
			return runtimeErrorf("format get sandbox response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprint(stdout, formatSandboxHandleText(response.GetSandbox()))
	return nil
}

func runSandboxDelete(ctx context.Context, client sandboxExecClient, parsed sandboxDeleteArgs, stdout io.Writer, stderr io.Writer) error {
	if parsed.sandboxID != "" && len(parsed.labels) > 0 {
		return usageErrorf("sandbox delete <sandbox_id> and --label are mutually exclusive")
	}
	if parsed.sandboxID == "" && len(parsed.labels) == 0 {
		return usageErrorf("sandbox delete requires <sandbox_id> or at least one --label")
	}

	if parsed.sandboxID != "" {
		return deleteSingleSandbox(ctx, client, parsed.sandboxID, stdout, stderr)
	}

	return deleteSandboxesByLabel(ctx, client, parsed.labels, stdout, stderr)
}

func deleteSingleSandbox(ctx context.Context, client sandboxExecClient, sandboxID string, _ io.Writer, stderr io.Writer) error {
	if _, err := client.DeleteSandbox(ctx, sandboxID); err != nil {
		return runtimeErrorf("delete sandbox: %v", err)
	}

	_, _ = fmt.Fprintf(stderr, "Waiting for sandbox %s to be deleted...\n", sandboxID)
	waitStart := time.Now()
	if err := waitForSandboxState(ctx, client, sandboxID, classifyDeleted); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Sandbox deleted in %.1fs.\n", time.Since(waitStart).Seconds())
	return nil
}

func deleteSandboxesByLabel(ctx context.Context, client sandboxExecClient, labels map[string]string, _ io.Writer, stderr io.Writer) error {
	response, err := client.DeleteSandboxes(ctx, &agboxv1.DeleteSandboxesRequest{
		LabelSelector: labels,
	})
	if err != nil {
		return runtimeErrorf("delete sandboxes: %v", err)
	}

	ids := response.GetDeletedSandboxIds()
	if len(ids) == 0 {
		_, _ = fmt.Fprintln(stderr, "No sandboxes matched the label selector.")
		return nil
	}

	_, _ = fmt.Fprintf(stderr, "Waiting for %d sandbox(es) to be deleted...\n", len(ids))
	waitStart := time.Now()
	for _, id := range ids {
		if err := waitForSandboxState(ctx, client, id, classifyDeleted); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(stderr, "%d sandbox(es) deleted in %.1fs.\n", len(ids), time.Since(waitStart).Seconds())
	return nil
}

// sandboxErrorDetail returns the error message from a sandbox handle,
// falling back to the error code or a generic message.
func sandboxErrorDetail(sandbox *agboxv1.SandboxHandle) string {
	if msg := sandbox.GetErrorMessage(); msg != "" {
		return msg
	}
	if code := sandbox.GetErrorCode(); code != "" {
		return code
	}
	return "unknown error"
}

// sandboxFailedError fetches the sandbox to get error details and returns a formatted error.
func sandboxFailedError(ctx context.Context, client sandboxExecClient, sandboxID string) error {
	getResp, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		return runtimeErrorf("sandbox %s failed (could not fetch details: %v)", sandboxID, err)
	}
	return runtimeErrorf("sandbox %s failed: %s", sandboxID, sandboxErrorDetail(getResp.GetSandbox()))
}

func runSandboxStop(ctx context.Context, client sandboxExecClient, sandboxID string, stderr io.Writer) error {
	if _, err := client.StopSandbox(ctx, sandboxID); err != nil {
		return runtimeErrorf("stop sandbox: %v", err)
	}

	_, _ = fmt.Fprintf(stderr, "Waiting for sandbox %s to be stopped...", sandboxID)
	waitStart := time.Now()
	if err := waitForSandboxState(ctx, client, sandboxID, classifyStopped); err != nil {
		_, _ = fmt.Fprintln(stderr)
		return err
	}
	_, _ = fmt.Fprintf(stderr, "\nSandbox stopped in %.1fs.\n", time.Since(waitStart).Seconds())
	return nil
}

func runSandboxResume(ctx context.Context, client sandboxExecClient, sandboxID string, stderr io.Writer) error {
	if _, err := client.ResumeSandbox(ctx, sandboxID); err != nil {
		return runtimeErrorf("resume sandbox: %v", err)
	}

	_, _ = fmt.Fprintf(stderr, "Waiting for sandbox %s to be resumed...", sandboxID)
	waitStart := time.Now()
	if err := waitForSandboxState(ctx, client, sandboxID, classifyResumed); err != nil {
		_, _ = fmt.Fprintln(stderr)
		return err
	}
	_, _ = fmt.Fprintf(stderr, "\nSandbox resumed in %.1fs.\n", time.Since(waitStart).Seconds())
	return nil
}

// stateVerdict is the result of classifying a sandbox state during a wait loop.
type stateVerdict int

const (
	stateKeepWaiting stateVerdict = iota // not yet at target, continue polling
	stateReached                         // target state reached, return success
	stateFailed                          // error state, return failure
)

// stateClassifier maps a sandbox state to a verdict. The sandboxID is passed
// so that the classifier can produce descriptive error messages.
type stateClassifier func(state agboxv1.SandboxState, sandboxID string, detail func() string) (stateVerdict, error)

// classifyDeleted is used by delete waits.
func classifyDeleted(state agboxv1.SandboxState, _ string, _ func() string) (stateVerdict, error) {
	if state == agboxv1.SandboxState_SANDBOX_STATE_DELETED {
		return stateReached, nil
	}
	return stateKeepWaiting, nil
}

// classifyStopped is used by waitForSandboxStopped (stop waits).
func classifyStopped(state agboxv1.SandboxState, sandboxID string, detail func() string) (stateVerdict, error) {
	switch state {
	case agboxv1.SandboxState_SANDBOX_STATE_STOPPED:
		return stateReached, nil
	case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
		return stateFailed, runtimeErrorf("sandbox %s failed: %s", sandboxID, detail())
	case agboxv1.SandboxState_SANDBOX_STATE_DELETED:
		return stateFailed, runtimeErrorf("sandbox %s was deleted while waiting for stop", sandboxID)
	default:
		return stateKeepWaiting, nil
	}
}

// classifyResumed is used by waitForSandboxResumed (resume waits).
// Tolerates STOPPED and PENDING as transitional states after a resume request.
func classifyResumed(state agboxv1.SandboxState, sandboxID string, detail func() string) (stateVerdict, error) {
	switch state {
	case agboxv1.SandboxState_SANDBOX_STATE_READY:
		return stateReached, nil
	case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
		return stateFailed, runtimeErrorf("sandbox %s failed: %s", sandboxID, detail())
	case agboxv1.SandboxState_SANDBOX_STATE_DELETED:
		return stateFailed, runtimeErrorf("sandbox %s was deleted while waiting for resume", sandboxID)
	default:
		return stateKeepWaiting, nil
	}
}

// waitForSandboxState is the generic wait loop for sandbox state transitions.
// It polls GetSandbox, then subscribes to events and classifies each state
// change using the provided classifier until the target state is reached or
// an error state is encountered.
func waitForSandboxState(ctx context.Context, client sandboxExecClient, sandboxID string, classify stateClassifier) error {
	getResp, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		return runtimeErrorf("get sandbox: %v", err)
	}
	sandbox := getResp.GetSandbox()

	detailFn := func() string { return sandboxErrorDetail(sandbox) }
	verdict, err := classify(sandbox.GetState(), sandboxID, detailFn)
	if err != nil {
		return err
	}
	if verdict == stateReached {
		return nil
	}

	cursorSeq := sandbox.GetLastEventSequence()
	stream, err := client.SubscribeSandboxEvents(ctx, sandboxID, cursorSeq, false)
	if err != nil {
		return runtimeErrorf("subscribe sandbox events: %v", err)
	}
	defer stream.Close()

	eventCh := make(chan sandboxEventResult, 1)
	go pumpSandboxEvents(stream, eventCh)

	for {
		select {
		case result, ok := <-eventCh:
			if !ok {
				return runtimeErrorf("sandbox event stream closed unexpectedly")
			}
			if result.err != nil {
				return runtimeErrorf("wait for sandbox state: %v", result.err)
			}
			// For event-based classification, fetch fresh sandbox details for error messages.
			eventDetailFn := func() string {
				resp, fetchErr := client.GetSandbox(ctx, sandboxID)
				if fetchErr != nil {
					return fmt.Sprintf("could not fetch details: %v", fetchErr)
				}
				return sandboxErrorDetail(resp.GetSandbox())
			}
			verdict, err := classify(result.event.GetSandboxState(), sandboxID, eventDetailFn)
			if err != nil {
				return err
			}
			if verdict == stateReached {
				return nil
			}
		case <-ctx.Done():
			return runtimeErrorf("wait for sandbox state: %v", ctx.Err())
		}
	}
}
