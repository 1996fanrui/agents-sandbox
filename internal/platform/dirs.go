// Package platform provides platform-aware directory resolution following
// XDG Base Directory conventions on Linux and Apple conventions on macOS.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// LookupEnv abstracts environment variable lookup so callers can inject
// test values without mutating the real process environment.
type LookupEnv func(string) (string, bool)

const (
	AgentsSandboxDirName = "agents-sandbox"
	RuntimeDirName       = "agbox"
)

// ConfigDir returns the base directory for user configuration files.
//
//   - Linux: $XDG_CONFIG_HOME, falling back to ~/.config
//   - macOS: ~/Library/Application Support
func ConfigDir(lookupEnv LookupEnv) string {
	return configDirForGOOS(runtime.GOOS, lookupEnv)
}

func configDirForGOOS(goos string, lookupEnv LookupEnv) string {
	switch goos {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnvValue(lookupEnv, "XDG_CONFIG_HOME"); ok && v != "" {
			return v
		}
		if home := homeDir(); home != "" {
			return filepath.Join(home, ".config")
		}
		return ""
	}
}

// DataDir returns the base directory for user data files.
//
//   - Linux: $XDG_DATA_HOME, falling back to ~/.local/share
//   - macOS: ~/Library/Application Support
func DataDir(lookupEnv LookupEnv) string {
	return dataDirForGOOS(runtime.GOOS, lookupEnv)
}

func dataDirForGOOS(goos string, lookupEnv LookupEnv) string {
	switch goos {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnvValue(lookupEnv, "XDG_DATA_HOME"); ok && v != "" {
			return v
		}
		if home := homeDir(); home != "" {
			return filepath.Join(home, ".local", "share")
		}
		return ""
	}
}

// RuntimeDir returns the base directory for runtime files (sockets, locks).
//
//   - Linux: $XDG_RUNTIME_DIR (no fallback — empty if unset)
//   - macOS: ~/Library/Application Support
func RuntimeDir(lookupEnv LookupEnv) string {
	return runtimeDirForGOOS(runtime.GOOS, lookupEnv)
}

func runtimeDirForGOOS(goos string, lookupEnv LookupEnv) string {
	switch goos {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnvValue(lookupEnv, "XDG_RUNTIME_DIR"); ok && v != "" {
			return v
		}
		return ""
	}
}

// SocketPath returns the fixed daemon socket path for the current platform.
func SocketPath(lookupEnv LookupEnv) (string, error) {
	return socketPathForGOOS(runtime.GOOS, lookupEnv)
}

func socketPathForGOOS(goos string, lookupEnv LookupEnv) (string, error) {
	runtimeRoot, err := runtimeRootPathForGOOS(goos, lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(runtimeRoot, "agboxd.sock"), nil
}

// LockPath returns the fixed daemon host lock path for the current platform.
func LockPath(lookupEnv LookupEnv) (string, error) {
	return lockPathForGOOS(runtime.GOOS, lookupEnv)
}

func lockPathForGOOS(goos string, lookupEnv LookupEnv) (string, error) {
	runtimeRoot, err := runtimeRootPathForGOOS(goos, lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(runtimeRoot, "agboxd.lock"), nil
}

// ConfigFilePath returns the fixed daemon config path for the current platform.
func ConfigFilePath(lookupEnv LookupEnv) (string, error) {
	return configFilePathForGOOS(runtime.GOOS, lookupEnv)
}

func configFilePathForGOOS(goos string, lookupEnv LookupEnv) (string, error) {
	configRoot := configDirForGOOS(goos, lookupEnv)
	if configRoot == "" {
		return "", fmt.Errorf("resolve config path: config root is unavailable on %s", goos)
	}
	return filepath.Join(configRoot, AgentsSandboxDirName, "config.toml"), nil
}

// IDStorePath returns the fixed persistent ID registry path for the current platform.
func IDStorePath(lookupEnv LookupEnv) (string, error) {
	return idStorePathForGOOS(runtime.GOOS, lookupEnv)
}

func idStorePathForGOOS(goos string, lookupEnv LookupEnv) (string, error) {
	dataRoot := dataDirForGOOS(goos, lookupEnv)
	if dataRoot == "" {
		return "", fmt.Errorf("resolve id store path: data root is unavailable on %s", goos)
	}
	return filepath.Join(dataRoot, AgentsSandboxDirName, "ids.db"), nil
}

func runtimeRootPathForGOOS(goos string, lookupEnv LookupEnv) (string, error) {
	runtimeDir := runtimeDirForGOOS(goos, lookupEnv)
	switch goos {
	case "darwin":
		if runtimeDir == "" {
			return "", fmt.Errorf("resolve runtime path: application support directory is unavailable on %s", goos)
		}
		return filepath.Join(runtimeDir, RuntimeDirName, "run"), nil
	default:
		if runtimeDir == "" {
			return "", fmt.Errorf("resolve runtime path: XDG_RUNTIME_DIR is required on %s", goos)
		}
		return filepath.Join(runtimeDir, RuntimeDirName), nil
	}
}

func lookupEnvValue(lookupEnv LookupEnv, key string) (string, bool) {
	if lookupEnv == nil {
		return "", false
	}
	return lookupEnv(key)
}

func macAppSupportDir() string {
	if home := homeDir(); home != "" {
		return filepath.Join(home, "Library", "Application Support")
	}
	return ""
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return home
}
