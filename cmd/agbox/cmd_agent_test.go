package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTempDir returns a t.TempDir() path with symlinks resolved, matching the
// behavior of resolveAgentSessionArgs which calls filepath.EvalSymlinks.
// This is necessary on macOS where /var is a symlink to /private/var.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	return real
}

func TestResolveAgentSessionArgs_RegisteredType(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs("claude", "", tmpDir, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "claude" {
		t.Fatalf("expected agentType=claude, got %q", parsed.agentType)
	}
	if len(parsed.command) == 0 || parsed.command[0] != "claude" {
		t.Fatalf("expected command from agentTypeDefs, got %v", parsed.command)
	}
	if parsed.workspace != tmpDir {
		t.Fatalf("expected workspace=%s, got %q", tmpDir, parsed.workspace)
	}
	if len(parsed.builtinTools) == 0 {
		t.Fatal("expected default builtin tools for claude")
	}
}

func TestResolveAgentSessionArgs_RegisteredTypeOverrideBuiltinTools(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs("claude", "", tmpDir, []string{"git"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 1 || parsed.builtinTools[0] != "git" {
		t.Fatalf("expected builtinTools=[git], got %v", parsed.builtinTools)
	}
}

func TestResolveAgentSessionArgs_CustomCommand(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs("", "aider --yes", tmpDir, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "" {
		t.Fatalf("expected empty agentType, got %q", parsed.agentType)
	}
	if len(parsed.command) != 2 || parsed.command[0] != "aider" || parsed.command[1] != "--yes" {
		t.Fatalf("expected command=[aider --yes], got %v", parsed.command)
	}
	if parsed.workspace != tmpDir {
		t.Fatalf("expected workspace=%s, got %q", tmpDir, parsed.workspace)
	}
}

func TestResolveAgentSessionArgs_CustomCommandWithBuiltinTools(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs("", "aider", tmpDir, []string{"git", "uv"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 2 || parsed.builtinTools[0] != "git" || parsed.builtinTools[1] != "uv" {
		t.Fatalf("expected builtinTools=[git uv], got %v", parsed.builtinTools)
	}
}

func TestResolveAgentSessionArgs_MutualExclusion(t *testing.T) {
	_, err := resolveAgentSessionArgs("claude", "aider", "/work", nil, false)
	if err == nil {
		t.Fatal("expected error for agent type + --command")
	}
	if !strings.Contains(err.Error(), "cannot use --command with agent type") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_NeitherTypeNorCommand(t *testing.T) {
	_, err := resolveAgentSessionArgs("", "", "/work", nil, false)
	if err == nil {
		t.Fatal("expected error when neither agent type nor --command is given")
	}
	if !strings.Contains(err.Error(), "requires an agent type or --command") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_UnknownType(t *testing.T) {
	_, err := resolveAgentSessionArgs("nonexistent", "", "/work", nil, false)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_EmptyCommand(t *testing.T) {
	_, err := resolveAgentSessionArgs("", "  ", "/work", nil, false)
	if err == nil {
		t.Fatal("expected error for empty --command")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_DuplicateAgentType(t *testing.T) {
	tmpDir := realTempDir(t)
	// With cobra, duplicate positional args are prevented by cobra.MaximumNArgs(1).
	// Here we test resolveAgentSessionArgs directly with a registered agent type.
	parsed, err := resolveAgentSessionArgs("claude", "", tmpDir, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "claude" {
		t.Fatalf("expected agentType=claude, got %q", parsed.agentType)
	}
}

func TestResolveAgentSessionArgs_RejectRoot(t *testing.T) {
	_, err := resolveAgentSessionArgs("claude", "", "/", nil, false)
	if err == nil {
		t.Fatal("expected error for root workspace")
	}
	if !strings.Contains(err.Error(), "root directory") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_RejectHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}
	_, err = resolveAgentSessionArgs("claude", "", home, nil, false)
	if err == nil {
		t.Fatal("expected error for home directory workspace")
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestConfirmWorkspaceCopy(t *testing.T) {
	path := "/some/workspace"

	t.Run("accept_y", func(t *testing.T) {
		stdin := strings.NewReader("y\n")
		var stderr bytes.Buffer
		err := confirmWorkspaceCopy(stdin, &stderr, path)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if !strings.Contains(stderr.String(), path) {
			t.Fatalf("stderr should contain path, got %q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "[y/N]") {
			t.Fatalf("stderr should contain [y/N], got %q", stderr.String())
		}
	})

	t.Run("accept_Y", func(t *testing.T) {
		stdin := strings.NewReader("Y\n")
		var stderr bytes.Buffer
		err := confirmWorkspaceCopy(stdin, &stderr, path)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("reject_n", func(t *testing.T) {
		stdin := strings.NewReader("n\n")
		var stderr bytes.Buffer
		err := confirmWorkspaceCopy(stdin, &stderr, path)
		if err == nil {
			t.Fatal("expected error for n input")
		}
	})

	t.Run("reject_empty", func(t *testing.T) {
		stdin := strings.NewReader("\n")
		var stderr bytes.Buffer
		err := confirmWorkspaceCopy(stdin, &stderr, path)
		if err == nil {
			t.Fatal("expected error for empty input")
		}
	})

	t.Run("reject_eof", func(t *testing.T) {
		stdin := strings.NewReader("")
		var stderr bytes.Buffer
		err := confirmWorkspaceCopy(stdin, &stderr, path)
		if err == nil {
			t.Fatal("expected error for EOF")
		}
	})
}
