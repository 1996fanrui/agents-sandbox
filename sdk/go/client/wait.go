package client

import (
	"context"
	"fmt"

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

	baselineSequence := baseline.LastEventSequence

	waitCtx, cancel := c.withOperationTimeout(ctx)
	defer cancel()

	for item := range c.SubscribeSandboxEvents(waitCtx, sandboxID, WithFromSequence(baselineSequence)) {
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
	baseline execSnapshot,
	operationName string,
) (ExecHandle, error) {
	if baseline.handle.State.IsTerminal() {
		return baseline.handle, nil
	}

	baselineSequence := baseline.lastEventSequence

	waitCtx, cancel := c.withOperationTimeout(ctx)
	defer cancel()

	for item := range c.SubscribeSandboxEvents(waitCtx, sandboxID, WithFromSequence(baselineSequence)) {
		if item.Err != nil {
			return ExecHandle{}, item.Err
		}
		if item.Event == nil || item.Event.Sequence <= baselineSequence {
			continue
		}
		if item.Event.ExecID == nil || *item.Event.ExecID != execID {
			continue
		}
		current, getErr := c.getExecSnapshot(waitCtx, execID)
		if getErr != nil {
			return ExecHandle{}, getErr
		}
		if current.handle.State.IsTerminal() {
			return current.handle, nil
		}
	}

	if err := waitCtx.Err(); err != nil {
		return ExecHandle{}, fmt.Errorf("%s timed out while waiting for exec %s to become terminal: %w", operationName, execID, waitCtx.Err())
	}
	return ExecHandle{}, rawclient.NewSandboxClientError(
		fmt.Sprintf("%s event stream ended before exec %s reached a terminal state", operationName, execID),
		nil,
	)
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
