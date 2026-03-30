package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/internal/version"
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
	cfg := control.DefaultServiceConfig()
	cfg.Logger = slog.Default()
	service, closer, err := control.NewService(cfg)
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
		_ = control.ListenAndServe(ctx, socketPath, service, slog.Default())
	}()
	waitForSocket(t, lookupEnv)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"version"}, &stdout, &stderr, lookupEnv)
	if exitCode != 0 {
		t.Fatalf("run returned %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agbox: "+version.Version) {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "agboxd: "+version.Version) {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestVersionCommandsPreserveExistingOutput(t *testing.T) {
	// Point to a non-existent runtime dir so the test never connects to a
	// real running daemon, which would change the expected output.
	isolatedEnv := func(key string) (string, bool) {
		if key == "XDG_RUNTIME_DIR" {
			return t.TempDir(), true
		}
		return "", false
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), nil, &stdout, &stderr, isolatedEnv)
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	wantLines := "agbox " + version.Version + "\n" + "Run \"agbox --help\" for usage information.\n"
	if stdout.String() != wantLines {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = run(context.Background(), []string{"version"}, &stdout, &stderr, isolatedEnv)
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	// When daemon is unavailable, version shows agbox version + "agboxd: unavailable".
	if !strings.Contains(stdout.String(), "agbox: "+version.Version) {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "agboxd: unavailable") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
	if stderr.Len() != 0 {
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
	if !strings.Contains(stderr.String(), "unknown command") {
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

func TestHelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := run(context.Background(), []string{flag}, &stdout, &stderr, func(string) (string, bool) {
			return "", false
		})
		if exitCode != exitCodeSuccess {
			t.Fatalf("unexpected exit code %d for %s", exitCode, flag)
		}
		output := stdout.String()
		for _, want := range []string{"sandbox", "agent", "version"} {
			if !strings.Contains(output, want) {
				t.Fatalf("help output missing %q for %s: %q", want, flag, output)
			}
		}
	}
}

func TestSandboxHelpFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox", "--help"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	output := stdout.String()
	for _, want := range []string{"create", "list", "get", "delete", "exec"} {
		if !strings.Contains(output, want) {
			t.Fatalf("sandbox help output missing %q: %q", want, output)
		}
	}
}

func TestSandboxCreateHelpFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox", "create", "--help"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "--image") {
		t.Fatalf("sandbox create help output missing --image: %q", stdout.String())
	}
}

func TestSandboxListHelpFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox", "list", "--help"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "--include-deleted") {
		t.Fatalf("sandbox list help output missing --include-deleted: %q", stdout.String())
	}
}

func TestSandboxExecHelpFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"sandbox", "exec", "--help"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "--cwd") {
		t.Fatalf("sandbox exec help output missing --cwd: %q", stdout.String())
	}
}

func TestAgentHelpFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"agent", "--help"}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != exitCodeSuccess {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	output := stdout.String()
	for _, want := range []string{"--command", "--mount"} {
		if !strings.Contains(output, want) {
			t.Fatalf("agent help output missing %q: %q", want, output)
		}
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
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := run(context.Background(), []string{"version"}, &stdout, &stderr, lookupEnv)
		if exitCode == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon socket %q was not ready", socketPath)
}
