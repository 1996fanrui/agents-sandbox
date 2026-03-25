// Package platform provides platform-aware directory resolution following
// XDG Base Directory conventions on Linux and Apple conventions on macOS.
package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// LookupEnv abstracts environment variable lookup so callers can inject
// test values without mutating the real process environment.
type LookupEnv func(string) (string, bool)

// ConfigDir returns the base directory for user configuration files.
//
//   - Linux: $XDG_CONFIG_HOME, falling back to ~/.config
//   - macOS: ~/Library/Application Support
func ConfigDir(lookupEnv LookupEnv) string {
	switch runtime.GOOS {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnv("XDG_CONFIG_HOME"); ok && v != "" {
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
	switch runtime.GOOS {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnv("XDG_DATA_HOME"); ok && v != "" {
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
	switch runtime.GOOS {
	case "darwin":
		return macAppSupportDir()
	default:
		if v, ok := lookupEnv("XDG_RUNTIME_DIR"); ok && v != "" {
			return v
		}
		return ""
	}
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
