package client

import (
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestToSandboxHandleWithError(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ts := timestamppb.New(now)

	// Case 1: FAILED sandbox with error fields populated.
	handle, err := toSandboxHandle(&agboxv1.SandboxHandle{
		SandboxId:      "sb-1",
		State:          agboxv1.SandboxState_SANDBOX_STATE_FAILED,
		ErrorCode:      "CONTAINER_NOT_RUNNING",
		ErrorMessage:   "primary container not running",
		StateChangedAt: ts,
		CreatedAt:      ts,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handle.ErrorCode == nil || *handle.ErrorCode != "CONTAINER_NOT_RUNNING" {
		t.Fatalf("expected ErrorCode=CONTAINER_NOT_RUNNING, got %v", handle.ErrorCode)
	}
	if handle.ErrorMessage == nil || *handle.ErrorMessage != "primary container not running" {
		t.Fatalf("expected ErrorMessage set, got %v", handle.ErrorMessage)
	}
	if handle.StateChangedAt == nil || !handle.StateChangedAt.Equal(now) {
		t.Fatalf("expected StateChangedAt=%v, got %v", now, handle.StateChangedAt)
	}

	// Case 2: READY sandbox with no error fields.
	handle2, err := toSandboxHandle(&agboxv1.SandboxHandle{
		SandboxId: "sb-2",
		State:     agboxv1.SandboxState_SANDBOX_STATE_READY,
		CreatedAt: ts,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handle2.ErrorCode != nil {
		t.Fatalf("expected nil ErrorCode, got %v", handle2.ErrorCode)
	}
	if handle2.ErrorMessage != nil {
		t.Fatalf("expected nil ErrorMessage, got %v", handle2.ErrorMessage)
	}
	if handle2.StateChangedAt != nil {
		t.Fatalf("expected nil StateChangedAt, got %v", handle2.StateChangedAt)
	}
}
