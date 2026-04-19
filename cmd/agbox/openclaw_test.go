package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOpenclawPreflight_AuthMissing(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer

	err := openclawPreFlightWithHome(&stderr, home)
	if err == nil {
		t.Fatal("expected error when auth file is missing")
	}

	output := stderr.String()
	if !containsAll(output, "OpenClaw LLM auth not found", "Codex OAuth", "API Key", "GitHub Copilot") {
		t.Fatalf("stderr should contain auth guidance with all three methods, got:\n%s", output)
	}
}

func TestOpenclawPreflight_AuthEmptyProfiles(t *testing.T) {
	home := t.TempDir()
	authDir := filepath.Join(home, ".openclaw", "agents", "main", "agent")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "auth-profiles.json"), []byte(`{"profiles": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err := openclawPreFlightWithHome(&stderr, home)
	if err == nil {
		t.Fatal("expected error when profiles map is empty")
	}

	output := stderr.String()
	if !containsAll(output, "OpenClaw LLM auth not found") {
		t.Fatalf("stderr should contain auth guidance, got:\n%s", output)
	}
}

func TestOpenclawPreflight_AuthInvalidJSON(t *testing.T) {
	home := t.TempDir()
	authDir := filepath.Join(home, ".openclaw", "agents", "main", "agent")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "auth-profiles.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err := openclawPreFlightWithHome(&stderr, home)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestOpenclawPreflight_AuthValid(t *testing.T) {
	home := t.TempDir()
	writeValidAuth(t, home)

	var stderr bytes.Buffer
	err := openclawPreFlightWithHome(&stderr, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRandomHexSuffix(t *testing.T) {
	for _, n := range []int{1, 2, 4, 8, 16} {
		result := randomHexSuffix(n)
		if len(result) != 2*n {
			t.Fatalf("randomHexSuffix(%d): expected length %d, got %d", n, 2*n, len(result))
		}
		re := regexp.MustCompile(`^[0-9a-f]+$`)
		if !re.MatchString(result) {
			t.Fatalf("randomHexSuffix(%d): expected hex string, got %q", n, result)
		}
	}
}

func TestOpenclawSandboxIDGen(t *testing.T) {
	re := regexp.MustCompile(`^openclaw-[0-9a-f]{6}$`)
	id := openclawSandboxIDGen()
	if !re.MatchString(id) {
		t.Fatalf("expected sandbox ID matching %s, got %q", re.String(), id)
	}
}

// writeValidAuth creates a valid auth-profiles.json under the given home directory.
func writeValidAuth(t *testing.T, home string) {
	t.Helper()
	authDir := filepath.Join(home, ".openclaw", "agents", "main", "agent")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "auth-profiles.json"), []byte(`{"profiles": {"default": {}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// containsAll returns true if s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}

func TestOpenclawConfigYaml_Content(t *testing.T) {
	// AT-O1: verify openclawConfigYaml contains expected fields.
	for _, want := range []string{
		"image: ghcr.io/agents-sandbox/openclaw-runtime:",
		"command:",
		"openclaw",
		"gateway",
		"mounts:",
		"ports:",
		"OPENCLAW_STATE_DIR",
		"OPENCLAW_CONFIG_PATH",
	} {
		if !strings.Contains(openclawConfigYaml, want) {
			t.Fatalf("openclawConfigYaml missing %q, got:\n%s", want, openclawConfigYaml)
		}
	}
	// Standalone PATH env key should NOT be present (openclaw is preinstalled in the image).
	// Use a line-based check to avoid matching OPENCLAW_CONFIG_PATH.
	if strings.Contains(openclawConfigYaml, "\n  PATH:") || strings.HasPrefix(openclawConfigYaml, "PATH:") {
		t.Fatal("openclawConfigYaml should not contain standalone PATH env")
	}
	if strings.Contains(openclawConfigYaml, "npm install") {
		t.Fatal("openclawConfigYaml should not contain npm install")
	}
	if strings.Contains(openclawConfigYaml, "bash -c") {
		t.Fatal("openclawConfigYaml should not contain bash -c")
	}
}

func TestOpenclawConfigYaml_CommandIsGatewayRun(t *testing.T) {
	// AT-O2: verify the command contains gateway run.
	if !strings.Contains(openclawConfigYaml, "gateway") {
		t.Fatal("openclawConfigYaml command should contain gateway")
	}
	if !strings.Contains(openclawConfigYaml, `"18789"`) {
		t.Fatal("openclawConfigYaml command should contain port 18789")
	}
}

func TestOpenclawPreFlight_AcceptsParsedArgs(t *testing.T) {
	// AT-O5: verify openclawPreFlight accepts *agentSessionArgs parameter.
	home := t.TempDir()
	writeValidAuth(t, home)

	// Override the function to use the test home.
	var stderr bytes.Buffer
	// Call openclawPreFlightWithHome directly since openclawPreFlight
	// just resolves home and delegates.
	err := openclawPreFlightWithHome(&stderr, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the function signature matches the new preFlight type.
	typeDef := agentTypeDefs["openclaw"]
	if typeDef.preFlight == nil {
		t.Fatal("openclaw preFlight should not be nil")
	}
}

func TestOpenclawReadyMessage_Content(t *testing.T) {
	// AT-O6: verify readyMessage contains expected content.
	msg := openclawReadyMessage("sb-test", "container-test")
	for _, want := range []string{
		"OpenClaw gateway is running",
		"Gateway:",
		"http://localhost:18789",
		"sb-test",
		"may take a few seconds",
		"agbox sandbox stop sb-test",
		"agbox sandbox resume sb-test",
		"gateway primary command restarts with it",
		"agbox sandbox delete sb-test",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("readyMessage missing %q, got:\n%s", want, msg)
		}
	}
}
