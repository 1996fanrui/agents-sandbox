package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/proto/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestResolveStartupConfigDefaultsToStandardSocket(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return runtimeDir, true
		default:
			return "", false
		}
	}
	startup, err := resolveStartupConfig(nil, lookupEnv)
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != expectedDefaultSocketPathForTest(lookupEnv) {
		t.Fatalf("unexpected socket path: got %q want %q", startup.socketPath, expectedDefaultSocketPathForTest(lookupEnv))
	}
	if startup.serviceConfig.IdleTTL != 30*time.Minute {
		t.Fatalf("unexpected idle ttl: got %s", startup.serviceConfig.IdleTTL)
	}
}

func TestResolveStartupConfigAutoLoadsDiscoveredConfig(t *testing.T) {
	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "agents-sandbox")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[server]
socket_path = "/tmp/auto-discovered.sock"

[runtime]
idle_ttl = "75s"

[artifacts]
exec_output_root = "/tmp/artifacts"
exec_output_template = "{sandbox_id}/{exec_id}.jsonl"
`), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	startup, err := resolveStartupConfig(nil, func(key string) (string, bool) {
		switch key {
		case "XDG_CONFIG_HOME":
			return configRoot, true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != "/tmp/auto-discovered.sock" {
		t.Fatalf("unexpected socket path: got %q", startup.socketPath)
	}
	if startup.serviceConfig.IdleTTL != 75*time.Second {
		t.Fatalf("unexpected idle ttl: got %s", startup.serviceConfig.IdleTTL)
	}
	if startup.serviceConfig.ArtifactOutputRoot != "/tmp/artifacts" {
		t.Fatalf("unexpected artifact output root: got %q", startup.serviceConfig.ArtifactOutputRoot)
	}
}

func TestResolveStartupConfigUsesConfigFileForSocketAndIdleTTL(t *testing.T) {
	configPath := writeConfigFile(t, `
[server]
socket_path = "/tmp/from-config.sock"

[runtime]
idle_ttl = "45s"
`)

	startup, err := resolveStartupConfig([]string{"--config", configPath}, func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != "/tmp/from-config.sock" {
		t.Fatalf("unexpected socket path: got %q", startup.socketPath)
	}
	if startup.serviceConfig.IdleTTL != 45*time.Second {
		t.Fatalf("unexpected idle ttl: got %s", startup.serviceConfig.IdleTTL)
	}
}

func TestResolveStartupConfigUsesEnvironmentWhenFlagMissing(t *testing.T) {
	startup, err := resolveStartupConfig(nil, func(key string) (string, bool) {
		switch key {
		case socketEnvVar:
			return "/tmp/from-env.sock", true
		case configEnvVar:
			return "", false
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != "/tmp/from-env.sock" {
		t.Fatalf("unexpected socket path: got %q", startup.socketPath)
	}
}

func TestResolveStartupConfigFlagOverridesEnvironmentAndConfig(t *testing.T) {
	configPath := writeConfigFile(t, `
[server]
socket_path = "/tmp/from-config.sock"
`)

	startup, err := resolveStartupConfig(
		[]string{"--socket", "/tmp/from-flag.sock"},
		func(key string) (string, bool) {
			switch key {
			case socketEnvVar:
				return "/tmp/from-env.sock", true
			case configEnvVar:
				return configPath, true
			default:
				return "", false
			}
		},
	)
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != "/tmp/from-flag.sock" {
		t.Fatalf("unexpected socket path: got %q", startup.socketPath)
	}
}

func TestResolveStartupConfigRejectsInvalidIdleTTL(t *testing.T) {
	configPath := writeConfigFile(t, `
[runtime]
idle_ttl = "never"
`)

	_, err := resolveStartupConfig([]string{"--config", configPath}, func(string) (string, bool) {
		return "", false
	})
	if err == nil {
		t.Fatal("expected invalid idle ttl to fail")
	}
}

func TestResolveLockPathKeepsHostDefaultAndCoLocatesOverride(t *testing.T) {
	if got := resolveLockPath(defaultSocketPath); got != defaultLockPath {
		t.Fatalf("unexpected default lock path: got %q want %q", got, defaultLockPath)
	}

	overrideSocket := filepath.Join("/tmp", "custom", "agboxd.sock")
	wantOverrideLock := filepath.Join("/tmp", "custom", "agboxd.lock")
	if got := resolveLockPath(overrideSocket); got != wantOverrideLock {
		t.Fatalf("unexpected override lock path: got %q want %q", got, wantOverrideLock)
	}
}

func expectedDefaultSocketPathForTest(lookupEnv func(string) (string, bool)) string {
	switch runtime.GOOS {
	case "darwin":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return defaultSocketPath
		}
		return filepath.Join(homeDir, "Library", "Application Support", "agbox", "run", "agboxd.sock")
	default:
		if runtimeDir, ok := lookupEnv("XDG_RUNTIME_DIR"); ok && runtimeDir != "" {
			return filepath.Join(runtimeDir, "agbox", "agboxd.sock")
		}
		return defaultSocketPath
	}
}

func TestAcquireHostLockRejectsSecondDaemonOnSameMachine(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "agboxd.lock")

	first, err := acquireHostLock(lockPath)
	if err != nil {
		t.Fatalf("acquireHostLock returned error: %v", err)
	}
	defer func() {
		if releaseErr := first.release(); releaseErr != nil {
			t.Fatalf("release returned error: %v", releaseErr)
		}
	}()

	second, err := acquireHostLock(lockPath)
	if err == nil {
		_ = second.release()
		t.Fatal("expected second host lock acquisition to fail")
	}
	if !strings.Contains(err.Error(), "already held") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFailsBeforeSocketMutationWhenHostLockIsHeld(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "agboxd.sock")
	if err := os.WriteFile(socketPath, []byte("stale socket placeholder"), 0o644); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	var stderr strings.Builder
	exitCode := runWithDeps(
		context.Background(),
		[]string{"--socket", socketPath},
		&stderr,
		func(string) (string, bool) { return "", false },
		func(lockPath string) (*hostLock, error) {
			wantLockPath := filepath.Join(filepath.Dir(socketPath), "agboxd.lock")
			if lockPath != wantLockPath {
				t.Fatalf("unexpected host lock path: got %q want %q", lockPath, wantLockPath)
			}
			return nil, errors.New("lock already held")
		},
		func(context.Context, string, *control.Service) error {
			t.Fatal("listenAndServe should not run when host lock acquisition fails")
			return nil
		},
	)

	if exitCode != 1 {
		t.Fatalf("unexpected exit code: got %d want 1", exitCode)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("socket placeholder should still exist: %v", err)
	}
	if !strings.Contains(stderr.String(), "lock already held") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunStartsDaemonWithOverriddenSocketAndAdjacentLock(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "agboxd.sock")
	lockPath := filepath.Join(tempDir, "agboxd.lock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exitCodes := make(chan int, 1)
	go func() {
		exitCodes <- runWithDeps(ctx, []string{"--socket", socketPath}, io.Discard, func(string) (string, bool) {
			return "", false
		}, acquireHostLock, control.ListenAndServe)
	}()

	waitForDaemonPing(t, socketPath)

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected adjacent lock file to exist: %v", err)
	}

	cancel()

	select {
	case exitCode := <-exitCodes:
		if exitCode != 0 {
			t.Fatalf("unexpected exit code: got %d want 0", exitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for daemon shutdown")
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return configPath
}

func waitForDaemonPing(t *testing.T, socketPath string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := grpc.NewClient(
			"passthrough:///agboxd-test",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			}),
		)
		if err == nil {
			client := agboxv1.NewSandboxServiceClient(conn)
			_, err = client.Ping(context.Background(), &agboxv1.PingRequest{})
			_ = conn.Close()
			if err == nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("daemon socket %q was not ready for ping", socketPath)
}
