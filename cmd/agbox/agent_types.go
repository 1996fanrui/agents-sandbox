package main

import "io"

// agentMode describes how the CLI manages a sandbox session.
type agentMode string

const (
	// agentModeInteractive attaches an interactive TTY to the agent process.
	agentModeInteractive agentMode = "interactive"
	// agentModeLongRunning starts the agent without a TTY and keeps the sandbox alive.
	agentModeLongRunning agentMode = "long-running"
)

// execPhase defines a single phase in a multi-phase startup sequence.
type execPhase struct {
	label   string   // display label (e.g., "Installing openclaw...")
	command []string // exec command for this phase
}

// agentTypeDef defines the container-internal command and the builtin tools for an agent type.
type agentTypeDef struct {
	mode          agentMode
	command       []string
	builtinTools  []string
	copyWorkspace bool
	confirmGit    bool
	configYaml    string                                       // embedded YAML config string, passed to CreateSandboxRequest.ConfigYaml
	sandboxIDGen  func() string                                // custom sandbox ID generator; nil means daemon auto-generates
	preFlight     func(stderr io.Writer) error                 // pre-flight check; nil means no check
	phases        []execPhase                                  // multi-phase startup sequence; non-empty replaces command
	readyMessage  func(sandboxID, containerName string) string // custom ready message; nil uses default output
}

// agentTypeDefs maps agent type names to their full definitions.
var agentTypeDefs = map[string]agentTypeDef{
	"claude": {
		mode:          agentModeInteractive,
		command:       []string{"claude", "--dangerously-skip-permissions"},
		builtinTools:  []string{"claude", "git", "uv", "npm", "apt"},
		copyWorkspace: true,
		confirmGit:    true,
	},
	"codex": {
		mode:          agentModeInteractive,
		command:       []string{"codex", "--dangerously-bypass-approvals-and-sandbox"},
		builtinTools:  []string{"codex", "git", "uv", "npm", "apt"},
		copyWorkspace: true,
		confirmGit:    true,
	},
	"openclaw": {
		mode:         agentModeLongRunning,
		builtinTools: []string{"git", "npm", "uv", "apt"},
		configYaml:   openclawConfigYaml,
		sandboxIDGen: openclawSandboxIDGen,
		preFlight:    openclawPreFlight,
		phases: []execPhase{
			{
				label: "Installing openclaw...",
				command: []string{
					"bash", "-c",
					// Install to user-local prefix because the agbox user lacks root/sudo.
					// PATH is set via configYaml envs so openclaw is available in all execs.
					"npm config set prefix ~/.npm-global && " +
						"npm install -g openclaw@latest",
				},
			},
			{
				label: "Initializing config...",
				command: []string{
					"bash", "-c",
					// Use openclaw's own CLI to generate config rather than hand-writing JSON.
					// This avoids coupling with OpenClaw's config schema and survives upstream upgrades.
					// Only runs when config does not exist yet (onboard is idempotent for existing configs).
					"if [ ! -f ~/.openclaw/config/openclaw.json ]; then " +
						"openclaw onboard --mode local --non-interactive --accept-risk " +
						"--gateway-auth token --gateway-bind lan --gateway-port 18789 " +
						"--no-install-daemon --skip-channels --skip-skills --skip-health --skip-search --skip-ui " +
						"--auth-choice skip && " +
						// dangerouslyDisableDeviceAuth skips per-browser device pairing while keeping
						// token auth. Safe because daemon hardcodes HostIP=127.0.0.1 for port bindings,
						// so the gateway is only reachable from localhost.
						"openclaw config set gateway.controlUi.dangerouslyDisableDeviceAuth true --strict-json; " +
						"fi && " +
						// The sandbox is fully isolated, so elevated (sudo) commands carry no
						// additional risk. Setting elevatedDefault=full makes the agent auto-approve
						// elevated exec without routing through gateway device-pairing approval.
						"openclaw config set agents.defaults.elevatedDefault full",
				},
			},
			{
				label: "Starting gateway...",
				command: []string{
					"bash", "-c",
					"openclaw gateway run --port 18789 --bind lan &\n" +
						"timeout=60; elapsed=0; " +
						"until curl -sf http://localhost:18789/ > /dev/null 2>&1; do " +
						"sleep 1; elapsed=$((elapsed+1)); " +
						"if [ $elapsed -ge $timeout ]; then echo 'Gateway health check timed out' >&2; exit 1; fi; " +
						"done",
				},
			},
		},
		readyMessage: openclawReadyMessage,
	},
}
