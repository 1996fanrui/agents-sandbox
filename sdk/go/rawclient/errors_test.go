package rawclient

import (
	"errors"
	"testing"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newStatusErrorWithMetadata(t *testing.T, reason string, message string, metadata map[string]string) error {
	t.Helper()
	st := status.New(codes.Unknown, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("status.WithDetails failed: %v", err)
	}
	return withDetails.Err()
}

func TestErrorTranslationMetadataExtraction(t *testing.T) {
	t.Parallel()

	t.Run("sandbox_not_found_extracts_sandbox_id_from_metadata", func(t *testing.T) {
		err := translateRPCError(newStatusErrorWithMetadata(t, control.ReasonSandboxNotFound, "sandbox not found",
			map[string]string{"sandbox_id": "sbx-xyz"},
		))
		var notFound *SandboxNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("expected SandboxNotFoundError, got %T", err)
		}
		if notFound.SandboxID != "sbx-xyz" {
			t.Fatalf("expected sandbox_id %q, got %q", "sbx-xyz", notFound.SandboxID)
		}
		// Message is rewritten to canonical form when sandbox_id is present.
		if notFound.Error() != "Sandbox sbx-xyz not found." {
			t.Fatalf("unexpected message: %q", notFound.Error())
		}
	})

	t.Run("exec_not_found_extracts_exec_id_from_metadata", func(t *testing.T) {
		err := translateRPCError(newStatusErrorWithMetadata(t, control.ReasonExecNotFound, "exec not found",
			map[string]string{"exec_id": "exec-abc"},
		))
		var notFound *ExecNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("expected ExecNotFoundError, got %T", err)
		}
		if notFound.ExecID != "exec-abc" {
			t.Fatalf("expected exec_id %q, got %q", "exec-abc", notFound.ExecID)
		}
		if notFound.Error() != "Exec exec-abc not found." {
			t.Fatalf("unexpected message: %q", notFound.Error())
		}
	})

	t.Run("exec_already_terminal_extracts_exec_id_from_metadata", func(t *testing.T) {
		err := translateRPCError(newStatusErrorWithMetadata(t, control.ReasonExecAlreadyTerminal, "exec is terminal",
			map[string]string{"exec_id": "exec-done"},
		))
		var terminal *ExecAlreadyTerminalError
		if !errors.As(err, &terminal) {
			t.Fatalf("expected ExecAlreadyTerminalError, got %T", err)
		}
		if terminal.ExecID != "exec-done" {
			t.Fatalf("expected exec_id %q, got %q", "exec-done", terminal.ExecID)
		}
		if terminal.Error() != "Exec exec-done is already terminal." {
			t.Fatalf("unexpected message: %q", terminal.Error())
		}
	})

	t.Run("exec_not_running_extracts_exec_id_from_metadata", func(t *testing.T) {
		err := translateRPCError(newStatusErrorWithMetadata(t, control.ReasonExecNotRunning, "exec not running",
			map[string]string{"exec_id": "exec-paused"},
		))
		var notRunning *ExecNotRunningError
		if !errors.As(err, &notRunning) {
			t.Fatalf("expected ExecNotRunningError, got %T", err)
		}
		if notRunning.ExecID != "exec-paused" {
			t.Fatalf("expected exec_id %q, got %q", "exec-paused", notRunning.ExecID)
		}
		if notRunning.Error() != "Exec exec-paused is not running." {
			t.Fatalf("unexpected message: %q", notRunning.Error())
		}
	})

	t.Run("no_metadata_keeps_empty_resource_ids", func(t *testing.T) {
		err := translateRPCError(newStatusError(t, control.ReasonSandboxNotFound, "sandbox missing"))
		var notFound *SandboxNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("expected SandboxNotFoundError, got %T", err)
		}
		if notFound.SandboxID != "" {
			t.Fatalf("expected empty sandbox_id when no metadata, got %q", notFound.SandboxID)
		}
		// Message is preserved unchanged when no sandbox_id in metadata.
		if notFound.Error() != "sandbox missing" {
			t.Fatalf("unexpected message: %q", notFound.Error())
		}
	})

	t.Run("sequence_expired_with_oldest_sequence", func(t *testing.T) {
		err := translateRPCError(
			newStatusErrorWithMetadata(
				t,
				control.ReasonSandboxEventSequenceExpired,
				"sandbox sandbox-beta event sequence 10 is outside retained history; oldest retained sequence is 50",
				map[string]string{
					"sandbox_id":      "sandbox-beta",
					"from_sequence":   "10",
					"oldest_sequence": "50",
				},
			),
		)
		var expired *SandboxSequenceExpiredError
		if !errors.As(err, &expired) {
			t.Fatalf("expected SandboxSequenceExpiredError, got %T", err)
		}
		if expired.SandboxID != "sandbox-beta" {
			t.Fatalf("unexpected sandbox id: %q", expired.SandboxID)
		}
		if expired.FromSequence == nil || *expired.FromSequence != 10 {
			t.Fatalf("unexpected from_sequence: %#v", expired.FromSequence)
		}
		if expired.OldestSequence == nil || *expired.OldestSequence != 50 {
			t.Fatalf("unexpected oldest_sequence: %#v", expired.OldestSequence)
		}
	})
}
