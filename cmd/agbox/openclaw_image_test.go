package main

import (
	"os"
	"strings"
	"testing"
)

func TestOpenclawRuntimeDockerfile_Structure(t *testing.T) {
	// AT-I1: verify Dockerfile exists and contains expected directives.
	data, err := os.ReadFile("../../images/openclaw-runtime/Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		"FROM ghcr.io/agents-sandbox/coding-runtime:latest",
		"ARG OPENCLAW_VERSION",
		"ENTRYPOINT",
		"CMD",
		"openclaw-entrypoint.sh",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}
