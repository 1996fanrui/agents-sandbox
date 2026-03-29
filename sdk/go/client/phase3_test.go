package client

import (
	"context"
	"errors"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

func TestListActiveExecsOptionPattern(t *testing.T) {
	t.Parallel()

	t.Run("no_options_passes_empty_sandbox_id", func(t *testing.T) {
		var capturedSandboxID string
		base := &fakeRPCClient{}
		base.listActiveExecsFn = func(_ context.Context, sandboxID string) (*agboxv1.ListActiveExecsResponse, error) {
			capturedSandboxID = sandboxID
			return &agboxv1.ListActiveExecsResponse{
				Execs: []*agboxv1.ExecStatus{
					{ExecId: "exec-1", SandboxId: "sandbox-a", State: agboxv1.ExecState_EXEC_STATE_RUNNING, LastEventSequence: 1},
					{ExecId: "exec-2", SandboxId: "sandbox-b", State: agboxv1.ExecState_EXEC_STATE_RUNNING, LastEventSequence: 2},
				},
			}, nil
		}
		client := newTestClient(base, nil)
		execs, err := client.ListActiveExecs(context.Background())
		if err != nil {
			t.Fatalf("ListActiveExecs failed: %v", err)
		}
		if capturedSandboxID != "" {
			t.Fatalf("expected empty sandbox_id for unfiltered list, got %q", capturedSandboxID)
		}
		if len(execs) != 2 {
			t.Fatalf("expected 2 execs, got %d", len(execs))
		}
	})

	t.Run("with_sandbox_id_filters", func(t *testing.T) {
		var capturedSandboxID string
		base := &fakeRPCClient{}
		base.listActiveExecsFn = func(_ context.Context, sandboxID string) (*agboxv1.ListActiveExecsResponse, error) {
			capturedSandboxID = sandboxID
			return &agboxv1.ListActiveExecsResponse{
				Execs: []*agboxv1.ExecStatus{
					{ExecId: "exec-1", SandboxId: sandboxID, State: agboxv1.ExecState_EXEC_STATE_RUNNING, LastEventSequence: 1},
				},
			}, nil
		}
		client := newTestClient(base, nil)
		execs, err := client.ListActiveExecs(context.Background(), WithSandboxID("sandbox-abc"))
		if err != nil {
			t.Fatalf("ListActiveExecs with sandbox filter failed: %v", err)
		}
		if capturedSandboxID != "sandbox-abc" {
			t.Fatalf("expected sandbox_id %q, got %q", "sandbox-abc", capturedSandboxID)
		}
		if len(execs) != 1 {
			t.Fatalf("expected 1 exec, got %d", len(execs))
		}
	})
}

func TestErrorTypeAliasesInClientPackage(t *testing.T) {
	t.Parallel()

	// Verify that errors created in rawclient can be matched via client package type aliases.
	rawErr := rawclient.NewExecNotRunningError("exec-1", nil)

	var notRunning *ExecNotRunningError
	if !errors.As(rawErr, &notRunning) {
		t.Fatalf("expected ExecNotRunningError via client alias, got %T", rawErr)
	}
	if notRunning.ExecID != "exec-1" {
		t.Fatalf("unexpected exec id: %q", notRunning.ExecID)
	}

	// SandboxClientError is accessible as a base type via errors.As since
	// ExecNotRunningError wraps it through SandboxInvalidStateError embedding.
	// errors.As traverses Unwrap() not embedding, so only the concrete type
	// check on the outermost error applies here. Verify the error message directly.
	if rawErr.Error() != "Exec exec-1 is not running." {
		t.Fatalf("unexpected error message: %q", rawErr.Error())
	}
}
