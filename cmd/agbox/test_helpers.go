package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"google.golang.org/grpc"
)

func startSandboxTestServer(t *testing.T, service agboxv1.SandboxServiceServer) (string, func(string) (string, bool)) {
	t.Helper()

	tempDir := t.TempDir()
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return tempDir, true
		default:
			return "", false
		}
	}
	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		t.Fatalf("SocketPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	server := grpc.NewServer()
	agboxv1.RegisterSandboxServiceServer(server, service)
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("grpc server did not stop")
		}
		_ = os.Remove(socketPath)
	})

	waitForUnixSocket(t, socketPath)
	return socketPath, lookupEnv
}

func waitForUnixSocket(t *testing.T, socketPath string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("unix socket %q was not ready", socketPath)
}
