package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDirUsesXDGDataHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	dir := DataDir(func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/custom/data", true
		}
		return "", false
	})
	if dir != "/custom/data" {
		t.Fatalf("expected /custom/data, got %q", dir)
	}
}

func TestDataDirFallsBackToHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	dir := DataDir(func(string) (string, bool) { return "", false })
	if dir == "" {
		t.Fatal("expected non-empty data dir from home fallback")
	}
	if filepath.Base(dir) != "share" {
		t.Fatalf("expected path ending in share, got %q", dir)
	}
}

func TestConfigDirUsesXDGConfigHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	dir := ConfigDir(func(key string) (string, bool) {
		if key == "XDG_CONFIG_HOME" {
			return "/custom/config", true
		}
		return "", false
	})
	if dir != "/custom/config" {
		t.Fatalf("expected /custom/config, got %q", dir)
	}
}

func TestRuntimeDirUsesXDGRuntimeDir(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	dir := RuntimeDir(func(key string) (string, bool) {
		if key == "XDG_RUNTIME_DIR" {
			return "/run/user/1000", true
		}
		return "", false
	})
	if dir != "/run/user/1000" {
		t.Fatalf("expected /run/user/1000, got %q", dir)
	}
}

func TestRuntimeDirEmptyWhenUnset(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	dir := RuntimeDir(func(string) (string, bool) { return "", false })
	if dir != "" {
		t.Fatalf("expected empty runtime dir when XDG_RUNTIME_DIR unset, got %q", dir)
	}
}
