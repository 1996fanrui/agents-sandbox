package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
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
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: tmpDir, workspaceOverridden: true}, "claude")
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
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: tmpDir, workspaceOverridden: true, builtinTools: []string{"git"}, builtinToolsOverridden: true}, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 1 || parsed.builtinTools[0] != "git" {
		t.Fatalf("expected builtinTools=[git], got %v", parsed.builtinTools)
	}
}

func TestResolveAgentSessionArgs_CustomCommand(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "aider --yes", workspace: tmpDir, workspaceOverridden: true}, "")
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
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "aider", workspace: tmpDir, workspaceOverridden: true, builtinTools: []string{"git", "uv"}, builtinToolsOverridden: true}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.builtinTools) != 2 || parsed.builtinTools[0] != "git" || parsed.builtinTools[1] != "uv" {
		t.Fatalf("expected builtinTools=[git uv], got %v", parsed.builtinTools)
	}
}

func TestResolveAgentSessionArgs_CommandFlag_NotMutuallyExclusive(t *testing.T) {
	// AT-C3: --command + agent type is NOW allowed.
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "aider", workspace: tmpDir, workspaceOverridden: true}, "claude")
	if err != nil {
		t.Fatalf("expected no error for agent type + --command, got: %v", err)
	}
	// rawCommand should override the type's default command.
	if len(parsed.command) != 1 || parsed.command[0] != "aider" {
		t.Fatalf("expected command=[aider], got %v", parsed.command)
	}
	// agentType is still set.
	if parsed.agentType != "claude" {
		t.Fatalf("expected agentType=claude, got %q", parsed.agentType)
	}
}

func TestResolveAgentSessionArgs_NeitherTypeNorCommand(t *testing.T) {
	_, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: "/work", workspaceOverridden: true}, "")
	if err == nil {
		t.Fatal("expected error when --command is missing on agbox agent")
	}
	if !strings.Contains(err.Error(), "requires --command") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_UnknownType(t *testing.T) {
	_, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: "/work", workspaceOverridden: true}, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_EmptyCommand(t *testing.T) {
	_, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "  ", workspace: "/work", workspaceOverridden: true}, "")
	if err == nil {
		t.Fatal("expected error for empty --command")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_RejectRoot(t *testing.T) {
	_, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: "/", workspaceOverridden: true}, "claude")
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
	_, err = resolveAgentSessionArgs(&agentSessionFlagVars{workspace: home, workspaceOverridden: true}, "claude")
	if err == nil {
		t.Fatal("expected error for home directory workspace")
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolveAgentSessionArgs_Mode(t *testing.T) {
	tmpDir := realTempDir(t)

	t.Run("claude_default_interactive", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{workspace: tmpDir, workspaceOverridden: true}, "claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.mode != agentModeInteractive {
			t.Fatalf("expected mode=interactive, got %q", parsed.mode)
		}
	})

	t.Run("claude_override_long_running", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{mode: "long-running", modeOverridden: true, workspace: tmpDir, workspaceOverridden: true}, "claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.mode != agentModeLongRunning {
			t.Fatalf("expected mode=long-running, got %q", parsed.mode)
		}
	})

	t.Run("command_default_interactive", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity", workspace: tmpDir, workspaceOverridden: true}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.mode != agentModeInteractive {
			t.Fatalf("expected mode=interactive, got %q", parsed.mode)
		}
	})

	t.Run("command_override_long_running", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity", mode: "long-running", modeOverridden: true, workspace: tmpDir, workspaceOverridden: true}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.mode != agentModeLongRunning {
			t.Fatalf("expected mode=long-running, got %q", parsed.mode)
		}
	})

	t.Run("invalid_mode", func(t *testing.T) {
		_, err := resolveAgentSessionArgs(&agentSessionFlagVars{mode: "invalid", modeOverridden: true, workspace: tmpDir, workspaceOverridden: true}, "claude")
		if err == nil {
			t.Fatal("expected error for invalid mode")
		}
		if !strings.Contains(err.Error(), "--mode must be") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

func TestResolveAgentSessionArgs_ModeOverride(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{mode: "long-running", modeOverridden: true, workspace: tmpDir, workspaceOverridden: true}, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.mode != agentModeLongRunning {
		t.Fatalf("expected mode=long-running, got %q", parsed.mode)
	}
}

func TestResolveAgentSessionArgs_WorkspaceCopy(t *testing.T) {
	t.Run("claude_default_workspace_is_cwd", func(t *testing.T) {
		// When no --workspace is given, registered type with copyWorkspace=true fills cwd.
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "claude")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.workspace == "" {
			t.Fatal("expected non-empty workspace for claude without --workspace")
		}
	})

	t.Run("command_no_workspace", func(t *testing.T) {
		// Custom --command without --workspace: workspace stays empty.
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.workspace != "" {
			t.Fatalf("expected empty workspace for custom command, got %q", parsed.workspace)
		}
	})

	t.Run("command_with_explicit_workspace", func(t *testing.T) {
		tmpDir := realTempDir(t)
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity", workspace: tmpDir, workspaceOverridden: true}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.workspace != tmpDir {
			t.Fatalf("expected workspace=%s, got %q", tmpDir, parsed.workspace)
		}
	})
}

func TestResolveAgentSessionArgs_WorkspaceExplicit(t *testing.T) {
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity", workspace: tmpDir, workspaceOverridden: true}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.workspace != tmpDir {
		t.Fatalf("expected workspace=%s, got %q", tmpDir, parsed.workspace)
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

func TestResolveAgentSessionArgs_Openclaw(t *testing.T) {
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.mode != agentModeLongRunning {
		t.Fatalf("expected mode=long-running, got %q", parsed.mode)
	}
	expectedTools := []string{"git", "npm", "uv", "apt"}
	if len(parsed.builtinTools) != len(expectedTools) {
		t.Fatalf("expected builtinTools=%v, got %v", expectedTools, parsed.builtinTools)
	}
	for i, tool := range expectedTools {
		if parsed.builtinTools[i] != tool {
			t.Fatalf("builtinTools[%d]: expected %q, got %q", i, tool, parsed.builtinTools[i])
		}
	}
	if parsed.configYaml == "" {
		t.Fatal("expected non-empty configYaml")
	}
	// configYaml should contain the image.
	if !strings.Contains(parsed.configYaml, "image:") {
		t.Fatalf("expected configYaml to contain image:, got:\n%s", parsed.configYaml)
	}
	// image should be empty because configYaml specifies it.
	if parsed.image != "" {
		t.Fatalf("expected empty image (configYaml provides it), got %q", parsed.image)
	}

	// openclaw typedef does not set copyWorkspace, so workspace should be empty.
	if parsed.workspace != "" {
		t.Fatalf("expected empty workspace, got %q", parsed.workspace)
	}
}

func TestResolveAgentSessionArgs_SandboxID(t *testing.T) {
	t.Run("auto_generated", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		re := regexp.MustCompile(`^openclaw-[0-9a-f]{6}$`)
		if !re.MatchString(parsed.sandboxID) {
			t.Fatalf("expected sandboxID matching %s, got %q", re.String(), parsed.sandboxID)
		}
	})

	t.Run("custom_command_no_generator", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{rawCommand: "sleep infinity"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.sandboxID != "" {
			t.Fatalf("expected empty sandboxID, got %q", parsed.sandboxID)
		}
	})
}

func TestResolveAgentSessionArgs_ConfigYaml(t *testing.T) {
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.configYaml == "" {
		t.Fatal("expected non-empty configYaml for openclaw")
	}
	for _, keyword := range []string{"mounts:", "ports:", "OPENCLAW_STATE_DIR"} {
		if !strings.Contains(parsed.configYaml, keyword) {
			t.Fatalf("configYaml should contain %q, got:\n%s", keyword, parsed.configYaml)
		}
	}
}

func TestTopLevelCommandRejectsPositionalArgs(t *testing.T) {
	cmd := newAgentTypeCommand("claude")
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"some-extra-arg"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for positional arg on top-level command")
	}
}

// TestAgentCommandRejectsPositionalAgentType ensures `agbox agent <type>`
// is no longer supported — callers must use the top-level per-type command.
func TestAgentCommandRejectsPositionalAgentType(t *testing.T) {
	cmd := newAgentCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"claude"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when positional agent type is passed to `agbox agent`")
	}
}

func TestTopLevelCommandAllowsCommandFlag(t *testing.T) {
	// --command is now allowed with top-level agent type commands (overrides default command).
	cmd := newAgentTypeCommand("claude")
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--command", "foo"})
	// The command will fail due to no daemon, but NOT due to mutual exclusion.
	err := cmd.Execute()
	if err != nil && strings.Contains(err.Error(), "cannot use --command with agent type") {
		t.Fatalf("--command should no longer be rejected with agent type: %v", err)
	}
}

func TestResolveAgentSessionArgsFlags(t *testing.T) {
	t.Run("single_env", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			envs:       []string{"KEY=VAL"},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(parsed.envs) != 1 || parsed.envs["KEY"] != "VAL" {
			t.Fatalf("expected envs={KEY:VAL}, got %v", parsed.envs)
		}
	})

	t.Run("env_last_wins", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			envs:       []string{"KEY=V1", "KEY=V2"},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.envs["KEY"] != "V2" {
			t.Fatalf("expected last-wins KEY=V2, got %q", parsed.envs["KEY"])
		}
	})

	t.Run("env_empty_value", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			envs:       []string{"KEY="},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := parsed.envs["KEY"]; !ok || v != "" {
			t.Fatalf("expected envs[KEY]=\"\", got %q (ok=%v)", v, ok)
		}
	})

	t.Run("env_empty_key", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			envs:       []string{"=VAL"},
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := parsed.envs[""]; !ok || v != "VAL" {
			t.Fatalf("expected envs[\"\"]=\"VAL\", got %q (ok=%v)", v, ok)
		}
	})

	t.Run("env_no_equals", func(t *testing.T) {
		_, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			envs:       []string{"BAD_NO_EQ"},
		}, "")
		if err == nil {
			t.Fatal("expected error for env without =")
		}
		if !strings.Contains(err.Error(), "--env") {
			t.Fatalf("expected error mentioning --env, got %v", err)
		}
	})

	t.Run("cpu_limit", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			cpuLimit:   "2",
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.cpuLimit != "2" {
			t.Fatalf("expected cpuLimit=2, got %q", parsed.cpuLimit)
		}
	})

	t.Run("memory_limit", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand:  "sleep infinity",
			memoryLimit: "4g",
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.memoryLimit != "4g" {
			t.Fatalf("expected memoryLimit=4g, got %q", parsed.memoryLimit)
		}
	})

	t.Run("disk_limit", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			diskLimit:  "10g",
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.diskLimit != "10g" {
			t.Fatalf("expected diskLimit=10g, got %q", parsed.diskLimit)
		}
	})

	t.Run("sandbox_id_override", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
			sandboxID:  "custom-abc",
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.sandboxID != "custom-abc" {
			t.Fatalf("expected sandboxID=custom-abc, got %q", parsed.sandboxID)
		}
	})

	t.Run("sandbox_id_empty_uses_generator", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			sandboxID: "",
		}, "openclaw")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		re := regexp.MustCompile(`^openclaw-[0-9a-f]{6}$`)
		if !re.MatchString(parsed.sandboxID) {
			t.Fatalf("expected sandboxID matching %s, got %q", re.String(), parsed.sandboxID)
		}
	})

	t.Run("sandbox_id_overrides_generator", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			sandboxID: "custom-id",
		}, "openclaw")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.sandboxID != "custom-id" {
			t.Fatalf("expected sandboxID=custom-id, got %q", parsed.sandboxID)
		}
	})

	t.Run("no_sandbox_id_openclaw_uses_generator", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		re := regexp.MustCompile(`^openclaw-[0-9a-f]{6}$`)
		if !re.MatchString(parsed.sandboxID) {
			t.Fatalf("expected sandboxID matching %s, got %q", re.String(), parsed.sandboxID)
		}
	})

	t.Run("no_new_flags", func(t *testing.T) {
		parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
			rawCommand: "sleep infinity",
		}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed.envs != nil {
			t.Fatalf("expected nil envs, got %v", parsed.envs)
		}
		if parsed.cpuLimit != "" {
			t.Fatalf("expected empty cpuLimit, got %q", parsed.cpuLimit)
		}
		if parsed.memoryLimit != "" {
			t.Fatalf("expected empty memoryLimit, got %q", parsed.memoryLimit)
		}
		if parsed.diskLimit != "" {
			t.Fatalf("expected empty diskLimit, got %q", parsed.diskLimit)
		}
		if parsed.sandboxID != "" {
			t.Fatalf("expected empty sandboxID, got %q", parsed.sandboxID)
		}
	})
}

func TestTopLevelCommandsInheritNewFlags(t *testing.T) {
	for _, agentType := range []string{"claude", "codex", "openclaw"} {
		t.Run(agentType, func(t *testing.T) {
			parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
				envs:        []string{"FOO=bar"},
				cpuLimit:    "2",
				memoryLimit: "4g",
				diskLimit:   "10g",
			}, agentType)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if parsed.envs["FOO"] != "bar" {
				t.Fatalf("expected envs[FOO]=bar, got %v", parsed.envs)
			}
			if parsed.cpuLimit != "2" {
				t.Fatalf("expected cpuLimit=2, got %q", parsed.cpuLimit)
			}
			if parsed.memoryLimit != "4g" {
				t.Fatalf("expected memoryLimit=4g, got %q", parsed.memoryLimit)
			}
			if parsed.diskLimit != "10g" {
				t.Fatalf("expected diskLimit=10g, got %q", parsed.diskLimit)
			}
		})
	}
}

func TestResolveAgentSessionArgs_CommandFlag_Interactive(t *testing.T) {
	// AT-C1: --command in interactive mode replaces TTY command.
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		rawCommand: "my-custom-agent --flag",
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.mode != agentModeInteractive {
		t.Fatalf("expected mode=interactive, got %q", parsed.mode)
	}
	if len(parsed.command) != 2 || parsed.command[0] != "my-custom-agent" || parsed.command[1] != "--flag" {
		t.Fatalf("expected command=[my-custom-agent --flag], got %v", parsed.command)
	}

	// Also test with registered type + --command override.
	parsed2, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		rawCommand:          "custom-cmd",
		workspace:           tmpDir,
		workspaceOverridden: true,
	}, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed2.mode != agentModeInteractive {
		t.Fatalf("expected mode=interactive, got %q", parsed2.mode)
	}
	if len(parsed2.command) != 1 || parsed2.command[0] != "custom-cmd" {
		t.Fatalf("expected command=[custom-cmd], got %v", parsed2.command)
	}
}

func TestResolveAgentSessionArgs_CommandFlag_LongRunning(t *testing.T) {
	// AT-C2: --command in long-running mode replaces container primary command.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		rawCommand:     "my-service start",
		mode:           "long-running",
		modeOverridden: true,
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.mode != agentModeLongRunning {
		t.Fatalf("expected mode=long-running, got %q", parsed.mode)
	}
	if len(parsed.command) != 2 || parsed.command[0] != "my-service" || parsed.command[1] != "start" {
		t.Fatalf("expected command=[my-service start], got %v", parsed.command)
	}
}

func TestResolveAgentSessionArgs_Image_ConfigYamlWithImage_Empty(t *testing.T) {
	// AT-C4: when configYaml contains image:, parsed.image should be empty.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.image != "" {
		t.Fatalf("expected empty image when configYaml has image:, got %q", parsed.image)
	}
}

func TestResolveAgentSessionArgs_Image_NoConfigYaml_UsesDefault(t *testing.T) {
	// AT-C5: when no configYaml, parsed.image should be defaultImage.
	tmpDir := realTempDir(t)
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		workspace:           tmpDir,
		workspaceOverridden: true,
	}, "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.image != defaultImage {
		t.Fatalf("expected image=%q, got %q", defaultImage, parsed.image)
	}
}

func TestResolveAgentSessionArgs_Image_ConfigYamlWithoutImage_UsesDefault(t *testing.T) {
	// AT-C6: configYaml without image: uses defaultImage.
	// Test with custom command (no configYaml).
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		rawCommand: "sleep infinity",
	}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.image != defaultImage {
		t.Fatalf("expected image=%q, got %q", defaultImage, parsed.image)
	}
}

func TestResolveAgentSessionArgs_EnvsIsolation_NoDefaultEnvInjection(t *testing.T) {
	// AT-EN: envs should only contain user --env values, not configYaml envs.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		envs: []string{"MY_KEY=my_val"},
	}, "openclaw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.envs) != 1 || parsed.envs["MY_KEY"] != "my_val" {
		t.Fatalf("expected envs={MY_KEY:my_val}, got %v", parsed.envs)
	}
	// configYaml envs must NOT leak into parsed.envs.
	if _, found := parsed.envs["OPENCLAW_STATE_DIR"]; found {
		t.Fatal("configYaml envs should NOT be in parsed.envs")
	}
}

func TestResolveAgentSessionArgs_BuiltinToolsIsCopy(t *testing.T) {
	// AT-SB: builtinTools must be a defensive copy.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "openclaw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Mutating parsed.builtinTools must not affect typeDef.
	original := make([]string, len(parsed.builtinTools))
	copy(original, parsed.builtinTools)
	parsed.builtinTools[0] = "MUTATED"

	typeDef := agentTypeDefs["openclaw"]
	if typeDef.builtinTools[0] == "MUTATED" {
		t.Fatal("modifying parsed.builtinTools must not affect agentTypeDefs")
	}
	if typeDef.builtinTools[0] != original[0] {
		t.Fatalf("expected typeDef.builtinTools[0]=%q, got %q", original[0], typeDef.builtinTools[0])
	}
}

func TestResolveAgentSessionArgs_BuiltinToolsIsCopy_Paseo(t *testing.T) {
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "paseo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := range parsed.builtinTools {
		parsed.builtinTools[i] = "CORRUPTED"
	}
	parsed2, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "paseo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed2.builtinTools[0] != "claude" {
		t.Fatalf("agentTypeDefs[paseo].builtinTools polluted: got %q", parsed2.builtinTools[0])
	}
}
