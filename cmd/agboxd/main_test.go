package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
)

func TestFixedPlatformPaths(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}
	lookupEnv := fixedPathLookupEnv(t)
	writeDaemonConfig(t, lookupEnv, `
[runtime]
idle_ttl = "75s"
`)

	startup, err := resolveStartupConfig(nil, lookupEnv)
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}

	wantSocket, err := platform.SocketPath(lookupEnv)
	if err != nil {
		t.Fatalf("SocketPath returned error: %v", err)
	}
	wantLock, err := platform.LockPath(lookupEnv)
	if err != nil {
		t.Fatalf("LockPath returned error: %v", err)
	}
	wantConfig, err := platform.ConfigFilePath(lookupEnv)
	if err != nil {
		t.Fatalf("ConfigFilePath returned error: %v", err)
	}
	wantIDStore, err := platform.IDStorePath(lookupEnv)
	if err != nil {
		t.Fatalf("IDStorePath returned error: %v", err)
	}

	if startup.socketPath != wantSocket {
		t.Fatalf("unexpected socket path: got %q want %q", startup.socketPath, wantSocket)
	}
	if startup.lockPath != wantLock {
		t.Fatalf("unexpected lock path: got %q want %q", startup.lockPath, wantLock)
	}
	if startup.idStorePath != wantIDStore {
		t.Fatalf("unexpected id store path: got %q want %q", startup.idStorePath, wantIDStore)
	}
	if startup.serviceConfig.IdleTTL != 75*time.Second {
		t.Fatalf("unexpected idle ttl: got %s", startup.serviceConfig.IdleTTL)
	}
	if startup.serviceConfig.ArtifactOutputRoot != "" {
		t.Fatalf("unexpected artifact output root: got %q", startup.serviceConfig.ArtifactOutputRoot)
	}
	if _, err := os.Stat(wantConfig); err != nil {
		t.Fatalf("expected config file to be readable at %q: %v", wantConfig, err)
	}
}

func TestDaemonRejectsLegacyPathOverrides(t *testing.T) {
	lookupEnv := fixedPathLookupEnv(t)

	for _, args := range [][]string{
		{"--socket", "/tmp/from-flag.sock"},
		{"--config", "/tmp/from-config.toml"},
	} {
		_, err := resolveStartupConfig(args, lookupEnv)
		if err == nil {
			t.Fatalf("expected args %v to be rejected", args)
		}
		if !strings.Contains(err.Error(), "does not accept CLI path overrides") {
			t.Fatalf("unexpected error for args %v: %v", args, err)
		}
	}
}

func TestResolveStartupConfigRejectsInvalidIdleTTL(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}
	lookupEnv := fixedPathLookupEnv(t)
	writeDaemonConfig(t, lookupEnv, `
[runtime]
idle_ttl = "never"
`)

	_, err := resolveStartupConfig(nil, lookupEnv)
	if err == nil {
		t.Fatal("expected invalid idle ttl to fail")
	}
}

func TestEventRetentionTTLConfig(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}

	t.Run("explicit", func(t *testing.T) {
		lookupEnv := fixedPathLookupEnv(t)
		writeDaemonConfig(t, lookupEnv, `
[runtime]
event_retention_ttl = "24h"
`)

		startup, err := resolveStartupConfig(nil, lookupEnv)
		if err != nil {
			t.Fatalf("resolveStartupConfig returned error: %v", err)
		}
		if startup.serviceConfig.EventRetentionTTL != 24*time.Hour {
			t.Fatalf("unexpected event retention ttl: got %s want %s", startup.serviceConfig.EventRetentionTTL, 24*time.Hour)
		}
	})

	t.Run("default", func(t *testing.T) {
		lookupEnv := fixedPathLookupEnv(t)
		startup, err := resolveStartupConfig(nil, lookupEnv)
		if err != nil {
			t.Fatalf("resolveStartupConfig returned error: %v", err)
		}
		if startup.serviceConfig.EventRetentionTTL != 168*time.Hour {
			t.Fatalf("unexpected default event retention ttl: got %s want %s", startup.serviceConfig.EventRetentionTTL, 168*time.Hour)
		}
	})
}

func TestRunWithDepsUsesResolvedLockPath(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}
	lookupEnv := fixedPathLookupEnv(t)
	startup, err := resolveStartupConfig(nil, lookupEnv)
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}

	var lockPath string
	var socketPath string
	var service *control.Service
	exitCode := runWithDeps(
		context.Background(),
		nil,
		io.Discard,
		lookupEnv,
		func(path string) (*hostLock, error) {
			lockPath = path
			return &hostLock{path: path}, nil
		},
		func(ctx context.Context, path string, svc *control.Service, _ *slog.Logger) error {
			_ = ctx
			socketPath = path
			service = svc
			return nil
		},
		control.NewServiceWithPersistentIDStore,
	)

	if exitCode != 0 {
		t.Fatalf("unexpected exit code: got %d want 0", exitCode)
	}
	if lockPath != startup.lockPath {
		t.Fatalf("unexpected lock path: got %q want %q", lockPath, startup.lockPath)
	}
	if socketPath != startup.socketPath {
		t.Fatalf("unexpected socket path: got %q want %q", socketPath, startup.socketPath)
	}
	if service == nil {
		t.Fatal("expected service to be created")
	}
}

func TestRunWithDepsFailsBeforeSocketMutationWhenHostLockIsHeld(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}
	lookupEnv := fixedPathLookupEnv(t)
	startup, err := resolveStartupConfig(nil, lookupEnv)
	if err != nil {
		t.Fatalf("resolveStartupConfig returned error: %v", err)
	}

	exitCode := runWithDeps(
		context.Background(),
		nil,
		io.Discard,
		lookupEnv,
		func(lockPath string) (*hostLock, error) {
			if lockPath != startup.lockPath {
				t.Fatalf("unexpected host lock path: got %q want %q", lockPath, startup.lockPath)
			}
			return nil, errors.New("lock already held")
		},
		func(context.Context, string, *control.Service, *slog.Logger) error {
			t.Fatal("listenAndServe should not run when host lock acquisition fails")
			return nil
		},
		control.NewServiceWithPersistentIDStore,
	)

	if exitCode != 1 {
		t.Fatalf("unexpected exit code: got %d want 1", exitCode)
	}
}

func TestDaemonFailsFastWhenIDStoreInitFails(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	configRoot := filepath.Join(t.TempDir(), "config")
	dataRoot := filepath.Join(t.TempDir(), "blocked-data-root")
	if err := os.WriteFile(dataRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return runtimeDir, true
		case "XDG_CONFIG_HOME":
			return configRoot, true
		case "XDG_DATA_HOME":
			return dataRoot, true
		default:
			return "", false
		}
	}

	exitCode := runWithDeps(
		context.Background(),
		nil,
		io.Discard,
		lookupEnv,
		func(path string) (*hostLock, error) {
			return &hostLock{path: path}, nil
		},
		func(context.Context, string, *control.Service, *slog.Logger) error {
			t.Fatal("listenAndServe should not run when id store initialization fails")
			return nil
		},
		control.NewServiceWithPersistentIDStore,
	)

	if exitCode != 1 {
		t.Fatalf("unexpected exit code: got %d want 1", exitCode)
	}
}

func fixedPathLookupEnv(t *testing.T) func(string) (string, bool) {
	t.Helper()

	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	configRoot := filepath.Join(t.TempDir(), "config")
	dataRoot := filepath.Join(t.TempDir(), "data")
	return func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return runtimeDir, true
		case "XDG_CONFIG_HOME":
			return configRoot, true
		case "XDG_DATA_HOME":
			return dataRoot, true
		default:
			return "", false
		}
	}
}

func writeDaemonConfig(t *testing.T, lookupEnv func(string) (string, bool), content string) string {
	t.Helper()

	configPath, err := platform.ConfigFilePath(lookupEnv)
	if err != nil {
		t.Fatalf("ConfigFilePath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	return configPath
}

func TestConfigLogLevel(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses a shared fixed home-directory config path")
	}

	t.Run("default", func(t *testing.T) {
		lookupEnv := fixedPathLookupEnv(t)
		startup, err := resolveStartupConfig(nil, lookupEnv)
		if err != nil {
			t.Fatalf("resolveStartupConfig returned error: %v", err)
		}
		if startup.serviceConfig.LogLevel != "info" {
			t.Fatalf("unexpected default log level: got %q want %q", startup.serviceConfig.LogLevel, "info")
		}
	})

	t.Run("explicit", func(t *testing.T) {
		lookupEnv := fixedPathLookupEnv(t)
		writeDaemonConfig(t, lookupEnv, `
[runtime]
log_level = "debug"
`)
		startup, err := resolveStartupConfig(nil, lookupEnv)
		if err != nil {
			t.Fatalf("resolveStartupConfig returned error: %v", err)
		}
		if startup.serviceConfig.LogLevel != "debug" {
			t.Fatalf("unexpected log level: got %q want %q", startup.serviceConfig.LogLevel, "debug")
		}
	})
}
