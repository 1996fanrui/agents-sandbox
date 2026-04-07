package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// randomHexSuffix generates n random bytes and returns them as 2*n hex characters.
func randomHexSuffix(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// openclawSandboxIDGen returns a sandbox ID with prefix "openclaw-" followed by 4 hex characters.
func openclawSandboxIDGen() string {
	return "openclaw-" + randomHexSuffix(2)
}

// openclawConfigYaml is the embedded YAML config for the openclaw sandbox.
// The daemon handles ~ expansion for mounts but NOT for envs, so env values
// use the absolute /home/agbox path.
const openclawConfigYaml = `mounts:
  - source: "~/.openclaw"
    target: "~/.openclaw"
    writable: true
ports:
  - host_port: 18789
    container_port: 18789
envs:
  OPENCLAW_STATE_DIR: "/home/agbox/.openclaw"
  OPENCLAW_CONFIG_PATH: "/home/agbox/.openclaw/config/openclaw.json"
  PATH: "/home/agbox/.npm-global/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
`

// openclawAuthGuide is printed to stderr when auth validation fails.
const openclawAuthGuide = `OpenClaw LLM auth not found. Complete authentication on the host first:
  Codex OAuth:      OPENCLAW_STATE_DIR=~/.openclaw openclaw models auth login --provider openai-codex
  API Key:          OPENCLAW_STATE_DIR=~/.openclaw openclaw models auth add --provider openai --api-key <key>
  GitHub Copilot:   OPENCLAW_STATE_DIR=~/.openclaw openclaw models auth login-github-copilot
`

// openclawPreFlight verifies that LLM auth profiles exist on the host.
// Gateway config initialization is handled inside the sandbox via `openclaw onboard`
// to avoid coupling with OpenClaw's config schema.
func openclawPreFlight(stderr io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	return openclawPreFlightWithHome(stderr, home)
}

// openclawPreFlightWithHome is the testable core of openclawPreFlight that accepts
// the home directory as a parameter instead of resolving it via os.UserHomeDir().
func openclawPreFlightWithHome(stderr io.Writer, home string) error {
	authPath := filepath.Join(home, ".openclaw", "agents", "main", "agent", "auth-profiles.json")
	authData, err := os.ReadFile(authPath)
	if err != nil {
		_, _ = fmt.Fprint(stderr, openclawAuthGuide)
		return fmt.Errorf("openclaw auth profiles not found: %w", err)
	}

	var authFile struct {
		Profiles map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(authData, &authFile); err != nil {
		_, _ = fmt.Fprint(stderr, openclawAuthGuide)
		return fmt.Errorf("openclaw auth profiles invalid JSON: %w", err)
	}
	if len(authFile.Profiles) == 0 {
		_, _ = fmt.Fprint(stderr, openclawAuthGuide)
		return fmt.Errorf("openclaw auth profiles are empty")
	}

	return nil
}

// readOpenclawGatewayToken reads the gateway auth token from the openclaw config file.
// Returns empty string if the file does not exist or cannot be parsed.
func readOpenclawGatewayToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".openclaw", "config", "openclaw.json"))
	if err != nil {
		return ""
	}
	var config struct {
		Gateway struct {
			Auth struct {
				Token string `json:"token"`
			} `json:"auth"`
		} `json:"gateway"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return ""
	}
	return config.Gateway.Auth.Token
}

// openclawReadyMessage returns the message displayed after the openclaw sandbox is ready.
func openclawReadyMessage(sandboxID, containerName string) string {
	gatewayURL := "http://localhost:18789"
	if token := readOpenclawGatewayToken(); token != "" {
		gatewayURL += "/#token=" + token
	}
	return fmt.Sprintf(`
OpenClaw gateway is running.
  Gateway:    %s
  Sandbox ID: %s

Manage:
  agbox sandbox stop %s      # stop gateway
  agbox sandbox resume %s    # restart container only (gateway process lost)
  # To redeploy after resume: delete and recreate
  agbox sandbox delete %s && agbox agent openclaw
  agbox sandbox delete %s    # delete sandbox
  agbox exec list %s         # list running execs
`, gatewayURL, sandboxID, sandboxID, sandboxID, sandboxID, sandboxID, sandboxID)
}
