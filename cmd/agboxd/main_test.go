package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
)

func TestResolveStartupConfigDefaultsToStandardSocket(t *testing.T) {
	startup, err := resolveStartupConfig(nil, func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}
	if startup.socketPath != defaultSocketPath {
		t.Fatalf("unexpected socket path: got %q want %q", startup.socketPath, defaultSocketPath)
	}
	if startup.serviceConfig.IdleTTL != 30*time.Minute {
		t.Fatalf("unexpected idle ttl: got %s", startup.serviceConfig.IdleTTL)
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
			if lockPath != defaultLockPath {
				t.Fatalf("unexpected host lock path: got %q want %q", lockPath, defaultLockPath)
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

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return configPath
}
