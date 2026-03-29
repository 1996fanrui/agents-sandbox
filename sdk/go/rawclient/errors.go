package rawclient

import (
	"fmt"
	"strconv"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

// SandboxClientError is the base rawclient error type.
type SandboxClientError struct {
	message string
	cause   error
}

// Error returns the client-facing message.
func (e *SandboxClientError) Error() string {
	return e.message
}

// Unwrap returns the original root error.
func (e *SandboxClientError) Unwrap() error {
	return e.cause
}

// SandboxConflictError is raised when an identifier already exists.
type SandboxConflictError struct {
	*SandboxClientError
	SandboxID string
}

// SandboxNotFoundError is raised when a sandbox is missing.
type SandboxNotFoundError struct {
	*SandboxClientError
	SandboxID string
}

// SandboxNotReadyError is raised when a sandbox cannot accept exec yet.
type SandboxNotReadyError struct {
	*SandboxClientError
	SandboxID string
}

// SandboxInvalidStateError is raised when an operation is invalid for current state.
type SandboxInvalidStateError struct {
	*SandboxClientError
}

// ExecNotFoundError is raised when an exec record is missing.
type ExecNotFoundError struct {
	*SandboxClientError
	ExecID string
}

// ExecAlreadyTerminalError is raised when an exec is already terminal.
type ExecAlreadyTerminalError struct {
	*SandboxClientError
	ExecID string
}

// ExecNotRunningError can be composed from SandboxInvalidStateError by higher layers.
type ExecNotRunningError struct {
	*SandboxInvalidStateError
	ExecID string
}

// NewSandboxClientError constructs a base SDK error from a message and optional cause.
func NewSandboxClientError(message string, cause error) *SandboxClientError {
	if message == "" {
		message = "RPC failed."
	}
	return &SandboxClientError{message: message, cause: cause}
}

// NewExecNotRunningError constructs the canonical high-level exec-not-running error.
func NewExecNotRunningError(execID string, cause error) *ExecNotRunningError {
	return &ExecNotRunningError{
		SandboxInvalidStateError: &SandboxInvalidStateError{
			SandboxClientError: &SandboxClientError{
				message: fmt.Sprintf("Exec %s is not running.", execID),
				cause:   cause,
			},
		},
		ExecID: execID,
	}
}

// SandboxSequenceExpiredError is raised when a subscription start sequence is outside retained history.
type SandboxSequenceExpiredError struct {
	*SandboxClientError
	SandboxID      string
	FromSequence   *uint64
	OldestSequence *uint64
}

func translateRPCError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return &SandboxClientError{message: err.Error(), cause: err}
	}

	reason := ""
	metadata := map[string]string(nil)
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			reason = info.GetReason()
			metadata = info.GetMetadata()
			break
		}
	}

	message := st.Message()
	if message == "" {
		if reason != "" {
			message = reason
		} else {
			message = "RPC failed"
		}
	}

	// Extract resource IDs from structured metadata instead of parsing message text.
	sandboxID := metadata["sandbox_id"]
	execID := metadata["exec_id"]

	switch reason {
	case control.ReasonSandboxConflict, control.ReasonSandboxIDAlreadyExists, control.ReasonExecIDAlreadyExists:
		return &SandboxConflictError{
			SandboxClientError: &SandboxClientError{message: message, cause: err},
			SandboxID:          sandboxID,
		}
	case control.ReasonSandboxNotFound:
		notFoundMessage := message
		if sandboxID != "" {
			notFoundMessage = fmt.Sprintf("Sandbox %s not found.", sandboxID)
		}
		return &SandboxNotFoundError{
			SandboxClientError: &SandboxClientError{message: notFoundMessage, cause: err},
			SandboxID:          sandboxID,
		}
	case control.ReasonSandboxNotReady:
		notReadyMessage := message
		if sandboxID != "" {
			notReadyMessage = fmt.Sprintf("Sandbox %s is not ready.", sandboxID)
		}
		return &SandboxNotReadyError{
			SandboxClientError: &SandboxClientError{message: notReadyMessage, cause: err},
			SandboxID:          sandboxID,
		}
	case control.ReasonSandboxInvalidState:
		return &SandboxInvalidStateError{
			SandboxClientError: &SandboxClientError{message: message, cause: err},
		}
	case control.ReasonExecNotFound:
		notFoundMessage := message
		if execID != "" {
			notFoundMessage = fmt.Sprintf("Exec %s not found.", execID)
		}
		return &ExecNotFoundError{
			SandboxClientError: &SandboxClientError{message: notFoundMessage, cause: err},
			ExecID:             execID,
		}
	case control.ReasonExecAlreadyTerminal:
		terminalMessage := message
		if execID != "" {
			terminalMessage = fmt.Sprintf("Exec %s is already terminal.", execID)
		}
		return &ExecAlreadyTerminalError{
			SandboxClientError: &SandboxClientError{message: terminalMessage, cause: err},
			ExecID:             execID,
		}
	case control.ReasonExecNotRunning:
		notRunningMessage := message
		if execID != "" {
			notRunningMessage = fmt.Sprintf("Exec %s is not running.", execID)
		}
		return &ExecNotRunningError{
			SandboxInvalidStateError: &SandboxInvalidStateError{
				SandboxClientError: &SandboxClientError{message: notRunningMessage, cause: err},
			},
			ExecID: execID,
		}
	case control.ReasonSandboxEventSequenceExpired:
		var fromSeq *uint64
		var oldestSeq *uint64
		if s, ok := metadata["from_sequence"]; ok && s != "" {
			if v, parseErr := strconv.ParseUint(s, 10, 64); parseErr == nil {
				fromSeq = &v
			}
		}
		if s, ok := metadata["oldest_sequence"]; ok && s != "" {
			if v, parseErr := strconv.ParseUint(s, 10, 64); parseErr == nil {
				oldestSeq = &v
			}
		}
		return &SandboxSequenceExpiredError{
			SandboxClientError: &SandboxClientError{message: message, cause: err},
			SandboxID:          sandboxID,
			FromSequence:       fromSeq,
			OldestSequence:     oldestSeq,
		}
	default:
		return &SandboxClientError{message: message, cause: err}
	}
}
