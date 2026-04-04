package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

// execReadClient is the interface for exec point-in-time read operations.
type execReadClient interface {
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	ListActiveExecs(context.Context, string) (*agboxv1.ListActiveExecsResponse, error)
}

// execCancelClient is the interface for exec cancel and wait operations.
type execCancelClient interface {
	CancelExec(context.Context, string) (*agboxv1.AcceptedResponse, error)
	GetExec(context.Context, string) (*agboxv1.GetExecResponse, error)
	SubscribeSandboxEvents(context.Context, string, uint64, bool) (rawclient.SandboxEventStream, error)
}

func runExecGet(ctx context.Context, client execReadClient, execID string, jsonOutput bool, stdout io.Writer) error {
	response, err := client.GetExec(ctx, execID)
	if err != nil {
		return runtimeErrorf("get exec: %v", err)
	}

	execStatus, err := requireExecStatus(response, execID)
	if err != nil {
		return runtimeErrorf("get exec: %v", err)
	}

	if jsonOutput {
		data, err := formatExecGetResponse(response)
		if err != nil {
			return runtimeErrorf("format get exec response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprint(stdout, formatExecStatusText(execStatus))
	return nil
}

func runExecCancel(ctx context.Context, client execCancelClient, execID string, stderr io.Writer) error {
	_, err := client.CancelExec(ctx, execID)
	if err != nil {
		var alreadyTerminal *rawclient.ExecAlreadyTerminalError
		if errors.As(err, &alreadyTerminal) {
			// Exec already reached terminal state — treat as success.
			return nil
		}
		return runtimeErrorf("cancel exec: %v", err)
	}

	// Get exec to find sandboxID for event subscription and check current state.
	getResp, err := client.GetExec(ctx, execID)
	if err != nil {
		return runtimeErrorf("get exec after cancel: %v", err)
	}
	execStatus, err := requireExecStatus(getResp, execID)
	if err != nil {
		return runtimeErrorf("get exec after cancel: %v", err)
	}

	if isTerminalExecState(execStatus.GetState()) {
		return nil
	}

	_, _ = fmt.Fprintf(stderr, "Waiting for exec %s to be cancelled...", execID)
	waitStart := time.Now()
	if err := waitForExecTerminal(ctx, client, execID, execStatus.GetSandboxId(), execStatus.GetLastEventSequence()); err != nil {
		_, _ = fmt.Fprintln(stderr)
		return err
	}
	_, _ = fmt.Fprintf(stderr, "\nExec cancelled in %.1fs.\n", time.Since(waitStart).Seconds())
	return nil
}

func runExecList(ctx context.Context, client execReadClient, sandboxID string, jsonOutput bool, stdout io.Writer) error {
	response, err := client.ListActiveExecs(ctx, sandboxID)
	if err != nil {
		return runtimeErrorf("list active execs: %v", err)
	}

	if jsonOutput {
		data, err := formatExecListResponse(response)
		if err != nil {
			return runtimeErrorf("format list active execs response: %v", err)
		}
		_, _ = fmt.Fprintln(stdout, data)
		return nil
	}

	_, _ = fmt.Fprint(stdout, formatExecListTable(response.GetExecs()))
	return nil
}

// waitForExecTerminal waits for an exec to reach a terminal state (FINISHED, FAILED, CANCELLED).
func waitForExecTerminal(ctx context.Context, client execCancelClient, execID string, sandboxID string, fromSequence uint64) error {
	stream, err := client.SubscribeSandboxEvents(ctx, sandboxID, fromSequence, false)
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
				return runtimeErrorf("exec event stream closed unexpectedly")
			}
			if result.err != nil {
				return runtimeErrorf("wait for exec terminal: %v", result.err)
			}
			getResp, err := client.GetExec(ctx, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			execStatus, err := requireExecStatus(getResp, execID)
			if err != nil {
				return runtimeErrorf("get exec: %v", err)
			}
			if isTerminalExecState(execStatus.GetState()) {
				return nil
			}
		case <-ctx.Done():
			return runtimeErrorf("wait for exec terminal: %v", ctx.Err())
		}
	}
}
