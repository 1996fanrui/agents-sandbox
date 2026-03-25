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
	go func() {
		_ = control.ListenAndServe(ctx, socketPath, control.NewService(control.DefaultServiceConfig()))
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
	if exitCode != 1 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "does not accept arguments") {
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
