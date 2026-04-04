package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc"
)

func TestExecGet(t *testing.T) {
	service := &fakeSandboxService{
		getExecFn: func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			if request.GetExecId() != "exec-abc" {
				t.Fatalf("unexpected exec id: %q", request.GetExecId())
			}
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    "exec-abc",
					SandboxId: "sandbox-123",
					State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:   []string{"python", "-c", "print(1)"},
					Cwd:       "/workspace",
				},
			}, nil
		},
	}

	// Text output
	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "get", "exec-abc")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	for _, want := range []string{"exec_id=exec-abc", "sandbox_id=sandbox-123", "state=Running", "command=python -c print(1)", "cwd=/workspace"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("text output missing %q: %q", want, stdout)
		}
	}
	// exit_code should NOT appear for non-terminal state
	if strings.Contains(stdout, "exit_code=") {
		t.Fatalf("exit_code should not appear for running exec: %q", stdout)
	}
}

func TestExecGetTerminal(t *testing.T) {
	service := &fakeSandboxService{
		getExecFn: func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    "exec-done",
					SandboxId: "sandbox-123",
					State:     agboxv1.ExecState_EXEC_STATE_FINISHED,
					Command:   []string{"echo", "hello"},
					ExitCode:  42,
				},
			}, nil
		},
	}

	stdout, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "get", "exec-done")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stdout, "exit_code=42") {
		t.Fatalf("exit_code should appear for terminal exec: %q", stdout)
	}
}

func TestExecGetJSON(t *testing.T) {
	service := &fakeSandboxService{
		getExecFn: func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:    "exec-abc",
					SandboxId: "sandbox-123",
					State:     agboxv1.ExecState_EXEC_STATE_RUNNING,
					Command:   []string{"ls"},
				},
			}, nil
		},
	}

	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "get", "exec-abc", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	exec, ok := payload["exec"].(map[string]any)
	if !ok {
		t.Fatalf("expected exec key in JSON: %#v", payload)
	}
	if exec["exec_id"] != "exec-abc" {
		t.Fatalf("unexpected exec_id: %#v", exec["exec_id"])
	}
}

func TestExecCancel(t *testing.T) {
	cancelCalled := false
	service := &fakeSandboxService{
		cancelExecFn: func(_ context.Context, request *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			if request.GetExecId() != "exec-abc" {
				t.Fatalf("unexpected exec id: %q", request.GetExecId())
			}
			cancelCalled = true
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getExecFn: func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			return execResponse("exec-abc", "sandbox-123", agboxv1.ExecState_EXEC_STATE_CANCELLED, 10, 0), nil
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "cancel", "exec-abc")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !cancelCalled {
		t.Fatal("CancelExec was not called")
	}
}

func TestExecCancelAlreadyTerminal(t *testing.T) {
	service := &fakeSandboxService{
		cancelExecFn: func(_ context.Context, _ *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			return nil, execAlreadyTerminalError("exec exec-abc is already terminal")
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "cancel", "exec-abc")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
}

func TestExecCancelWithWait(t *testing.T) {
	getCalls := 0
	service := &fakeSandboxService{
		cancelExecFn: func(_ context.Context, _ *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		},
		getExecFn: func(_ context.Context, _ *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			getCalls++
			switch getCalls {
			case 1:
				// After cancel: still running, need to wait
				return execResponse("exec-abc", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 10, 0), nil
			default:
				// After event: reached terminal
				return execResponse("exec-abc", "sandbox-123", agboxv1.ExecState_EXEC_STATE_CANCELLED, 11, 0), nil
			}
		},
		subscribeFn: func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
			return stream.Send(&agboxv1.SandboxEvent{
				EventId: "event-1", Sequence: 11, SandboxId: "sandbox-123",
				Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-abc"}},
			})
		},
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "cancel", "exec-abc")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "Waiting for exec exec-abc to be cancelled...") {
		t.Fatalf("expected wait message in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Exec cancelled in") {
		t.Fatalf("expected cancelled message in stderr, got %q", stderr)
	}
}

func TestExecList(t *testing.T) {
	service := &fakeSandboxService{
		listActiveExecsFn: func(_ context.Context, request *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
			return &agboxv1.ListActiveExecsResponse{
				Execs: []*agboxv1.ExecStatus{
					{ExecId: "exec-1", SandboxId: "sandbox-a", State: agboxv1.ExecState_EXEC_STATE_RUNNING, Command: []string{"python", "main.py"}},
					{ExecId: "exec-2", SandboxId: "sandbox-b", State: agboxv1.ExecState_EXEC_STATE_RUNNING, Command: []string{"npm", "start"}},
				},
			}, nil
		},
	}

	// Without sandbox_id filter
	stdout, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "list")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "EXEC ID") || !strings.Contains(stdout, "SANDBOX ID") || !strings.Contains(stdout, "STATE") || !strings.Contains(stdout, "COMMAND") {
		t.Fatalf("unexpected header: %q", stdout)
	}
	if !strings.Contains(stdout, "exec-1") || !strings.Contains(stdout, "exec-2") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	if !strings.Contains(stdout, "python main.py") {
		t.Fatalf("command not shown as shell-style: %q", stdout)
	}
	if service.listActiveExecsReq.GetSandboxId() != "" {
		t.Fatalf("expected empty sandbox_id filter, got %q", service.listActiveExecsReq.GetSandboxId())
	}
}

func TestExecListWithSandboxFilter(t *testing.T) {
	service := &fakeSandboxService{
		listActiveExecsFn: func(_ context.Context, request *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
			if request.GetSandboxId() != "sandbox-a" {
				t.Fatalf("unexpected sandbox_id filter: %q", request.GetSandboxId())
			}
			return &agboxv1.ListActiveExecsResponse{
				Execs: []*agboxv1.ExecStatus{
					{ExecId: "exec-1", SandboxId: "sandbox-a", State: agboxv1.ExecState_EXEC_STATE_RUNNING, Command: []string{"ls"}},
				},
			}, nil
		},
	}

	stdout, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "list", "sandbox-a")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stdout, "exec-1") {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
}

func TestExecListJSON(t *testing.T) {
	service := &fakeSandboxService{
		listActiveExecsFn: func(_ context.Context, _ *agboxv1.ListActiveExecsRequest) (*agboxv1.ListActiveExecsResponse, error) {
			return &agboxv1.ListActiveExecsResponse{
				Execs: []*agboxv1.ExecStatus{
					{ExecId: "exec-1", SandboxId: "sandbox-a", State: agboxv1.ExecState_EXEC_STATE_RUNNING, Command: []string{"ls"}},
				},
			}, nil
		},
	}

	stdout, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "list", "--json")
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	var payload struct {
		Execs []map[string]any `json:"execs"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if len(payload.Execs) != 1 {
		t.Fatalf("unexpected execs count: %d", len(payload.Execs))
	}
}
