package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSandboxExecExitCode(t *testing.T) {
	service := &fakeSandboxService{}
	calls := make(chan string, 4)
	getCalls := 0

	service.createExecFn = func(ctx context.Context, request *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
		calls <- "create"
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("create exec should not carry a deadline")
		}
		if request.GetSandboxId() != "sandbox-123" {
			t.Fatalf("unexpected sandbox id: %q", request.GetSandboxId())
		}
		if got := request.GetCwd(); got != "/workspace" {
			t.Fatalf("unexpected cwd: %q", got)
		}
		if got := execEnvPairs(request.GetEnvOverrides()); got != "PATH=/usr/bin,TEAM=platform" {
			t.Fatalf("unexpected env overrides: %s", got)
		}
		if got := strings.Join(request.GetCommand(), " "); got != "python -c print(42)" {
			t.Fatalf("unexpected command: %q", got)
		}
		return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
	}
	service.getExecFn = func(ctx context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("get exec should not carry a deadline")
		}
		getCalls++
		if request.GetExecId() != "exec-1" {
			t.Fatalf("unexpected exec id: %q", request.GetExecId())
		}
		switch getCalls {
		case 1:
			calls <- "baseline"
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 11, 0), nil
		case 2:
			calls <- "final"
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_FINISHED, 12, 7), nil
		default:
			t.Fatalf("unexpected GetExec call %d", getCalls)
			return nil, nil
		}
	}
	service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
		calls <- "subscribe"
		if _, ok := stream.Context().Deadline(); ok {
			t.Fatal("subscribe sandbox events should not carry a deadline")
		}
		if request.GetSandboxId() != "sandbox-123" {
			t.Fatalf("unexpected subscribe sandbox id: %q", request.GetSandboxId())
		}
		if request.GetFromSequence() != 11 {
			t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
		}
		if request.GetIncludeCurrentSnapshot() {
			t.Fatal("include_current_snapshot should be false")
		}
		return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 12, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
	}

	_, _, exitCode := runCLIWithSandboxServer(
		t,
		service,
		"exec",
		"run",
		"sandbox-123",
		"--cwd", "/workspace",
		"--env-overrides", "PATH=/usr/bin",
		"--env-overrides", "TEAM=platform",
		"--",
		"python",
		"-c",
		"print(42)",
	)
	if exitCode != 7 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	for _, want := range []string{"create", "baseline", "subscribe", "final"} {
		got := <-calls
		if got != want {
			t.Fatalf("unexpected call order: got %q want %q", got, want)
		}
	}
}

func TestSandboxExecPropagatesFailedExitCode(t *testing.T) {
	service := &fakeSandboxService{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		},
	}
	getCalls := 0
	service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
		getCalls++
		switch getCalls {
		case 1:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 3, 0), nil
		case 2:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_FAILED, 4, 9), nil
		default:
			t.Fatalf("unexpected GetExec call %d", getCalls)
			return nil, nil
		}
	}
	service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
		if request.GetFromSequence() != 3 {
			t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
		}
		return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 4, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
	}

	_, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "run", "sandbox-123", "--", "false")
	if exitCode != 9 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func TestSandboxExecReturns125ForFailedZeroExitCode(t *testing.T) {
	service := &fakeSandboxService{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		},
	}
	getCalls := 0
	service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
		getCalls++
		switch getCalls {
		case 1:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 5, 0), nil
		case 2:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_FAILED, 6, 0), nil
		default:
			t.Fatalf("unexpected GetExec call %d", getCalls)
			return nil, nil
		}
	}
	service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
		if request.GetFromSequence() != 5 {
			t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
		}
		return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 6, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
	}

	_, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "run", "sandbox-123", "--", "false")
	if exitCode != 125 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func TestSandboxExecReturns125ForCancelledWithoutLocalSignal(t *testing.T) {
	service := &fakeSandboxService{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		},
	}
	getCalls := 0
	service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
		getCalls++
		switch getCalls {
		case 1:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 8, 0), nil
		case 2:
			return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_CANCELLED, 9, 0), nil
		default:
			t.Fatalf("unexpected GetExec call %d", getCalls)
			return nil, nil
		}
	}
	service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
		if request.GetFromSequence() != 8 {
			t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
		}
		return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 9, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
	}

	_, _, exitCode := runCLIWithSandboxServer(t, service, "exec", "run", "sandbox-123", "--", "false")
	if exitCode != 125 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func TestSandboxExecReturnsRuntimeErrorOnSubscribeFailure(t *testing.T) {
	service := &fakeSandboxService{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		},
		subscribeErr: status.Error(codes.Unavailable, "stream broken"),
	}
	service.getExecFn = func(_ context.Context, _ *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
		return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 10, 0), nil
	}

	_, stderr, exitCode := runCLIWithSandboxServer(t, service, "exec", "run", "sandbox-123", "--", "false")
	if exitCode != exitCodeRuntimeError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "wait exec events") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecRejectsMissingSeparator(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "exec", "run", "sandbox-123", "--cwd", "/workspace", "python")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "requires -- <command> [args...]") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecRejectsEmptyCommandAfterSeparator(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "exec", "run", "sandbox-123", "--")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "requires <sandbox_id> -- <command> [args...]") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecRejectsBadEnvAssignment(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "exec", "run", "sandbox-123", "--env-overrides", "BAD", "--", "python")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "--env-overrides") || !strings.Contains(stderr, "=") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecRejectsDeprecatedEnvFlag(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "exec", "run", "sandbox-123", "--env", "KEY=value", "--", "python")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "unknown flag: --env") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecRejectsJSON(t *testing.T) {
	_, stderr, exitCode := runCLIWithSandboxServer(t, &fakeSandboxService{}, "exec", "run", "sandbox-123", "--json", "--", "python")
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr, "unknown flag: --json") {
		t.Fatalf("unexpected stderr %q", stderr)
	}
}

func TestSandboxExecLocalInterruptReturns130(t *testing.T) {
	exitCode := runSandboxExecSignalTest(t, os.Interrupt, func(service *fakeSandboxService, subscribeReady chan struct{}, cancelSeen chan struct{}, releaseEvents chan struct{}) {
		service.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		getCalls := 0
		service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			getCalls++
			switch getCalls {
			case 1:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 21, 0), nil
			case 2:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_CANCELLED, 22, 0), nil
			default:
				t.Fatalf("unexpected GetExec call %d", getCalls)
				return nil, nil
			}
		}
		service.cancelExecFn = func(_ context.Context, request *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			if request.GetExecId() != "exec-1" {
				t.Fatalf("unexpected cancel exec id: %q", request.GetExecId())
			}
			close(cancelSeen)
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
			if request.GetFromSequence() != 21 {
				t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
			}
			close(subscribeReady)
			<-releaseEvents
			return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 22, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
		}
	})
	if exitCode != 130 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func TestSandboxExecLocalTerminateReturns143(t *testing.T) {
	exitCode := runSandboxExecSignalTest(t, syscall.SIGTERM, func(service *fakeSandboxService, subscribeReady chan struct{}, cancelSeen chan struct{}, releaseEvents chan struct{}) {
		service.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		getCalls := 0
		service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			getCalls++
			switch getCalls {
			case 1:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 31, 0), nil
			case 2:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_CANCELLED, 32, 0), nil
			default:
				t.Fatalf("unexpected GetExec call %d", getCalls)
				return nil, nil
			}
		}
		service.cancelExecFn = func(_ context.Context, _ *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			close(cancelSeen)
			return &agboxv1.AcceptedResponse{Accepted: true}, nil
		}
		service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
			if request.GetFromSequence() != 31 {
				t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
			}
			close(subscribeReady)
			<-releaseEvents
			return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 32, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
		}
	})
	if exitCode != 143 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func TestSandboxExecAlreadyTerminalFallsBackToNormalExit(t *testing.T) {
	exitCode := runSandboxExecSignalTest(t, os.Interrupt, func(service *fakeSandboxService, subscribeReady chan struct{}, cancelSeen chan struct{}, releaseEvents chan struct{}) {
		service.createExecFn = func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{ExecId: "exec-1"}, nil
		}
		getCalls := 0
		service.getExecFn = func(_ context.Context, request *agboxv1.GetExecRequest) (*agboxv1.GetExecResponse, error) {
			getCalls++
			switch getCalls {
			case 1:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_RUNNING, 41, 0), nil
			case 2:
				return execResponse("exec-1", "sandbox-123", agboxv1.ExecState_EXEC_STATE_FINISHED, 42, 0), nil
			default:
				t.Fatalf("unexpected GetExec call %d", getCalls)
				return nil, nil
			}
		}
		service.cancelExecFn = func(_ context.Context, _ *agboxv1.CancelExecRequest) (*agboxv1.AcceptedResponse, error) {
			close(cancelSeen)
			return nil, execAlreadyTerminalError("exec exec-1 is already terminal")
		}
		service.subscribeFn = func(request *agboxv1.SubscribeSandboxEventsRequest, stream grpc.ServerStreamingServer[agboxv1.SandboxEvent]) error {
			if request.GetFromSequence() != 41 {
				t.Fatalf("unexpected from_sequence: %d", request.GetFromSequence())
			}
			close(subscribeReady)
			<-releaseEvents
			return stream.Send(&agboxv1.SandboxEvent{EventId: "event-1", Sequence: 42, SandboxId: "sandbox-123", Details: &agboxv1.SandboxEvent_Exec{Exec: &agboxv1.ExecEventDetails{ExecId: "exec-1"}}})
		}
	})
	if exitCode != 0 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
}

func runSandboxExecSignalTest(
	t *testing.T,
	sig os.Signal,
	configure func(service *fakeSandboxService, subscribeReady chan struct{}, cancelSeen chan struct{}, releaseEvents chan struct{}),
) int {
	t.Helper()

	service := &fakeSandboxService{}
	subscribeReady := make(chan struct{})
	cancelSeen := make(chan struct{})
	releaseEvents := make(chan struct{})
	signalCh := make(chan os.Signal, 1)
	configure(service, subscribeReady, cancelSeen, releaseEvents)

	client := startExecTestClient(t, service)
	resultCh := make(chan int, 1)
	go func() {
		resultCh <- exitCodeForError(runSandboxExecWithSignals(
			context.Background(),
			client,
			sandboxExecArgs{
				sandboxID:    "sandbox-123",
				command:      []string{"sleep", "1"},
				envOverrides: make(map[string]string),
			},
			signalCh,
		))
	}()

	<-subscribeReady
	signalCh <- sig
	<-cancelSeen
	close(releaseEvents)

	exitCode := <-resultCh
	return exitCode
}

func execResponse(execID, sandboxID string, state agboxv1.ExecState, sequence uint64, exitCode int32) *agboxv1.GetExecResponse {
	return &agboxv1.GetExecResponse{
		Exec: &agboxv1.ExecStatus{
			ExecId:            execID,
			SandboxId:         sandboxID,
			State:             state,
			ExitCode:          exitCode,
			LastEventSequence: sequence,
		},
	}
}

func execAlreadyTerminalError(message string) error {
	st := status.New(codes.Unknown, message)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: control.ReasonExecAlreadyTerminal,
	})
	if err != nil {
		panic(err)
	}
	return withDetails.Err()
}

func startExecTestClient(t *testing.T, service *fakeSandboxService) *rawclient.RawClient {
	t.Helper()

	socketPath, _ := startSandboxTestServer(t, service)

	client, err := rawclient.New(socketPath, rawclient.WithTimeout(0))
	if err != nil {
		t.Fatalf("rawclient.New failed: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Fatalf("rawclient.Close failed: %v", err)
		}
	})
	return client
}

func execEnvPairs(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values[key])
	}
	return strings.Join(pairs, ",")
}
