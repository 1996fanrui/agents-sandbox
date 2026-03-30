package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"github.com/spf13/cobra"
)

const execCancelTimeout = time.Second

// runSandboxExec is the entry point called from the cobra command. It sets up
// signal handling and delegates to runSandboxExecWithSignals.
func runSandboxExec(ctx context.Context, cmd *cobra.Command, parsed sandboxExecArgs) error {
	lookupEnv := lookupEnvFromCmd(cmd)

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath, rawclient.WithTimeout(0))
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	signalCh := make(chan os.Signal, 2)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	return runSandboxExecWithSignals(ctx, client, parsed, signalCh)
}

func runSandboxExecWithSignals(
	ctx context.Context,
	client sandboxExecClient,
	parsed sandboxExecArgs,
	signalCh <-chan os.Signal,
) error {
	request := &agboxv1.CreateExecRequest{
		SandboxId:    parsed.sandboxID,
		Command:      parsed.command,
		EnvOverrides: parsed.envOverrides,
	}
	if parsed.cwd != "" {
		request.Cwd = parsed.cwd
	}

	createResponse, err := client.CreateExec(ctx, request)
	if err != nil {
		return runtimeErrorf("create exec: %v", err)
	}

	execID := createResponse.GetExecId()
	baselineResponse, err := client.GetExec(ctx, execID)
	if err != nil {
		return runtimeErrorf("get exec baseline: %v", err)
	}
	baseline, err := requireExecStatus(baselineResponse, execID)
	if err != nil {
		return runtimeErrorf("get exec baseline: %v", err)
	}

	if isTerminalExecState(baseline.GetState()) {
		if signalCode, ok := peekExecSignal(signalCh); ok {
			cancelErr := cancelExec(client, execID)
			if cancelErr != nil {
				var alreadyTerminal *rawclient.ExecAlreadyTerminalError
				if !errors.As(cancelErr, &alreadyTerminal) {
					return runtimeErrorf("cancel exec: %v", cancelErr)
				}
			} else {
				return exitCodeError(signalCode)
			}
		}
		return exitCodeError(execExitCode(baseline.GetState(), baseline.GetExitCode(), 0))
	}

	stream, err := client.SubscribeSandboxEvents(ctx, baseline.GetSandboxId(), baseline.GetLastEventSequence(), false)
	if err != nil {
		return runtimeErrorf("subscribe sandbox events: %v", err)
	}
	defer stream.Close()

	eventCh := make(chan execEventResult, 1)
	go pumpExecEvents(stream, eventCh)

	signalCode := 0
	signalOverridesExitCode := false
	signalChVar := signalCh

	for {
		select {
		case sig := <-signalChVar:
			if sig == nil {
				signalChVar = nil
				continue
			}
			signalChVar = nil
			signalCode = exitCodeForSignal(sig)
			cancelErr := cancelExec(client, execID)
			if cancelErr != nil {
				var alreadyTerminal *rawclient.ExecAlreadyTerminalError
				if !errors.As(cancelErr, &alreadyTerminal) {
					return runtimeErrorf("cancel exec: %v", cancelErr)
				}
				continue
			}
			signalOverridesExitCode = true
		case result, ok := <-eventCh:
			if !ok {
				return runtimeErrorf("exec event stream ended unexpectedly")
			}
			if result.err != nil {
				return runtimeErrorf("wait exec events: %v", result.err)
			}
			currentResponse, err := client.GetExec(ctx, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			current, err := requireExecStatus(currentResponse, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			if isTerminalExecState(current.GetState()) {
				if signalOverridesExitCode {
					return exitCodeError(signalCode)
				}
				return exitCodeError(execExitCode(current.GetState(), current.GetExitCode(), 0))
			}
		case <-ctx.Done():
			return runtimeErrorf("wait exec: %v", ctx.Err())
		}
	}
}

type execEventResult struct {
	event *agboxv1.SandboxEvent
	err   error
}

func pumpExecEvents(stream rawclient.SandboxEventStream, eventCh chan<- execEventResult) {
	defer close(eventCh)
	for {
		event, err := stream.Recv()
		if err != nil {
			eventCh <- execEventResult{err: err}
			return
		}
		eventCh <- execEventResult{event: event}
	}
}

func cancelExec(client sandboxExecClient, execID string) error {
	cancelCtx, cancel := context.WithTimeout(context.Background(), execCancelTimeout)
	defer cancel()

	_, err := client.CancelExec(cancelCtx, execID)
	return err
}

func requireExecStatus(response *agboxv1.GetExecResponse, execID string) (*agboxv1.ExecStatus, error) {
	if response == nil || response.GetExec() == nil {
		return nil, fmt.Errorf("exec %s is missing from GetExec response", execID)
	}
	return response.GetExec(), nil
}

func peekExecSignal(signalCh <-chan os.Signal) (int, bool) {
	if signalCh == nil {
		return 0, false
	}
	select {
	case sig := <-signalCh:
		if sig == nil {
			return 0, false
		}
		return exitCodeForSignal(sig), true
	default:
		return 0, false
	}
}

func exitCodeForSignal(sig os.Signal) int {
	switch sig {
	case os.Interrupt:
		return 130
	case syscall.SIGTERM:
		return 143
	default:
		return 125
	}
}

func isTerminalExecState(state agboxv1.ExecState) bool {
	switch state {
	case agboxv1.ExecState_EXEC_STATE_FINISHED, agboxv1.ExecState_EXEC_STATE_FAILED, agboxv1.ExecState_EXEC_STATE_CANCELLED:
		return true
	default:
		return false
	}
}

func execExitCode(state agboxv1.ExecState, exitCode int32, signalCode int) int {
	if signalCode != 0 {
		return signalCode
	}
	switch state {
	case agboxv1.ExecState_EXEC_STATE_FINISHED:
		return int(exitCode)
	case agboxv1.ExecState_EXEC_STATE_FAILED:
		if exitCode != 0 {
			return int(exitCode)
		}
		return 125
	default:
		return 125
	}
}
