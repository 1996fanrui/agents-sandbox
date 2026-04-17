package control

import (
	"context"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// assertErrorInfoField checks that the gRPC error carries an ErrorInfo detail with
// the given domain, reason, and metadata key/value pair.
func assertErrorInfoField(t *testing.T, err error, wantDomain, wantReason, metaKey, metaVal string) {
	t.Helper()

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok {
			continue
		}
		if info.GetDomain() != wantDomain {
			t.Fatalf("unexpected domain: got %q want %q", info.GetDomain(), wantDomain)
		}
		if info.GetReason() != wantReason {
			t.Fatalf("unexpected reason: got %q want %q", info.GetReason(), wantReason)
		}
		if metaKey != "" {
			if got := info.GetMetadata()[metaKey]; got != metaVal {
				t.Fatalf("unexpected metadata[%q]: got %q want %q", metaKey, got, metaVal)
			}
		}
		return
	}
	t.Fatalf("no ErrorInfo detail found in error: %v", err)
}

func TestErrorMetadataEnrichment(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	const sandboxID = "meta-test-sandbox"

	// GetSandbox on a non-existent sandbox: expects sandbox_id in metadata.
	_, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: sandboxID})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	assertErrorInfoField(t, err, ErrorDomain, ReasonSandboxNotFound, "sandbox_id", sandboxID)

	// Create sandbox and wait for READY.
	_, createErr := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  sandboxID,
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if createErr != nil {
		t.Fatalf("CreateSandbox failed: %v", createErr)
	}
	waitForSandboxState(t, client, sandboxID, agboxv1.SandboxState_SANDBOX_STATE_READY)

	// Duplicate sandbox creation: expects SANDBOX_ID_ALREADY_EXISTS + sandbox_id metadata.
	_, err = client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  sandboxID,
		CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", err)
	}
	assertErrorInfoField(t, err, ErrorDomain, ReasonSandboxIDAlreadyExists, "sandbox_id", sandboxID)

	// GetExec on non-existent exec: expects exec_id in metadata.
	const execID = "nonexistent-exec-id"
	_, err = client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execID})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	assertErrorInfoField(t, err, ErrorDomain, ReasonExecNotFound, "exec_id", execID)

	// StopSandbox on non-ready sandbox (it's already READY, stop it first to make it stopped, then try ResumeSandbox on READY).
	_, err = client.ResumeSandbox(context.Background(), &agboxv1.ResumeSandboxRequest{SandboxId: sandboxID})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for resume on ready, got %v", err)
	}
	assertErrorInfoField(t, err, ErrorDomain, ReasonSandboxInvalidState, "sandbox_id", sandboxID)
}

func TestSandboxHandleCreatedAtAndImage(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})

	const image = "test-image:v1"
	before := time.Now().Add(-time.Second)

	resp, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId:  "handle-fields-test",
		CreateSpec: &agboxv1.CreateSpec{Image: image},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}

	handle := resp.GetSandbox()
	if handle.GetImage() != image {
		t.Fatalf("expected image %q, got %q", image, handle.GetImage())
	}
	if handle.GetCreatedAt() == nil {
		t.Fatal("expected created_at to be set, got nil")
	}
	createdAt := handle.GetCreatedAt().AsTime()
	if createdAt.Before(before) || createdAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("created_at %v is out of expected range", createdAt)
	}

	// Verify GetSandbox also returns the same fields.
	getResp, err := client.GetSandbox(context.Background(), &agboxv1.GetSandboxRequest{SandboxId: "handle-fields-test"})
	if err != nil {
		t.Fatalf("GetSandbox failed: %v", err)
	}
	if getResp.GetSandbox().GetImage() != image {
		t.Fatalf("GetSandbox: expected image %q, got %q", image, getResp.GetSandbox().GetImage())
	}
	if !proto.Equal(getResp.GetSandbox().GetCreatedAt(), handle.GetCreatedAt()) {
		t.Fatalf("GetSandbox: created_at mismatch: %v vs %v", getResp.GetSandbox().GetCreatedAt(), handle.GetCreatedAt())
	}
}

// blockingExecBackend blocks RunExec until the unblock channel is closed.
type blockingExecBackend struct {
	*fakeRuntimeBackend
	unblock chan struct{}
}

func (b *blockingExecBackend) RunExec(ctx context.Context, record *sandboxRecord, exec *agboxv1.ExecStatus) (runtimeExecResult, error) {
	select {
	case <-b.unblock:
	case <-ctx.Done():
	}
	return runtimeExecResult{ExitCode: 0}, nil
}

func TestListActiveExecsOptionalSandboxID(t *testing.T) {
	unblock := make(chan struct{})
	backend := &blockingExecBackend{fakeRuntimeBackend: &fakeRuntimeBackend{}, unblock: unblock}
	t.Cleanup(func() {
		// Ensure execs are unblocked when the test ends.
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	})

	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  backend,
	})

	// Create two sandboxes and start a blocking exec in each.
	for _, id := range []string{"list-exec-sandbox-a", "list-exec-sandbox-b"} {
		_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
			SandboxId:  id,
			CreateSpec: &agboxv1.CreateSpec{Image: "test:latest"},
		})
		if err != nil {
			t.Fatalf("CreateSandbox %s failed: %v", id, err)
		}
		waitForSandboxState(t, client, id, agboxv1.SandboxState_SANDBOX_STATE_READY)
		_, err = client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
			SandboxId: id,
			ExecId:    id + "-exec",
			Command:   []string{"echo", "running"},
		})
		if err != nil {
			t.Fatalf("CreateExec for %s failed: %v", id, err)
		}
	}

	// Without filter: both execs should appear as active (they are blocked).
	respAll, err := client.ListActiveExecs(context.Background(), &agboxv1.ListActiveExecsRequest{})
	if err != nil {
		t.Fatalf("ListActiveExecs (no filter) failed: %v", err)
	}
	if len(respAll.GetExecs()) < 2 {
		t.Fatalf("expected at least 2 active execs without filter, got %d", len(respAll.GetExecs()))
	}

	// With sandbox_id filter: only the exec for that sandbox.
	filterID := "list-exec-sandbox-a"
	respFiltered, err := client.ListActiveExecs(context.Background(), &agboxv1.ListActiveExecsRequest{
		SandboxId: proto.String(filterID),
	})
	if err != nil {
		t.Fatalf("ListActiveExecs (with filter) failed: %v", err)
	}
	for _, exec := range respFiltered.GetExecs() {
		if exec.GetSandboxId() != filterID {
			t.Fatalf("expected all execs in sandbox %q, got exec with sandbox_id %q", filterID, exec.GetSandboxId())
		}
	}
	if len(respFiltered.GetExecs()) != 1 {
		t.Fatalf("expected exactly 1 exec for sandbox %q, got %d", filterID, len(respFiltered.GetExecs()))
	}

	// Unblock to let execs finish cleanly.
	close(unblock)
}
