package main

import (
	"os"
	"strings"
	"testing"
)

func TestLongRunningDocsUpdated(t *testing.T) {
	// AT-DF: verify 4 docs files contain updated content about the new
	// long-running model (no CreateExec, container primary command).
	tests := []struct {
		path string
		want []string
	}{
		{
			"../../docs/sandbox_container_lifecycle.md",
			[]string{
				"primary command",
				"READY",
			},
		},
		{
			"../../docs/container_dependency_strategy.md",
			[]string{
				"primary command",
				"Does not use `CreateExec` for the service process",
			},
		},
		{
			"../../docs/agent_guide.md",
			[]string{
				"openclaw",
				"paseo",
				"container primary command",
			},
		},
		{
			"../../docs/cli_reference.md",
			[]string{
				"--command",
				"container primary command",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			data, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			content := string(data)
			for _, want := range tt.want {
				if !strings.Contains(content, want) {
					t.Fatalf("%s missing %q", tt.path, want)
				}
			}
		})
	}
}
