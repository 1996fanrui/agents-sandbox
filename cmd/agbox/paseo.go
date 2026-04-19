package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/1996fanrui/agents-sandbox/internal/profile"
)

var paseoConfigYaml = `image: ghcr.io/agents-sandbox/paseo-runtime:latest
command:
  - /usr/local/bin/paseo
  - daemon
  - start
  - "--listen"
  - "0.0.0.0:6767"
  - "--hostnames"
  - "true"
  - "--foreground"
envs:
  PASEO_DICTATION_ENABLED: "0"
  PASEO_VOICE_MODE_ENABLED: "0"
  OPENCODE_DISABLE_EXTERNAL_SKILLS: "1"
`

func paseoSandboxIDGen() string {
	return "paseo-" + randomHexSuffix(3)
}

// paseoPreFlight filters parsed.builtinTools by checking whether each tool's
// required (non-Optional) mounts exist on the host. Unknown tools are kept
// (daemon will fail-fast). The filtered list may be empty.
func paseoPreFlight(stderr io.Writer, parsed *agentSessionArgs) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	return paseoPreFlightWithHome(stderr, parsed, home)
}

func paseoPreFlightWithHome(stderr io.Writer, parsed *agentSessionArgs, home string) error {
	filtered := parsed.builtinTools[:0]
	for _, tool := range parsed.builtinTools {
		cap, ok := profile.CapabilityByID(tool)
		if !ok {
			// Unknown tool: pass through to daemon for fail-fast.
			filtered = append(filtered, tool)
			continue
		}
		missing := ""
		for _, mid := range cap.MountIDs {
			m, _ := profile.MountByID(mid)
			if m.Optional {
				continue
			}
			hostPath := expandHostPath(m.DefaultHostPath, home)
			if hostPath == "" {
				missing = m.DefaultHostPath
				break
			}
			if _, err := os.Stat(hostPath); err != nil {
				missing = m.DefaultHostPath
				break
			}
		}
		if missing != "" {
			fmt.Fprintf(stderr, "paseo: skipping builtin tool %q: required host path %q not found\n", tool, missing)
			continue
		}
		filtered = append(filtered, tool)
	}
	parsed.builtinTools = filtered
	return nil
}

// expandHostPath resolves ~ to the given home directory and environment variables.
func expandHostPath(p, home string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		p = home + p[1:]
	} else if p == "~" {
		p = home
	} else {
		p = os.ExpandEnv(p)
	}
	return p
}

// paseoReadyMessageFactory returns a readyMessage closure that captures the
// filtered builtin tools list. The factory makes a defensive copy of activeTools
// to prevent caller mutation from affecting the closure.
func paseoReadyMessageFactory(activeTools []string) func(sandboxID, containerName string) string {
	clone := append([]string(nil), activeTools...)
	return func(sandboxID, containerName string) string {
		toolsLine := "(none)"
		if len(clone) > 0 {
			toolsLine = strings.Join(clone, ", ")
		}
		return fmt.Sprintf(`
Paseo daemon is running.
  Pair URL:   agbox paseo url %s
  Active builtin tools: %s

Manage:
  agbox sandbox stop %s      # stop sandbox
  agbox sandbox resume %s    # restart container (primary command restarts with it)
  agbox sandbox delete %s    # delete sandbox
  agbox exec list %s         # list running execs
`, sandboxID, toolsLine, sandboxID, sandboxID, sandboxID, sandboxID)
	}
}
