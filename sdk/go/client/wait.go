package client

import (
	"context"
	"fmt"
	"time"

	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

func (c *Client) waitForSandboxState(
	ctx context.Context,
	sandboxID string,
	baseline SandboxHandle,
	targetState SandboxState,
	operationName string,
) (SandboxHandle, error) {
	if baseline.State == targetState {
		return baseline, nil
	}
	if err := raiseForFailedSandbox(baseline, operationName); err != nil {
		return SandboxHandle{}, err
	}

	baselineSequence, err := parseCursorSequence(sandboxID, baseline.LastEventCursor)
	if err != nil {
		return SandboxHandle{}, err
	}
	streamCursor := baseline.LastEventCursor
	if streamCursor == "" {
		streamCursor = "0"
	}

	waitCtx, cancel := c.withOperationTimeout(ctx)
	defer cancel()

	for item := range c.SubscribeSandboxEvents(waitCtx, sandboxID, WithFromCursor(streamCursor)) {
		if item.Err != nil {
			return SandboxHandle{}, item.Err
		}
		if item.Event == nil || item.Event.Sequence <= baselineSequence {
			continue
		}
		current, getErr := c.GetSandbox(waitCtx, sandboxID)
		if getErr != nil {
			return SandboxHandle{}, getErr
		}
		if current.State == targetState {
			return current, nil
		}
		if err := raiseForFailedSandbox(current, operationName); err != nil {
			return SandboxHandle{}, err
		}
	}

	if err := waitCtx.Err(); err != nil {
		return SandboxHandle{}, fmt.Errorf("%s timed out while waiting for sandbox %s to reach %v: %w", operationName, sandboxID, targetState, err)
	}
	return SandboxHandle{}, rawclient.NewSandboxClientError(
		fmt.Sprintf("%s ended before sandbox %s reached %v", operationName, sandboxID, targetState),
		nil,
	)
}

func (c *Client) waitForExecTerminal(
	ctx context.Context,
	execID string,
	sandboxID string,
	baseline ExecHandle,
	operationName string,
) (ExecHandle, error) {
	if baseline.State.IsTerminal() {
		return baseline, nil
	}

	sandboxBaseline, err := c.GetSandbox(ctx, sandboxID)
	if err != nil {
		return ExecHandle{}, err
	}
	baselineSequence, err := parseCursorSequence(sandboxID, sandboxBaseline.LastEventCursor)
	if err != nil {
		return ExecHandle{}, err
	}
	streamCursor := sandboxBaseline.LastEventCursor
	if streamCursor == "" {
		streamCursor = "0"
	}

	waitCtx, cancel := c.withOperationTimeout(ctx)
	defer cancel()

	events := c.SubscribeSandboxEvents(waitCtx, sandboxID, WithFromCursor(streamCursor))
	pollTicker := time.NewTicker(c.execPollInterval)
	defer pollTicker.Stop()

	for {
		current, getErr := c.GetExec(waitCtx, execID)
		if getErr != nil {
			return ExecHandle{}, getErr
		}
		if current.State.IsTerminal() {
			return current, nil
		}

		select {
		case <-waitCtx.Done():
			return ExecHandle{}, fmt.Errorf("%s timed out while waiting for exec %s to become terminal: %w", operationName, execID, waitCtx.Err())
		case item, ok := <-events:
			if !ok {
				return ExecHandle{}, rawclient.NewSandboxClientError(
					fmt.Sprintf("%s event stream ended before exec %s reached a terminal state", operationName, execID),
					nil,
				)
			}
			if item.Err != nil {
				return ExecHandle{}, item.Err
			}
			if item.Event == nil || item.Event.Sequence <= baselineSequence {
				continue
			}
		case <-pollTicker.C:
		}
	}
}

func (c *Client) withOperationTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.operationTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.operationTimeout)
}

func raiseForFailedSandbox(handle SandboxHandle, operationName string) error {
	if handle.State == SandboxStateFailed {
		return rawclient.NewSandboxClientError(
			fmt.Sprintf("%s observed sandbox %s in FAILED state", operationName, handle.SandboxID),
			nil,
		)
	}
	return nil
}
