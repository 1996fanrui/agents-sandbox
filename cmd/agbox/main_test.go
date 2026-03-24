package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
)

func TestPingConnectsToDaemon(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "agboxd.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = control.ListenAndServe(ctx, socketPath, control.NewService(control.DefaultServiceConfig()))
	}()
	waitForSocket(t, socketPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"ping", "--socket", socketPath}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != 0 {
		t.Fatalf("run returned %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "daemon=agboxd") {
		t.Fatalf("unexpected stdout %q", stdout.String())
	}
}

func TestPingFailsWhenDaemonIsUnavailable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"ping", "--socket", filepath.Join(t.TempDir(), "missing.sock")}, &stdout, &stderr, func(string) (string, bool) {
		return "", false
	})
	if exitCode != 1 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "ping daemon") {
		t.Fatalf("unexpected stderr %q", stderr.String())
	}
}

func waitForSocket(t *testing.T, socketPath string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := runPing(context.Background(), []string{"--socket", socketPath}, io.Discard, func(string) (string, bool) {
			return "", false
		}); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon socket %q was not ready", socketPath)
}
