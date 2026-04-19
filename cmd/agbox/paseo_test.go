package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"gopkg.in/yaml.v3"
)

// --- Config YAML tests ---

func TestPaseoConfigYaml_ParsesImageCommandEnvs(t *testing.T) {
	// AT-P1: yaml.Unmarshal, check image/command/envs/no mounts.
	var cfg struct {
		Image   string            `yaml:"image"`
		Command []string          `yaml:"command"`
		Envs    map[string]string `yaml:"envs"`
		Mounts  []any             `yaml:"mounts"`
		Ports   []any             `yaml:"ports"`
	}
	if err := yaml.Unmarshal([]byte(paseoConfigYaml), &cfg); err != nil {
		t.Fatalf("failed to parse paseoConfigYaml: %v", err)
	}

	if !strings.HasPrefix(cfg.Image, "ghcr.io/agents-sandbox/paseo-runtime:") {
		t.Fatalf("unexpected image: %q", cfg.Image)
	}

	if len(cfg.Command) == 0 || cfg.Command[0] != "/usr/local/bin/paseo" {
		t.Fatalf("unexpected command: %v", cfg.Command)
	}
	if !containsStr(cfg.Command, "daemon") || !containsStr(cfg.Command, "start") {
		t.Fatalf("command missing daemon/start: %v", cfg.Command)
	}

	if cfg.Envs["PASEO_DICTATION_ENABLED"] != "0" {
		t.Fatalf("expected PASEO_DICTATION_ENABLED=0, got %q", cfg.Envs["PASEO_DICTATION_ENABLED"])
	}
	if cfg.Envs["PASEO_VOICE_MODE_ENABLED"] != "0" {
		t.Fatalf("expected PASEO_VOICE_MODE_ENABLED=0, got %q", cfg.Envs["PASEO_VOICE_MODE_ENABLED"])
	}
	if cfg.Envs["OPENCODE_DISABLE_EXTERNAL_SKILLS"] != "1" {
		t.Fatalf("expected OPENCODE_DISABLE_EXTERNAL_SKILLS=1, got %q", cfg.Envs["OPENCODE_DISABLE_EXTERNAL_SKILLS"])
	}

	if len(cfg.Mounts) != 0 {
		t.Fatalf("expected no mounts, got %v", cfg.Mounts)
	}
	if len(cfg.Ports) != 0 {
		t.Fatalf("expected no ports, got %v", cfg.Ports)
	}
}

func TestPaseoSandboxIDGen_Format(t *testing.T) {
	// AT-P3: regex ^paseo-[0-9a-f]{6}$.
	re := regexp.MustCompile(`^paseo-[0-9a-f]{6}$`)
	for i := 0; i < 10; i++ {
		id := paseoSandboxIDGen()
		if !re.MatchString(id) {
			t.Fatalf("expected sandbox ID matching %s, got %q", re.String(), id)
		}
	}
}

// --- PreFlight tests ---

// setupPaseoHome creates a temp home with the specified paths.
// paths are relative to home (e.g. ".claude", ".claude.json", ".codex").
// Files ending in .json are created as files; others as directories.
func setupPaseoHome(t *testing.T, paths ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(home, p)
		if strings.HasSuffix(p, ".json") {
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home
}

func defaultPaseoTools() []string {
	return []string{"claude", "codex", "npm", "uv", "apt", "opencode"}
}

func TestPaseoPreFlight_ClaudeDirMissing(t *testing.T) {
	// AT-P4: .claude.json present, .codex present, but .claude missing.
	home := setupPaseoHome(t, ".claude.json", ".codex")
	tools := defaultPaseoTools()
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// claude should be filtered out (missing .claude dir).
	for _, tool := range parsed.builtinTools {
		if tool == "claude" {
			t.Fatal("claude should have been filtered out (missing .claude dir)")
		}
	}
	// codex should remain.
	if !containsStr(parsed.builtinTools, "codex") {
		t.Fatalf("codex should remain, got %v", parsed.builtinTools)
	}
	if !strings.Contains(stderr.String(), "skipping builtin tool \"claude\"") {
		t.Fatalf("expected skip message for claude, got: %s", stderr.String())
	}
}

func TestPaseoPreFlight_ClaudeJSONMissing(t *testing.T) {
	// AT-P5: .claude dir present, .codex present, but .claude.json missing.
	home := setupPaseoHome(t, ".claude", ".codex")
	tools := defaultPaseoTools()
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tool := range parsed.builtinTools {
		if tool == "claude" {
			t.Fatal("claude should have been filtered out (missing .claude.json)")
		}
	}
	if !containsStr(parsed.builtinTools, "codex") {
		t.Fatalf("codex should remain, got %v", parsed.builtinTools)
	}
}

func TestPaseoPreFlight_CodexDirMissing(t *testing.T) {
	// AT-P6: .claude and .claude.json present, but .codex missing.
	home := setupPaseoHome(t, ".claude", ".claude.json")
	tools := defaultPaseoTools()
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !containsStr(parsed.builtinTools, "claude") {
		t.Fatalf("claude should remain, got %v", parsed.builtinTools)
	}
	for _, tool := range parsed.builtinTools {
		if tool == "codex" {
			t.Fatal("codex should have been filtered out (missing .codex dir)")
		}
	}
	if !strings.Contains(stderr.String(), "skipping builtin tool \"codex\"") {
		t.Fatalf("expected skip message for codex, got: %s", stderr.String())
	}
}

func TestPaseoPreFlight_AllNonOptionalPresent(t *testing.T) {
	// AT-P7: all non-optional paths present → all 6 tools kept.
	home := setupPaseoHome(t, ".claude", ".claude.json", ".codex")
	tools := defaultPaseoTools()
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(parsed.builtinTools) != 6 {
		t.Fatalf("expected 6 tools, got %d: %v", len(parsed.builtinTools), parsed.builtinTools)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no skip messages, got: %s", stderr.String())
	}
}

func TestPaseoPreFlight_OptionalOnlyToolsNeverFiltered(t *testing.T) {
	// AT-P8: empty HOME, only optional-mount tools → never filtered.
	home := t.TempDir() // empty
	tools := []string{"npm", "uv", "apt", "opencode"}
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(parsed.builtinTools) != 4 {
		t.Fatalf("expected 4 tools, got %d: %v", len(parsed.builtinTools), parsed.builtinTools)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no skip messages, got: %s", stderr.String())
	}
}

func TestPaseoPreFlight_UnknownToolKept(t *testing.T) {
	// AT-P9: unknown tool "foo" preserved.
	home := t.TempDir()
	tools := []string{"foo"}
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(parsed.builtinTools) != 1 || parsed.builtinTools[0] != "foo" {
		t.Fatalf("expected [foo], got %v", parsed.builtinTools)
	}
}

func TestPaseoPreFlight_EmptyAfterFilter(t *testing.T) {
	// AT-PA: empty HOME, only claude+codex → empty list.
	home := t.TempDir() // empty
	tools := []string{"claude", "codex"}
	parsed := &agentSessionArgs{builtinTools: tools}
	var stderr bytes.Buffer

	err := paseoPreFlightWithHome(&stderr, parsed, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(parsed.builtinTools) != 0 {
		t.Fatalf("expected empty tools, got %v", parsed.builtinTools)
	}
	if !strings.Contains(stderr.String(), "claude") || !strings.Contains(stderr.String(), "codex") {
		t.Fatalf("expected skip messages for claude and codex, got: %s", stderr.String())
	}
}

// --- resolveAgentSessionArgs tests ---

func TestResolveAgentSessionArgs_Paseo(t *testing.T) {
	// AT-PB: mode, configYaml, builtinTools, sandboxID prefix.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "paseo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if parsed.mode != agentModeLongRunning {
		t.Fatalf("expected mode=long-running, got %q", parsed.mode)
	}

	expectedTools := []string{"claude", "codex", "npm", "uv", "apt", "opencode"}
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
	if !strings.Contains(parsed.configYaml, "image:") {
		t.Fatalf("expected configYaml to contain image:, got:\n%s", parsed.configYaml)
	}
	// image should be empty because configYaml specifies it.
	if parsed.image != "" {
		t.Fatalf("expected empty image (configYaml provides it), got %q", parsed.image)
	}

	// sandboxID should match paseo-XXXX.
	re := regexp.MustCompile(`^paseo-[0-9a-f]{6}$`)
	if !re.MatchString(parsed.sandboxID) {
		t.Fatalf("expected sandboxID matching %s, got %q", re.String(), parsed.sandboxID)
	}

	// paseo does not copy workspace.
	if parsed.workspace != "" {
		t.Fatalf("expected empty workspace, got %q", parsed.workspace)
	}
}

// --- runAgentSession / runLongRunningSession tests ---

func TestRunAgentSession_Paseo_ReadyMessageIncludesFilteredTools(t *testing.T) {
	// AT-PC: fake client, observe stderr for active builtin tools.
	home := setupPaseoHome(t, ".claude", ".claude.json") // codex missing → filtered
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{}, "paseo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate the preFlight and factory injection that runAgentSession does.
	var preFlightStderr bytes.Buffer
	if err := paseoPreFlightWithHome(&preFlightStderr, &parsed, home); err != nil {
		t.Fatalf("preFlight error: %v", err)
	}
	parsed.readyMessage = paseoReadyMessageFactory(parsed.builtinTools)

	mock := newReadyOnlyMock()
	var stdout, stderr bytes.Buffer
	err = runLongRunningSession(context.Background(), mock, parsed, "paseo", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "Paseo daemon is running") {
		t.Fatalf("stderr missing Paseo readyMessage, got:\n%s", stderrStr)
	}
	if !strings.Contains(stderrStr, "claude") {
		t.Fatalf("stderr should contain claude in active tools, got:\n%s", stderrStr)
	}
	// codex was filtered out, should not appear in tools line.
	// But "codex" could appear in "agbox sandbox..." commands - check the tools line specifically.
	if strings.Contains(stderrStr, "Active builtin tools: ") {
		toolsLineStart := strings.Index(stderrStr, "Active builtin tools: ")
		toolsLineEnd := strings.Index(stderrStr[toolsLineStart:], "\n")
		toolsLine := stderrStr[toolsLineStart : toolsLineStart+toolsLineEnd]
		if strings.Contains(toolsLine, "codex") {
			t.Fatalf("codex should have been filtered from active tools, got line: %s", toolsLine)
		}
	}
}

// --- Ready message factory tests ---

func TestPaseoReadyMessageFactory_WithTools(t *testing.T) {
	// AT-PD: factory with tools.
	factory := paseoReadyMessageFactory([]string{"claude", "npm", "uv"})
	msg := factory("paseo-abc0", "agbox-primary-paseo-abc0")

	if !strings.Contains(msg, "Paseo daemon is running") {
		t.Fatalf("missing Paseo header, got:\n%s", msg)
	}
	if !strings.Contains(msg, "agbox paseo url paseo-abc0") {
		t.Fatalf("missing pair URL command, got:\n%s", msg)
	}
	if !strings.Contains(msg, "claude, npm, uv") {
		t.Fatalf("expected tools list, got:\n%s", msg)
	}
	if !strings.Contains(msg, "agbox sandbox stop paseo-abc0") {
		t.Fatalf("missing stop command, got:\n%s", msg)
	}
}

func TestPaseoReadyMessageFactory_EmptyTools(t *testing.T) {
	// AT-PE: factory with nil/empty.
	for _, input := range [][]string{nil, {}} {
		factory := paseoReadyMessageFactory(input)
		msg := factory("paseo-0001", "c")
		if !strings.Contains(msg, "(none)") {
			t.Fatalf("expected (none) for empty tools, got:\n%s", msg)
		}
	}
}

func TestPaseoReadyMessageFactory_DefensiveCopy(t *testing.T) {
	// AT-PF: mutate input after factory.
	tools := []string{"claude", "npm"}
	factory := paseoReadyMessageFactory(tools)
	tools[0] = "MUTATED"

	msg := factory("x", "y")
	if strings.Contains(msg, "MUTATED") {
		t.Fatal("factory should have made a defensive copy")
	}
	if !strings.Contains(msg, "claude") {
		t.Fatal("factory should still reference original tools")
	}
}

// --- Paseo URL command tests ---

func TestPaseoURLCommand_InvokesCreateExec(t *testing.T) {
	// AT-PG: fake client, check Command.
	var capturedReq *agboxv1.CreateExecRequest
	sandboxID := "paseo-test"

	stdoutDir := t.TempDir()
	stdoutLog := filepath.Join(stdoutDir, "stdout.log")
	if err := os.WriteFile(stdoutLog, []byte("https://paseo.sh/pair/abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAgentClient{
		createExecFn: func(_ context.Context, req *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			capturedReq = req
			return &agboxv1.CreateExecResponse{
				ExecId:        "exec-pair",
				StdoutLogPath: stdoutLog,
			}, nil
		},
		getExecFn: func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            "exec-pair",
					SandboxId:         sandboxID,
					State:             agboxv1.ExecState_EXEC_STATE_FINISHED,
					ExitCode:          0,
					LastEventSequence: 3,
				},
			}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	err := runPaseoURL(context.Background(), mock, sandboxID, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("CreateExec not called")
	}
	if capturedReq.SandboxId != sandboxID {
		t.Fatalf("expected SandboxId=%q, got %q", sandboxID, capturedReq.SandboxId)
	}
	cmd := capturedReq.Command
	if len(cmd) != 3 || cmd[0] != "/usr/local/bin/paseo" || cmd[1] != "daemon" || cmd[2] != "pair" {
		t.Fatalf("expected command=[/usr/local/bin/paseo daemon pair], got %v", cmd)
	}
}

func TestPaseoURLCommand_PrintsStdoutLog(t *testing.T) {
	// AT-PH: fake exec, check stdout.
	expectedURL := "https://paseo.sh/pair/xyz789\n"

	stdoutDir := t.TempDir()
	stdoutLog := filepath.Join(stdoutDir, "stdout.log")
	if err := os.WriteFile(stdoutLog, []byte(expectedURL), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAgentClient{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{
				ExecId:        "exec-pair",
				StdoutLogPath: stdoutLog,
			}, nil
		},
		getExecFn: func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            "exec-pair",
					SandboxId:         "paseo-test",
					State:             agboxv1.ExecState_EXEC_STATE_FINISHED,
					ExitCode:          0,
					LastEventSequence: 3,
				},
			}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	err := runPaseoURL(context.Background(), mock, "paseo-test", &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.String() != expectedURL {
		t.Fatalf("expected stdout=%q, got %q", expectedURL, stdout.String())
	}
}

func TestPaseoURLCommand_NonZeroExit(t *testing.T) {
	// AT-PI: fake exec failed.
	stderrDir := t.TempDir()
	stderrLog := filepath.Join(stderrDir, "stderr.log")
	if err := os.WriteFile(stderrLog, []byte("daemon not running\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockAgentClient{
		createExecFn: func(_ context.Context, _ *agboxv1.CreateExecRequest) (*agboxv1.CreateExecResponse, error) {
			return &agboxv1.CreateExecResponse{
				ExecId:        "exec-pair",
				StderrLogPath: stderrLog,
			}, nil
		},
		getExecFn: func(_ context.Context, _ string) (*agboxv1.GetExecResponse, error) {
			return &agboxv1.GetExecResponse{
				Exec: &agboxv1.ExecStatus{
					ExecId:            "exec-pair",
					SandboxId:         "paseo-test",
					State:             agboxv1.ExecState_EXEC_STATE_FINISHED,
					ExitCode:          1,
					LastEventSequence: 3,
				},
			}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	err := runPaseoURL(context.Background(), mock, "paseo-test", &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for non-zero exit code")
	}
	// stderr log should have been printed.
	if !strings.Contains(stderr.String(), "daemon not running") {
		t.Fatalf("expected stderr to contain exec stderr log, got: %s", stderr.String())
	}
}

func TestPaseoURLCommand_ArgsValidation(t *testing.T) {
	// AT-PJ: 0 args and 2 args.
	t.Run("zero_args", func(t *testing.T) {
		cmd := newPaseoURLCommand()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for zero args")
		}
	})

	t.Run("two_args", func(t *testing.T) {
		cmd := newPaseoURLCommand()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"id1", "id2"})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for two args")
		}
	})
}

func TestTopLevelPaseoInheritsGenericFlags(t *testing.T) {
	// AT-PK: parse flags.
	parsed, err := resolveAgentSessionArgs(&agentSessionFlagVars{
		envs:        []string{"K=V"},
		cpuLimit:    "2",
		memoryLimit: "4g",
		sandboxID:   "paseo-custom",
	}, "paseo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.envs["K"] != "V" {
		t.Fatalf("expected envs[K]=V, got %v", parsed.envs)
	}
	if parsed.cpuLimit != "2" {
		t.Fatalf("expected cpuLimit=2, got %q", parsed.cpuLimit)
	}
	if parsed.memoryLimit != "4g" {
		t.Fatalf("expected memoryLimit=4g, got %q", parsed.memoryLimit)
	}
	if parsed.sandboxID != "paseo-custom" {
		t.Fatalf("expected sandboxID=paseo-custom, got %q", parsed.sandboxID)
	}

	// Verify the paseo command has the expected flags.
	cmd := newPaseoTopLevelCommand()
	for _, flagName := range []string{"env", "cpu-limit", "memory-limit", "disk-limit", "sandbox-id", "builtin-tool", "workspace", "mode", "command"} {
		if cmd.Flags().Lookup(flagName) == nil {
			t.Fatalf("paseo command missing flag %q", flagName)
		}
	}

	// Verify url subcommand is present.
	urlCmd, _, err := cmd.Find([]string{"url"})
	if err != nil || urlCmd == nil || urlCmd.Name() != "url" {
		t.Fatal("paseo command should have a 'url' subcommand")
	}
}

// --- Helpers ---

// containsStr checks if a string slice contains a given string.
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// Verify mockAgentClient satisfies sandboxExecClient at compile time.
var _ sandboxExecClient = (*mockAgentClient)(nil)

// Ensure newReadyOnlyMock is accessible (defined in agent_session_test.go).
var _ = newReadyOnlyMock

// Ensure fmt is used.
var _ = fmt.Sprintf

// Ensure rawclient import is referenced (used by agent_session_test.go mockEventStream).
var _ rawclient.SandboxEventStream = (*mockEventStream)(nil)
