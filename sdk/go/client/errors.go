package client

import "github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"

// Type aliases so callers can use errors.As without importing rawclient directly.
type SandboxClientError = rawclient.SandboxClientError
type SandboxConflictError = rawclient.SandboxConflictError
type SandboxNotFoundError = rawclient.SandboxNotFoundError
type SandboxNotReadyError = rawclient.SandboxNotReadyError
type SandboxInvalidStateError = rawclient.SandboxInvalidStateError
type ExecNotFoundError = rawclient.ExecNotFoundError
type ExecAlreadyTerminalError = rawclient.ExecAlreadyTerminalError
type ExecNotRunningError = rawclient.ExecNotRunningError
type SandboxSequenceExpiredError = rawclient.SandboxSequenceExpiredError
