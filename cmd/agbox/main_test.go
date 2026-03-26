package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
)

func TestCLIUsesFixedSocketPath(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory socket path")
	}
	tempDir := t.TempDir()
	ignoredEnvKey := "UNRELATED_SOCKET_HINT"
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return tempDir, true
		case ignoredEnvKey:
			return filepath.Join(tempDir, "ignored.sock"), true
		default:
			return "", false
		}
	}
	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		t.Fatalf("SocketPath returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, closer, err := control.NewService(control.DefaultServiceConfig())
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if closer != nil {
		t.Cleanup(func() {
			if closeErr := closer.Close(); closeErr != nil {
				t.Fatalf("service closer failed: %v", closeErr)
			}
		})
	}
	go func() {
		_ = control.ListenAndServe(ctx, socketPath, service)
	}()
	waitForSocket(t, lookupEnv)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"ping"}, &stdout, &stderr, lookupEnv)
	if exitCode != 0 {
		t.Fatalf("run returned %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "daemon=agboxd") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestVersionCommandsPreserveExistingOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), nil, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if stdout.String() != "agbox "+version+"\n" {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = run(context.Background(), []string{"version"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if stdout.String() != version+"\n" {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestPingFailsWhenDaemonIsUnavailable(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory socket path")
	}
	tempDir := t.TempDir()
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return tempDir, true
		default:
			return "", false
		}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"ping"}, &stdout, &stderr, lookupEnv)
	if exitCode != 1 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "ping daemon") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestPingRejectsLegacySocketOverride(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"ping", "--socket", filepath.Join(t.TempDir(), "socket.sock")}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "does not accept arguments") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestSandboxCommandRequiresSubcommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "requires a subcommand") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "create, list, get, delete, exec") {
		t.Fatalf("missing subcommand list in stderr %q", stderr.String())
	}
}

func TestSandboxCommandRejectsUnknownSubcommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox", "unknown"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), `unknown sandbox command "unknown"`) {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func TestUnknownTopLevelCommandReturnsUsageError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"unknown"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeUsageError {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), `unknown command "unknown"`) {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func waitForSocket(t *testing.T, lookupEnv func(string) (string, bool)) {
	t.Helper()

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		t.Fatalf("SocketPath returned error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := runPing(context.Background(), nil, io.Discard, func(key string) (string, bool) {
			return lookupEnv(key)
		}); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon socket %q was not ready", socketPath)
}
