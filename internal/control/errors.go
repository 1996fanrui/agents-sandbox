package control

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorDomain is the domain string used in all ErrorInfo details emitted by this service.
const ErrorDomain = "agents-sandbox"

const (
	ReasonSandboxConflict             = "SANDBOX_CONFLICT"
	ReasonSandboxIDAlreadyExists      = "SANDBOX_ID_ALREADY_EXISTS"
	ReasonSandboxNotFound             = "SANDBOX_NOT_FOUND"
	ReasonSandboxNotReady             = "SANDBOX_NOT_READY"
	ReasonSandboxInvalidState         = "SANDBOX_INVALID_STATE"
	ReasonExecIDAlreadyExists         = "EXEC_ID_ALREADY_EXISTS"
	ReasonExecNotFound                = "EXEC_NOT_FOUND"
	ReasonExecAlreadyTerminal         = "EXEC_ALREADY_TERMINAL"
	ReasonExecNotRunning              = "EXEC_NOT_RUNNING"
	ReasonSandboxEventSequenceExpired = "SANDBOX_EVENT_SEQUENCE_EXPIRED"
)

// newStatusError creates a gRPC status error enriched with ErrorInfo details
// including the service domain, a machine-readable reason code, and optional
// structured metadata (e.g. sandbox_id, exec_id).
func newStatusError(code codes.Code, reason string, metadata map[string]string, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	st := status.New(code, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   ErrorDomain,
		Metadata: metadata,
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}
