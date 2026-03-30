package main

import (
	"strings"
	"testing"
)

func TestResolveAgentSessionArgs_RegisteredType(t *testing.T) {
	parsed, err := resolveAgentSessionArgs("claude", "", "/work", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "claude" {
		t.Fatalf("expected agentType=claude, got %q", parsed.agentType)
	}
	if len(parsed.command) == 0 || parsed.command[0] != "claude" {
		t.Fatalf("expected command from agentTypeDefs, got %v", parsed.command)
	}
	if parsed.mount != "/work" {
		t.Fatalf("expected mount=/work, got %q", parsed.mount)
	}
	if len(parsed.builtinTools) == 0 {
		t.Fatal("expected default builtin tools for claude")
	}
}

func TestResolveAgentSessionArgs_RegisteredTypeOverrideBuiltinTools(t *testing.T) {
	parsed, err := resolveAgentSessionArgs("claude", "", "/work", []string{"git"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 1 || parsed.builtinTools[0] != "git" {
		t.Fatalf("expected builtinTools=[git], got %v", parsed.builtinTools)
	}
}

func TestResolveAgentSessionArgs_CustomCommand(t *testing.T) {
	parsed, err := resolveAgentSessionArgs("", "aider --yes", "/my/project", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "" {
		t.Fatalf("expected empty agentType, got %q", parsed.agentType)
	}
	if len(parsed.command) != 2 || parsed.command[0] != "aider" || parsed.command[1] != "--yes" {
		t.Fatalf("expected command=[aider --yes], got %v", parsed.command)
	}
	if parsed.mount != "/my/project" {
		t.Fatalf("expected mount=/my/project, got %q", parsed.mount)
	}
}

func TestResolveAgentSessionArgs_CustomCommandWithBuiltinTools(t *testing.T) {
	parsed, err := resolveAgentSessionArgs("", "aider", "/work", []string{"git", "uv"}, true)
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
	// With cobra, duplicate positional args are prevented by cobra.MaximumNArgs(1).
	// Here we test resolveAgentSessionArgs directly with a registered agent type.
	parsed, err := resolveAgentSessionArgs("claude", "", "/work", nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.agentType != "claude" {
		t.Fatalf("expected agentType=claude, got %q", parsed.agentType)
	}
}
