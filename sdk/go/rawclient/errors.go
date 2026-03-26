package rawclient

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

var sandboxCursorExpiredPattern = regexp.MustCompile(`^Sandbox (\S+) event cursor (\d+) expired; oldest retained sequence is (\d+)\.?$`)

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

func newSandboxConflictError(message string, cause error) *SandboxConflictError {
	sandboxID, idProvided := idFromMessage(message)
	if !idProvided {
		if message == "" {
			message = "Sandbox already exists."
		}
	}
	return &SandboxConflictError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		SandboxID:          sandboxID,
	}
}

// SandboxNotFoundError is raised when a sandbox is missing.
type SandboxNotFoundError struct {
	*SandboxClientError
	SandboxID string
}

func newSandboxNotFoundError(message string, cause error) *SandboxNotFoundError {
	sandboxID, idProvided := idFromMessage(message)
	if idProvided {
		message = fmt.Sprintf("Sandbox %s not found.", sandboxID)
	} else if message == "" {
		message = "RPC failed."
	}
	return &SandboxNotFoundError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		SandboxID:          sandboxID,
	}
}

// SandboxNotReadyError is raised when a sandbox cannot accept exec yet.
type SandboxNotReadyError struct {
	*SandboxClientError
	SandboxID string
}

func newSandboxNotReadyError(message string, cause error) *SandboxNotReadyError {
	sandboxID, idProvided := idFromMessage(message)
	if idProvided {
		message = fmt.Sprintf("Sandbox %s is not ready.", sandboxID)
	} else if message == "" {
		message = "RPC failed."
	}
	return &SandboxNotReadyError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		SandboxID:          sandboxID,
	}
}

// SandboxInvalidStateError is raised when an operation is invalid for current state.
type SandboxInvalidStateError struct {
	*SandboxClientError
}

func newSandboxInvalidStateError(message string, cause error) *SandboxInvalidStateError {
	if message == "" {
		message = "RPC failed."
	}
	return &SandboxInvalidStateError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
	}
}

// ExecNotFoundError is raised when an exec record is missing.
type ExecNotFoundError struct {
	*SandboxClientError
	ExecID string
}

func newExecNotFoundError(message string, cause error) *ExecNotFoundError {
	execID, idProvided := idFromMessage(message)
	if idProvided {
		message = fmt.Sprintf("Exec %s not found.", execID)
	} else if message == "" {
		message = "RPC failed."
	}
	return &ExecNotFoundError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		ExecID:             execID,
	}
}

// ExecAlreadyTerminalError is raised when an exec is already terminal.
type ExecAlreadyTerminalError struct {
	*SandboxClientError
	ExecID string
}

func newExecAlreadyTerminalError(message string, cause error) *ExecAlreadyTerminalError {
	execID, idProvided := idFromMessage(message)
	if idProvided {
		message = fmt.Sprintf("Exec %s is already terminal.", execID)
	} else if message == "" {
		message = "RPC failed."
	}
	return &ExecAlreadyTerminalError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		ExecID:             execID,
	}
}

// ExecNotRunningError can be composed from SandboxInvalidStateError by higher layers.
type ExecNotRunningError struct {
	*SandboxInvalidStateError
	ExecID string
}

func newExecNotRunningError(message string, cause error) *ExecNotRunningError {
	execID, idProvided := idFromMessage(message)
	if idProvided {
		message = fmt.Sprintf("Exec %s is not running.", execID)
	} else if message == "" {
		message = "RPC failed."
	}
	return &ExecNotRunningError{
		SandboxInvalidStateError: newSandboxInvalidStateError(message, cause),
		ExecID:                   execID,
	}
}

// SandboxCursorExpiredError is raised when a subscription cursor is too old.
type SandboxCursorExpiredError struct {
	*SandboxClientError
	SandboxID      string
	FromSequence   *uint64
	OldestSequence *uint64
}

func newSandboxCursorExpiredError(message string, cause error) *SandboxCursorExpiredError {
	sandboxID, fromSeq, oldestSeq := parseCursorExpiredMessage(message)
	if sandboxID == "" {
		id, hasID := idFromMessage(message)
		if hasID {
			sandboxID = id
		}
	}

	if message == "" {
		message = "RPC failed."
	}

	return &SandboxCursorExpiredError{
		SandboxClientError: &SandboxClientError{message: message, cause: cause},
		SandboxID:          sandboxID,
		FromSequence:       fromSeq,
		OldestSequence:     oldestSeq,
	}
}

func translateRPCError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return &SandboxClientError{message: err.Error(), cause: err}
	}

	reason := ""
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			reason = info.GetReason()
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

	switch reason {
	case control.ReasonSandboxConflict, control.ReasonSandboxIDAlreadyExists, control.ReasonExecIDAlreadyExists:
		return newSandboxConflictError(message, err)
	case control.ReasonSandboxNotFound:
		return newSandboxNotFoundError(message, err)
	case control.ReasonSandboxNotReady:
		return newSandboxNotReadyError(message, err)
	case control.ReasonSandboxInvalidState:
		return newSandboxInvalidStateError(message, err)
	case control.ReasonExecNotFound:
		return newExecNotFoundError(message, err)
	case control.ReasonExecAlreadyTerminal:
		return newExecAlreadyTerminalError(message, err)
	case control.ReasonSandboxEventCursorExpired:
		return newSandboxCursorExpiredError(message, err)
	default:
		return &SandboxClientError{message: message, cause: err}
	}
}

func parseCursorExpiredMessage(message string) (string, *uint64, *uint64) {
	matches := sandboxCursorExpiredPattern.FindStringSubmatch(message)
	if len(matches) != 4 {
		return "", nil, nil
	}
	sandboxID := matches[1]
	fromSequence, err := strconv.ParseUint(matches[2], 10, 64)
	if err != nil {
		return sandboxID, nil, nil
	}
	oldestSequence, err := strconv.ParseUint(matches[3], 10, 64)
	if err != nil {
		return sandboxID, &fromSequence, nil
	}

	return sandboxID, &fromSequence, &oldestSequence
}

func idFromMessage(message string) (string, bool) {
	if message == "" || strings.Contains(message, " ") {
		return "", false
	}
	return message, true
}
