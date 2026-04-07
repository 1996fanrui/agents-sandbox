package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
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
	re := regexp.MustCompile(`^openclaw-[0-9a-f]{4}$`)
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
