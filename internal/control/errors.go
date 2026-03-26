package control

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	ReasonSandboxConflict             = "SANDBOX_CONFLICT"
	ReasonSandboxIDAlreadyExists      = "SANDBOX_ID_ALREADY_EXISTS"
	ReasonSandboxNotFound             = "SANDBOX_NOT_FOUND"
	ReasonSandboxNotReady             = "SANDBOX_NOT_READY"
	ReasonSandboxInvalidState         = "SANDBOX_INVALID_STATE"
	ReasonSandboxRecoveredOnly        = "SANDBOX_RECOVERED_ONLY"
	ReasonExecIDAlreadyExists         = "EXEC_ID_ALREADY_EXISTS"
	ReasonExecNotFound                = "EXEC_NOT_FOUND"
	ReasonExecAlreadyTerminal         = "EXEC_ALREADY_TERMINAL"
	ReasonSandboxEventSequenceExpired = "SANDBOX_EVENT_SEQUENCE_EXPIRED"
)

func newStatusError(code codes.Code, reason string, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	st := status.New(code, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}
