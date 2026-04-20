package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDataDirUsesXDGDataHome(t *testing.T) {
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

func TestFixedPlatformPaths(t *testing.T) {
	lookupEnv := func(key string) (string, bool) {
		switch key {
		case "XDG_RUNTIME_DIR":
			return "/run/user/1000", true
		case "XDG_CONFIG_HOME":
			return "/tmp/config", true
		case "XDG_DATA_HOME":
			return "/tmp/data", true
		default:
			return "", false
		}
	}

	cases := []struct {
		goos      string
		wantSock  string
		wantLock  string
		wantCfg   string
		wantStore string
	}{
		{
			goos:      "linux",
			wantSock:  filepath.Join("/run/user/1000", "agbox", "agboxd.sock"),
			wantLock:  filepath.Join("/run/user/1000", "agbox", "agboxd.lock"),
			wantCfg:   filepath.Join("/tmp/config", "agents-sandbox", "config.toml"),
			wantStore: filepath.Join("/tmp/data", "agents-sandbox", "ids.db"),
		},
		{
			// When lookupEnv provides XDG overrides, darwin also respects them.
			goos:      "darwin",
			wantSock:  filepath.Join("/run/user/1000", "agbox", "agboxd.sock"),
			wantLock:  filepath.Join("/run/user/1000", "agbox", "agboxd.lock"),
			wantCfg:   filepath.Join("/tmp/config", "agents-sandbox", "config.toml"),
			wantStore: filepath.Join("/tmp/data", "agents-sandbox", "ids.db"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			socketPath, err := socketPathForGOOS(tc.goos, lookupEnv)
			if err != nil {
				t.Fatalf("socketPathForGOOS returned error: %v", err)
			}
			lockPath, err := lockPathForGOOS(tc.goos, lookupEnv)
			if err != nil {
				t.Fatalf("lockPathForGOOS returned error: %v", err)
			}
			configPath, err := configFilePathForGOOS(tc.goos, lookupEnv)
			if err != nil {
				t.Fatalf("configFilePathForGOOS returned error: %v", err)
			}
			idStorePath, err := idStorePathForGOOS(tc.goos, lookupEnv)
			if err != nil {
				t.Fatalf("idStorePathForGOOS returned error: %v", err)
			}

			if socketPath != tc.wantSock {
				t.Fatalf("unexpected socket path: got %q want %q", socketPath, tc.wantSock)
			}
			if lockPath != tc.wantLock {
				t.Fatalf("unexpected lock path: got %q want %q", lockPath, tc.wantLock)
			}
			if configPath != tc.wantCfg {
				t.Fatalf("unexpected config path: got %q want %q", configPath, tc.wantCfg)
			}
			if idStorePath != tc.wantStore {
				t.Fatalf("unexpected id store path: got %q want %q", idStorePath, tc.wantStore)
			}
		})
	}
}

func TestExecLogRootWithXDGDataHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	root := execLogRootForGOOS("linux", func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/custom/data", true
		}
		return "", false
	})
	want := filepath.Join("/custom/data", "agents-sandbox", "exec-logs")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestExecLogRootFallsBackToHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	root := ExecLogRoot(func(string) (string, bool) { return "", false })
	if root == "" {
		t.Fatal("expected non-empty exec log root from home fallback")
	}
	if !strings.HasSuffix(root, filepath.Join("agents-sandbox", "exec-logs")) {
		t.Fatalf("expected path ending in agents-sandbox/exec-logs, got %q", root)
	}
}

func TestExecLogRootDarwinDefault(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir returned error: %v", err)
	}
	root := execLogRootForGOOS("darwin", func(string) (string, bool) { return "", false })
	want := filepath.Join(homeDir, "Library", "Application Support", "agents-sandbox", "exec-logs")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestExecLogRootDarwinWithOverride(t *testing.T) {
	root := execLogRootForGOOS("darwin", func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/custom/data", true
		}
		return "", false
	})
	want := filepath.Join("/custom/data", "agents-sandbox", "exec-logs")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestSocketPathRequiresRuntimeDirectoryOnLinux(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("linux-specific runtime behavior")
	}
	if _, err := socketPathForGOOS("linux", func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("expected SocketPath to fail when XDG_RUNTIME_DIR is unset")
	}
}

func TestSandboxDataRootWithXDGDataHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	root := sandboxDataRootForGOOS("linux", func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/custom/data", true
		}
		return "", false
	})
	want := filepath.Join("/custom/data", "agents-sandbox", "mounts")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestSandboxDataRootFallsBackToHome(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG not applicable on macOS")
	}
	root := SandboxDataRoot(func(string) (string, bool) { return "", false })
	if root == "" {
		t.Fatal("expected non-empty sandbox data root from home fallback")
	}
	if !strings.HasSuffix(root, filepath.Join("agents-sandbox", "mounts")) {
		t.Fatalf("expected path ending in agents-sandbox/mounts, got %q", root)
	}
}

func TestSandboxDataRootDarwinDefault(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir returned error: %v", err)
	}
	root := sandboxDataRootForGOOS("darwin", func(string) (string, bool) { return "", false })
	want := filepath.Join(homeDir, "Library", "Application Support", "agents-sandbox", "mounts")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestSandboxDataRootDarwinWithOverride(t *testing.T) {
	root := sandboxDataRootForGOOS("darwin", func(key string) (string, bool) {
		if key == "XDG_DATA_HOME" {
			return "/custom/data", true
		}
		return "", false
	})
	want := filepath.Join("/custom/data", "agents-sandbox", "mounts")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}
