package main

import (
	"strings"
	"testing"
)

func TestParseAgentSessionArgs_RegisteredTool(t *testing.T) {
	parsed, err := parseAgentSessionArgs([]string{"claude"}, "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.toolName != "claude" {
		t.Fatalf("expected toolName=claude, got %q", parsed.toolName)
	}
	if len(parsed.command) == 0 || parsed.command[0] != "claude" {
		t.Fatalf("expected command from agentToolDefs, got %v", parsed.command)
	}
	if parsed.mount != "/work" {
		t.Fatalf("expected mount=/work, got %q", parsed.mount)
	}
	if len(parsed.builtinTools) == 0 {
		t.Fatal("expected default builtin tools for claude")
	}
}

func TestParseAgentSessionArgs_RegisteredToolOverrideBuiltinTools(t *testing.T) {
	parsed, err := parseAgentSessionArgs([]string{"claude", "--builtin-tool", "git"}, "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 1 || parsed.builtinTools[0] != "git" {
		t.Fatalf("expected builtinTools=[git], got %v", parsed.builtinTools)
	}
}

func TestParseAgentSessionArgs_CustomCommand(t *testing.T) {
	parsed, err := parseAgentSessionArgs([]string{"--command", "aider --yes", "--mount", "/my/project"}, "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.toolName != "" {
		t.Fatalf("expected empty toolName, got %q", parsed.toolName)
	}
	if len(parsed.command) != 2 || parsed.command[0] != "aider" || parsed.command[1] != "--yes" {
		t.Fatalf("expected command=[aider --yes], got %v", parsed.command)
	}
	if parsed.mount != "/my/project" {
		t.Fatalf("expected mount=/my/project, got %q", parsed.mount)
	}
}

func TestParseAgentSessionArgs_CustomCommandWithBuiltinTools(t *testing.T) {
	parsed, err := parseAgentSessionArgs([]string{"--command", "aider", "--builtin-tool", "git", "--builtin-tool", "uv"}, "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 2 || parsed.builtinTools[0] != "git" || parsed.builtinTools[1] != "uv" {
		t.Fatalf("expected builtinTools=[git uv], got %v", parsed.builtinTools)
	}
}

func TestParseAgentSessionArgs_MutualExclusion(t *testing.T) {
	_, err := parseAgentSessionArgs([]string{"claude", "--command", "aider"}, "/work")
	if err == nil {
		t.Fatal("expected error for tool name + --command")
	}
	if !strings.Contains(err.Error(), "cannot use --command with a registered tool name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAgentSessionArgs_NeitherToolNorCommand(t *testing.T) {
	_, err := parseAgentSessionArgs(nil, "/work")
	if err == nil {
		t.Fatal("expected error when neither tool name nor --command is given")
	}
	if !strings.Contains(err.Error(), "requires a tool name or --command") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAgentSessionArgs_UnknownTool(t *testing.T) {
	_, err := parseAgentSessionArgs([]string{"nonexistent"}, "/work")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown agent tool") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAgentSessionArgs_EmptyCommand(t *testing.T) {
	_, err := parseAgentSessionArgs([]string{"--command", "  "}, "/work")
	if err == nil {
		t.Fatal("expected error for empty --command")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAgentSessionArgs_DuplicateToolName(t *testing.T) {
	_, err := parseAgentSessionArgs([]string{"claude", "codex"}, "/work")
	if err == nil {
		t.Fatal("expected error for duplicate positional args")
	}
	if !strings.Contains(err.Error(), "tool name already set") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestParseAgentSessionArgs_UnknownFlag(t *testing.T) {
	_, err := parseAgentSessionArgs([]string{"claude", "--unknown"}, "/work")
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
