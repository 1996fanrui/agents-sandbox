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
	// deleteAndWaitTimeout is the maximum time to wait for sandbox deletion to complete.
	deleteAndWaitTimeout = 30 * time.Second

	// sigtermGracePeriod is how long we wait for the docker exec child after forwarding SIGTERM
	// before escalating to SIGKILL.
	sigtermGracePeriod = 10 * time.Second

	// agentSessionIdleTTL is a safety-net idle TTL for interactive agent sessions.
	// The CLI always deletes the sandbox on exit, but if signal handling fails
	// (e.g., SIGKILL), the daemon will reclaim the sandbox after this duration.
	agentSessionIdleTTL = 10 * 24 * time.Hour
)

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

// loginShellProbeTimeout caps the wait for the entrypoint to finish creating
// the target user. SANDBOX_READY only means "container is running" — the
// runtime image's entrypoint may still be executing useradd/usermod/gosu.
// During that window, `docker exec --user agbox` fails because the user
// does not exist yet. Three seconds is generous: paseo-runtime entrypoint
// completes well under 1 s on a warm cache.
const loginShellProbeTimeout = 3 * time.Second

// resolveContainerLoginShell asks the container what login shell the target
// user is configured with by reading /etc/passwd. Returns the absolute path
// (e.g. "/bin/zsh", "/bin/bash"). The probe relies only on POSIX-mandatory
// components (sh, awk, /etc/passwd colon format).
//
// SANDBOX_READY only means the container is running; the runtime image's
// entrypoint may still be creating the agbox user. We loop on a single
// shell pipeline that (1) waits until the user exists, (2) reads its login
// shell. The wait happens inside the container, so we do not get exec 126
// from a missing user.
//
// Indirected through a package-level variable so unit tests that use fake
// runtimes (no real container) can stub it out.
var resolveContainerLoginShell = func(ctx context.Context, containerName, user string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, loginShellProbeTimeout)
	defer cancel()

	// Inner shell, taking $1 as the target username:
	//   1. Spin until /etc/passwd has an entry for that user (entrypoint's
	//      useradd may still be running).
	//   2. Print the 7th colon-separated field (login shell) for that user.
	probe := `u="$1"; while ! awk -F: -v u="$u" '$1==u {found=1; exit} END {exit !found}' /etc/passwd; do sleep 0.01; done; awk -F: -v u="$u" '$1==u {print $7; exit}' /etc/passwd`
	out, err := exec.CommandContext(probeCtx,
		"docker", "exec", containerName, "sh", "-c", probe, "_probe", user,
	).Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		if stderr != "" {
			return "", fmt.Errorf("probe login shell in %s: %w (%s)", containerName, err, stderr)
		}
		return "", fmt.Errorf("probe login shell in %s: %w", containerName, err)
	}
	shell := strings.TrimSpace(string(out))
	if shell == "" {
		return "", fmt.Errorf("probe login shell in %s: user %q has no shell entry in /etc/passwd", containerName, user)
	}
	return shell, nil
}

// waitReadyAndPrintShellHint waits for the sandbox to enter READY, prints
// the elapsed wait time, then prints a copy-pasteable docker exec command
// pointing at the user's actual login shell (resolved from the container's
// /etc/passwd, not assumed). Returns the resolved container name.
func waitReadyAndPrintShellHint(
	ctx context.Context,
	client sandboxExecClient,
	stderr io.Writer,
	sandboxID string,
	lastEventSeq uint64,
	sigintCh, sigtermCh <-chan os.Signal,
) (string, error) {
	containerName := primaryContainerName(sandboxID)
	_, _ = fmt.Fprintf(stderr, "Waiting for sandbox to be ready...")
	waitStart := time.Now()
	if err := waitForSandboxReady(ctx, client, sandboxID, lastEventSeq, sigintCh, sigtermCh); err != nil {
		_, _ = fmt.Fprintln(stderr)
		return "", err
	}
	_, _ = fmt.Fprintf(stderr, "     [%.1fs]\n", time.Since(waitStart).Seconds())

	loginShell, err := resolveContainerLoginShell(ctx, containerName, "agbox")
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(stderr, "  Shell: docker exec -it --user agbox %s %s\n", containerName, loginShell)
	return containerName, nil
}

// runAgentSession implements the shared flow for top-level per-type agent
// commands (`agbox claude`, `agbox codex`, `agbox openclaw`) and for
// `agbox agent --command "..."`. It validates inputs, connects to the
// daemon, and dispatches to the appropriate mode handler.
func runAgentSession(
	ctx context.Context,
	parsed agentSessionArgs,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) error {
	// Run pre-flight checks if the agent type defines them.
	if typeDef, ok := agentTypeDefs[parsed.agentType]; ok && typeDef.preFlight != nil {
		if err := typeDef.preFlight(stderr, &parsed); err != nil {
			return err
		}
	}

	// Workspace existence check (only when workspace is non-empty).
	if parsed.workspace != "" {
		if _, err := os.Stat(parsed.workspace); err != nil {
			return usageErrorf("--workspace path %q: %v", parsed.workspace, err)
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

	// Mode dispatch.
	switch parsed.mode {
	case agentModeLongRunning:
		return runLongRunningSession(ctx, client, parsed, agentLabel, stdout, stderr)
	default:
		return runInteractiveSession(ctx, client, parsed, agentLabel, stderr)
	}
}

// runInteractiveSession attaches an interactive TTY to the agent process inside
// a sandbox. The sandbox is deleted on exit.
func runInteractiveSession(
	ctx context.Context,
	client sandboxExecClient,
	parsed agentSessionArgs,
	agentLabel string,
	stderr io.Writer,
) error {
	// Require a real TTY on stdin. Passing -t to docker exec without a TTY causes
	// docker to exit immediately with an error, and the interactive experience
	// (echo, Ctrl+C, terminal size) would be broken anyway.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return usageErrorf("stdin is not a TTY; agbox agent requires an interactive terminal")
	}

	// Prompt for confirmation when the workspace lacks a top-level .git directory,
	// but only for registered agent types that declare confirmGit=true.
	if parsed.workspace != "" {
		if typeDef, ok := agentTypeDefs[parsed.agentType]; ok && typeDef.confirmGit {
			if _, err := os.Stat(filepath.Join(parsed.workspace, ".git")); os.IsNotExist(err) {
				if confirmErr := confirmWorkspaceCopy(os.Stdin, stderr, parsed.workspace); confirmErr != nil {
					return confirmErr
				}
			}
		}
	}

	// Register signal handlers BEFORE creating the sandbox so that any signal
	// received during creation or the READY wait still triggers cleanup.
	// SIGHUP is included because terminal closure sends SIGHUP, and Go's
	// default SIGHUP handler terminates without running deferred cleanup.
	sigintCh := make(chan os.Signal, 2)
	sigtermCh := make(chan os.Signal, 1)
	signal.Notify(sigintCh, os.Interrupt)
	signal.Notify(sigtermCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigintCh)
	defer signal.Stop(sigtermCh)

	// Use a large idle TTL as a safety net: the CLI always cleans up on exit,
	// but if the process is killed without cleanup (e.g., SIGKILL), the daemon
	// will reclaim the sandbox after agentSessionIdleTTL.
	createResp, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		SandboxId:  parsed.sandboxID,
		ConfigYaml: configYamlBytes(parsed.configYaml),
		CreateSpec: buildAgentCreateSpec(parsed, agentLabel, durationpb.New(agentSessionIdleTTL), nil),
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
		// Move the cursor to the bottom of the terminal (past any TUI remnants)
		// before writing cleanup messages. \033[9999B moves down as far as
		// possible, landing on the last occupied line, then \n starts a fresh line.
		_, _ = fmt.Fprintf(stderr, "\033[9999B\nCleaning up sandbox %s...\n", sandboxID)
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

	containerName, err := waitReadyAndPrintShellHint(ctx, client, stderr, sandboxID, lastEventSeq, sigintCh, sigtermCh)
	if err != nil {
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

// runLongRunningSession creates the sandbox, waits for it to become READY,
// then detaches. The sandbox must be managed manually via
// `agbox sandbox stop/delete`.
func runLongRunningSession(
	ctx context.Context,
	client sandboxExecClient,
	parsed agentSessionArgs,
	agentLabel string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	// Register signal handlers before creating the sandbox.
	sigintCh := make(chan os.Signal, 2)
	sigtermCh := make(chan os.Signal, 1)
	signal.Notify(sigintCh, os.Interrupt)
	signal.Notify(sigtermCh, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigintCh)
	defer signal.Stop(sigtermCh)

	// idle_ttl=0 disables idle stop; the sandbox stays alive until explicit stop/delete.
	createResp, err := client.CreateSandbox(ctx, &agboxv1.CreateSandboxRequest{
		SandboxId:  parsed.sandboxID,
		ConfigYaml: configYamlBytes(parsed.configYaml),
		CreateSpec: buildAgentCreateSpec(parsed, agentLabel, durationpb.New(0), parsed.command),
	})
	if err != nil {
		return runtimeErrorf("create sandbox: %v", err)
	}

	sandboxID := createResp.GetSandbox().GetSandboxId()
	lastEventSeq := createResp.GetSandbox().GetLastEventSequence()

	// Track whether exec was successfully delivered. If delivery fails,
	// the deferred cleanup deletes the sandbox to avoid orphans.
	detachSuccess := false
	defer func() {
		if !detachSuccess {
			_, _ = fmt.Fprintf(stderr, "\nCleaning up sandbox %s...\n", sandboxID)
			deleteAndWait(client, sandboxID, stderr)
		}
	}()

	// Check for a pre-exec signal before blocking on waitForSandboxReady.
	select {
	case <-sigintCh:
		return exitCodeError(130)
	case <-sigtermCh:
		return exitCodeError(143)
	default:
	}

	containerName, err := waitReadyAndPrintShellHint(ctx, client, stderr, sandboxID, lastEventSeq, sigintCh, sigtermCh)
	if err != nil {
		return err
	}

	// After READY, container is running. Set detachSuccess so deferred cleanup
	// does not delete the sandbox. Any error beyond this point leaves the
	// sandbox alive so the user can still interact with it (e.g. via
	// `agbox paseo url`).
	detachSuccess = true

	// For paseo, fetch the pair URL now and inject it into the ready message.
	if parsed.agentType == "paseo" {
		pairURL, err := fetchPaseoPairURL(ctx, client, sandboxID, stderr)
		if err != nil {
			return err
		}
		parsed.readyMessage = paseoReadyMessageFactory(parsed.builtinTools, pairURL)
	}

	// Print readyMessage if defined.
	if parsed.readyMessage != nil {
		_, _ = fmt.Fprint(stderr, parsed.readyMessage(sandboxID, containerName))
	}

	// stdout: sandbox_id for programmatic consumption.
	_, _ = fmt.Fprintln(stdout, sandboxID)
	return nil
}

// waitForExecDone blocks until the exec reaches a terminal state.
// Returns nil on FINISHED with exit_code=0, error otherwise.
func waitForExecDone(ctx context.Context, client sandboxExecClient, execID, sandboxID string) error {
	getExecResp, err := client.GetExec(ctx, execID)
	if err != nil {
		return runtimeErrorf("get exec: %v", err)
	}
	baseline, err := requireExecStatus(getExecResp, execID)
	if err != nil {
		return runtimeErrorf("get exec: %v", err)
	}

	if isTerminalExecState(baseline.GetState()) {
		return execDoneResult(baseline)
	}

	stream, err := client.SubscribeSandboxEvents(ctx, sandboxID, baseline.GetLastEventSequence(), false)
	if err != nil {
		return runtimeErrorf("subscribe sandbox events: %v", err)
	}
	defer stream.Close()

	eventCh := make(chan execEventResult, 1)
	go pumpExecEvents(stream, eventCh)

	for {
		select {
		case result, ok := <-eventCh:
			if !ok {
				return runtimeErrorf("exec event stream closed; check with: agbox exec get %s", execID)
			}
			if result.err != nil {
				return runtimeErrorf("wait exec events: %v", result.err)
			}
			currentResp, err := client.GetExec(ctx, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			current, err := requireExecStatus(currentResp, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			if isTerminalExecState(current.GetState()) {
				return execDoneResult(current)
			}
		case <-ctx.Done():
			return runtimeErrorf("wait exec: %v", ctx.Err())
		}
	}
}

// execDoneResult returns nil for successful exec, error otherwise.
func execDoneResult(status *agboxv1.ExecStatus) error {
	switch status.GetState() {
	case agboxv1.ExecState_EXEC_STATE_FINISHED:
		if status.GetExitCode() == 0 {
			return nil
		}
		return runtimeErrorf("exec finished with exit_code=%d", status.GetExitCode())
	case agboxv1.ExecState_EXEC_STATE_FAILED:
		msg := fmt.Sprintf("exec failed (exit_code=%d)", status.GetExitCode())
		if status.GetError() != "" {
			msg += ": " + status.GetError()
		}
		return runtimeErrorf("%s", msg)
	case agboxv1.ExecState_EXEC_STATE_CANCELLED:
		return runtimeErrorf("exec cancelled")
	default:
		return runtimeErrorf("exec in unexpected state: %v", status.GetState())
	}
}

// configYamlBytes converts a config YAML string to bytes, returning nil for empty strings.
func configYamlBytes(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
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
	case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
		return runtimeErrorf("sandbox %s failed: %s", sandboxID, sandboxErrorDetail(sandbox))
	case agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
		agboxv1.SandboxState_SANDBOX_STATE_DELETED:
		return runtimeErrorf("sandbox %s entered %s state before ready", sandboxID, humanStateName(sandbox.GetState()))
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
			case agboxv1.SandboxState_SANDBOX_STATE_FAILED:
				return sandboxFailedError(ctx, client, sandboxID)
			case agboxv1.SandboxState_SANDBOX_STATE_STOPPED,
				agboxv1.SandboxState_SANDBOX_STATE_DELETED:
				return runtimeErrorf("sandbox %s entered %s state before ready", sandboxID, humanStateName(result.event.GetSandboxState()))
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
		_, _ = fmt.Fprintf(stderr, "warning: sandbox %s failed to clean up\n", sandboxID)
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
				_, _ = fmt.Fprintf(stderr, "warning: sandbox %s failed during cleanup\n", sandboxID)
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

// buildAgentCreateSpec assembles the CreateSpec sent to the daemon for an
// agent session. Layout rules:
//   - Copies: workspace first (when present), then user --copy entries.
//   - Mounts/Ports: user --mount/--port entries appended; preset YAML mounts
//     and ports are merged in by the daemon (base + override append).
//   - Labels: built-in labels (created-by, agent-type) written first, then
//     user --label entries overlay (last write wins via Go map semantics).
//   - command: caller-controlled — interactive sessions pass nil so the
//     primary command from the preset YAML is used; long-running sessions
//     pass parsed.command to set the container primary command.
func buildAgentCreateSpec(parsed agentSessionArgs, agentLabel string, idleTTL *durationpb.Duration, command []string) *agboxv1.CreateSpec {
	copies := []*agboxv1.CopySpec{}
	if parsed.workspace != "" {
		copies = append(copies, &agboxv1.CopySpec{Source: parsed.workspace, Target: "/workspace"})
	}
	copies = append(copies, parsed.userCopies...)

	labels := map[string]string{
		"created-by": "agbox-cli",
		"agent-type": agentLabel,
	}
	for k, v := range parsed.userLabels {
		labels[k] = v
	}

	return &agboxv1.CreateSpec{
		Image:        parsed.image,
		Command:      command,
		BuiltinTools: parsed.builtinTools,
		Mounts:       parsed.userMounts,
		Copies:       copies,
		Ports:        parsed.userPorts,
		Envs:         parsed.envs,
		CpuLimit:     parsed.cpuLimit,
		MemoryLimit:  parsed.memoryLimit,
		DiskLimit:    parsed.diskLimit,
		Gpus:         parsed.gpus,
		Labels:       labels,
		IdleTtl:      idleTTL,
	}
}

// confirmWorkspaceCopy prompts the user to confirm uploading a non-Git directory
// as the sandbox workspace. It reads a single line from stdin; only "y" or "Y"
// is accepted. Any other input (including empty/EOF) is treated as rejection.
func confirmWorkspaceCopy(stdin io.Reader, stderr io.Writer, path string) error {
	_, _ = fmt.Fprintf(stderr, "%s is not a Git repository. It will be copied into the sandbox as your workspace.\nProceed? [y/N] ", path)
	var response string
	_, err := fmt.Fscanln(stdin, &response)
	if err != nil || (response != "y" && response != "Y") {
		return exitCodeError(1)
	}
	return nil
}
