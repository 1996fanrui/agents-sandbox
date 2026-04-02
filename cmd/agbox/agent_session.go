package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"golang.org/x/term"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	// defaultImage is the coding runtime image used for interactive agent sessions.
	// Pre-registered agent types are designed for this image; the commands inside it
	// may not be available in other images.
	defaultImage = "ghcr.io/agents-sandbox/coding-runtime:latest"

	// deleteAndWaitTimeout is the maximum time to wait for sandbox deletion to complete.
	deleteAndWaitTimeout = 30 * time.Second

	// sigtermGracePeriod is how long we wait for the docker exec child after forwarding SIGTERM
	// before escalating to SIGKILL.
	sigtermGracePeriod = 10 * time.Second
)

// agentTypeDef defines the container-internal command and the builtin tools for an agent type.
type agentTypeDef struct {
	command      []string
	builtinTools []string
}

// agentTypeDefs maps agent type names to their full definitions.
var agentTypeDefs = map[string]agentTypeDef{
	"claude": {
		command:      []string{"claude", "--dangerously-skip-permissions"},
		builtinTools: []string{"claude", "git", "uv", "npm", "apt"},
	},
	"codex": {
		command:      []string{"codex", "--dangerously-bypass-approvals-and-sandbox"},
		builtinTools: []string{"codex", "git", "uv", "npm", "apt"},
	},
}

// sanitizeContainerName replicates the daemon's sanitizeRuntimeName rule so that
// the CLI can derive the primary container name from a sandbox ID without an RPC.
// Keeping this in sync with dockerPrimaryContainerName in internal/control is a known
// coupling point: if the daemon changes its naming scheme, this function must be updated.
func sanitizeContainerName(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", ".", "-", "_", "-")
	return replacer.Replace(value)
}

func primaryContainerName(sandboxID string) string {
	return "agbox-primary-" + sanitizeContainerName(sandboxID)
}

// runAgentSession implements the shared flow for `agbox agent <tool>` and
// `agbox agent --command "..."`.
func runAgentSession(
	ctx context.Context,
	parsed agentSessionArgs,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) error {
	// Require a real TTY on stdin. Passing -t to docker exec without a TTY causes
	// docker to exit immediately with an error, and the interactive experience
	// (echo, Ctrl+C, terminal size) would be broken anyway.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return usageErrorf("stdin is not a TTY; agbox agent requires an interactive terminal")
	}

	if _, err := os.Stat(parsed.workspace); err != nil {
		return usageErrorf("--workspace path %q: %v", parsed.workspace, err)
	}

	// Prompt for confirmation when the workspace lacks a top-level .git directory.
	if _, err := os.Stat(filepath.Join(parsed.workspace, ".git")); os.IsNotExist(err) {
		if confirmErr := confirmWorkspaceCopy(os.Stdin, stderr, parsed.workspace); confirmErr != nil {
			return confirmErr
		}
	}

	// Derive label for sandbox metadata. For registered agent types use the type name;
	// for custom commands use the basename of the first command token.
	agentLabel := parsed.agentType
	if agentLabel == "" {
		agentLabel = parsed.command[0]
	}

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath, rawclient.WithTimeout(0))
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	// Register signal handlers BEFORE creating the sandbox so that any signal
	// received during creation or the READY wait still triggers cleanup.
	sigintCh := make(chan os.Signal, 2)
	sigtermCh := make(chan os.Signal, 1)
	signal.Notify(sigintCh, os.Interrupt)
	signal.Notify(sigtermCh, syscall.SIGTERM)
	defer signal.Stop(sigintCh)
	defer signal.Stop(sigtermCh)

	// idle_ttl=0 disables the idle-stop timer; the sandbox should live until the
	// session exits and we explicitly delete it.
	createResp, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		CreateSpec: &agboxv1.CreateSpec{
			Image:        defaultImage,
			BuiltinTools: parsed.builtinTools,
			Copies: []*agboxv1.CopySpec{
				{Source: parsed.workspace, Target: "/workspace"},
			},
			Labels: map[string]string{
				"created-by": "agbox-cli",
				"agent-type": agentLabel,
			},
			IdleTtl: durationpb.New(0),
		},
	})
	if err != nil {
		return runtimeErrorf("create sandbox: %v", err)
	}

	sandboxID := createResp.GetSandbox().GetSandboxId()
	lastEventSeq := createResp.GetSandbox().GetLastEventSequence()

	// deleteAndWait is deferred here so that cleanup happens even if we return
	// early from any error path below.
	defer func() {
		// Allow a second Ctrl+C during cleanup to force-exit the process.
		signal.Reset(syscall.SIGINT)
		_, _ = fmt.Fprintf(stderr, "Cleaning up sandbox %s...\n", sandboxID)
		deleteAndWait(client, sandboxID, stderr)
	}()

	// Check for a pre-exec signal before blocking on waitForSandboxReady.
	select {
	case <-sigintCh:
		return exitCodeError(130)
	case <-sigtermCh:
		return exitCodeError(143)
	default:
	}

	containerName := primaryContainerName(sandboxID)

	_, _ = fmt.Fprintf(stdout, "Waiting for sandbox %s to be ready...\n", sandboxID)
	_, _ = fmt.Fprintf(stdout, "Tip: open another shell into this sandbox with:\n  docker exec -it --user agbox %s bash\n", containerName)
	if err := waitForSandboxReady(ctx, client, sandboxID, lastEventSeq, sigintCh, sigtermCh); err != nil {
		return err
	}
	dockerArgs := append([]string{"exec", "-it", "--user", "agbox", containerName}, parsed.command...)
	cmd := exec.Command("docker", dockerArgs...) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return runtimeErrorf("start docker exec: %v", err)
	}

	// waitCh receives the result of cmd.Wait() so we can handle it alongside signals.
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	for {
		select {
		case <-sigintCh:
			// The TTY process group already delivered SIGINT to the container
			// process. We just need to wait for docker exec to finish naturally.
			waitErr := <-waitCh
			return exitCodeFromCmdErr(waitErr, 130)

		case <-sigtermCh:
			// Forward SIGTERM to the docker exec process (which forwards it to
			// the container). Wait up to sigtermGracePeriod, then escalate.
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case waitErr := <-waitCh:
				return exitCodeFromCmdErr(waitErr, 143)
			case <-time.After(sigtermGracePeriod):
				_ = cmd.Process.Kill()
				<-waitCh
				return exitCodeError(143)
			}

		case waitErr := <-waitCh:
			return exitCodeFromCmdErr(waitErr, 0)
		}
	}
}

// exitCodeFromCmdErr converts an exec.ExitError to an exitCodeError, preferring
// signalCode when the process was killed by a signal (exit code 0 in that case).
func exitCodeFromCmdErr(err error, signalCode int) error {
	if err == nil {
		if signalCode != 0 {
			return exitCodeError(signalCode)
		}
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		if code == -1 && signalCode != 0 {
			// Process was killed by a signal; use the signal-derived exit code.
			return exitCodeError(signalCode)
		}
		if code >= 0 {
			return exitCodeError(code)
		}
	}
	if signalCode != 0 {
		return exitCodeError(signalCode)
	}
	return runtimeErrorf("docker exec: %v", err)
}

// waitForSandboxReady blocks until the sandbox reaches READY state, a terminal
// state (FAILED/STOPPED/DELETED), or a signal is received.
func waitForSandboxReady(
	ctx context.Context,
	client sandboxExecClient,
	sandboxID string,
	lastEventSeq uint64,
	sigintCh <-chan os.Signal,
	sigtermCh <-chan os.Signal,
) error {
	// GetSandbox first to check if already READY or terminal, using the
	// returned lastEventSequence as the subscription cursor to close the
	// snapshot/subscription race window.
	getResp, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		return runtimeErrorf("get sandbox: %v", err)
	}
	sandbox := getResp.GetSandbox()
	switch sandbox.GetState() {
	case agboxv1.SandboxState_SANDBOX_STATE_READY:
		return nil
	case agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
		agboxv1.SandboxState_SANDBOX_STATE_DELETED:
		return runtimeErrorf("sandbox %s entered terminal state %s before READY", sandboxID, sandbox.GetState())
	}

	// Use the most recent sequence from GetSandbox as the subscription cursor.
	cursorSeq := sandbox.GetLastEventSequence()
	if cursorSeq < lastEventSeq {
		cursorSeq = lastEventSeq
	}

	stream, err := client.SubscribeSandboxEvents(ctx, sandboxID, cursorSeq, false)
	if err != nil {
		return runtimeErrorf("subscribe sandbox events: %v", err)
	}
	defer stream.Close()

	eventCh := make(chan sandboxEventResult, 1)
	go pumpSandboxEvents(stream, eventCh)

	for {
		select {
		case <-sigintCh:
			return exitCodeError(130)
		case <-sigtermCh:
			return exitCodeError(143)
		case result, ok := <-eventCh:
			if !ok {
				return runtimeErrorf("sandbox event stream closed unexpectedly")
			}
			if result.err != nil {
				return runtimeErrorf("wait for sandbox ready: %v", result.err)
			}
			switch result.event.GetSandboxState() {
			case agboxv1.SandboxState_SANDBOX_STATE_READY:
				return nil
			case agboxv1.SandboxState_SANDBOX_STATE_FAILED,
				agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
				agboxv1.SandboxState_SANDBOX_STATE_DELETED:
				return runtimeErrorf("sandbox %s reached state %s before READY", sandboxID, result.event.GetSandboxState())
			}
		case <-ctx.Done():
			return runtimeErrorf("wait for sandbox ready: %v", ctx.Err())
		}
	}
}

// deleteAndWait calls DeleteSandbox and waits for the sandbox to reach DELETED.
// Errors and timeouts are printed to stderr; the function does not return an error
// because it is called from a defer and any return value would be discarded.
func deleteAndWait(client sandboxExecClient, sandboxID string, stderr io.Writer) {
	// Use a background context so cleanup is not tied to the parent context,
	// which may already be cancelled.
	bgCtx := context.Background()

	_, deleteErr := client.DeleteSandbox(bgCtx, sandboxID)
	if deleteErr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: delete sandbox %s: %v\n", sandboxID, deleteErr)
		return
	}

	// Re-read the sandbox to get the authoritative cursor for subscribing to
	// further events. This closes the race between DeleteSandbox and Subscribe.
	getResp, err := client.GetSandbox(bgCtx, sandboxID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: get sandbox after delete %s: %v\n", sandboxID, err)
		return
	}
	sandbox := getResp.GetSandbox()
	switch sandbox.GetState() {
	case agboxv1.SandboxState_SANDBOX_STATE_DELETED:
		_, _ = fmt.Fprintf(stderr, "Sandbox %s cleaned up.\n", sandboxID)
		return
	case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
		_, _ = fmt.Fprintf(stderr, "warning: sandbox %s is in FAILED state after delete\n", sandboxID)
		return
	}

	stream, err := client.SubscribeSandboxEvents(bgCtx, sandboxID, sandbox.GetLastEventSequence(), false)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: subscribe sandbox events for cleanup %s: %v\n", sandboxID, err)
		return
	}
	defer stream.Close()

	eventCh := make(chan sandboxEventResult, 1)
	go pumpSandboxEvents(stream, eventCh)

	timeout := time.After(deleteAndWaitTimeout)
	for {
		select {
		case result, ok := <-eventCh:
			if !ok {
				_, _ = fmt.Fprintf(stderr, "warning: sandbox event stream closed before sandbox %s was deleted\n", sandboxID)
				return
			}
			if result.err != nil {
				_, _ = fmt.Fprintf(stderr, "warning: sandbox cleanup event error %s: %v\n", sandboxID, result.err)
				return
			}
			switch result.event.GetSandboxState() {
			case agboxv1.SandboxState_SANDBOX_STATE_DELETED:
				_, _ = fmt.Fprintf(stderr, "Sandbox %s cleaned up.\n", sandboxID)
				return
			case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
				_, _ = fmt.Fprintf(stderr, "warning: sandbox %s entered FAILED state during cleanup\n", sandboxID)
				return
			}
		case <-timeout:
			_, _ = fmt.Fprintf(stderr, "warning: timed out waiting for sandbox %s to be deleted\n", sandboxID)
			return
		}
	}
}

// sandboxEventResult carries a single event or error from the event stream pump goroutine.
type sandboxEventResult struct {
	event *agboxv1.SandboxEvent
	err   error
}

// pumpSandboxEvents reads events from stream and sends them to eventCh until
// the stream ends or an error is received.
func pumpSandboxEvents(stream rawclient.SandboxEventStream, eventCh chan<- sandboxEventResult) {
	defer close(eventCh)
	for {
		event, err := stream.Recv()
		if err != nil {
			eventCh <- sandboxEventResult{err: err}
			return
		}
		eventCh <- sandboxEventResult{event: event}
	}
}

// confirmWorkspaceCopy prompts the user to confirm copying a workspace that lacks
// a top-level .git directory. It reads a single line from stdin; only "y" or "Y"
// is accepted. Any other input (including empty/EOF) is treated as rejection.
func confirmWorkspaceCopy(stdin io.Reader, stderr io.Writer, path string) error {
	_, _ = fmt.Fprintf(stderr, "Warning: no .git directory found in %s. This directory will be copied into the sandbox.\nContinue? [y/N] ", path)
	var response string
	_, err := fmt.Fscanln(stdin, &response)
	if err != nil || (response != "y" && response != "Y") {
		return exitCodeError(1)
	}
	return nil
}
