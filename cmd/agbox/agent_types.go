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

// agentTypeDef defines the container-internal command and the builtin tools for an agent type.
type agentTypeDef struct {
	mode          agentMode
	command       []string
	builtinTools  []string
	copyWorkspace bool
	confirmGit    bool
	configYaml    string                                                 // embedded YAML config string, passed to CreateSandboxRequest.ConfigYaml
	sandboxIDGen  func() string                                          // custom sandbox ID generator; nil means daemon auto-generates
	preFlight     func(stderr io.Writer, parsed *agentSessionArgs) error // pre-flight check; nil means no check
	readyMessage  func(sandboxID, containerName string) string           // custom ready message; nil uses default output
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
		readyMessage: openclawReadyMessage,
	},
	"paseo": {
		mode:         agentModeLongRunning,
		builtinTools: []string{"claude", "codex", "npm", "uv", "apt", "opencode"},
		configYaml:   paseoConfigYaml,
		sandboxIDGen: paseoSandboxIDGen,
		preFlight:    paseoPreFlight,
		// readyMessage injected by runLongRunningSession after fetching the pair URL.
	},
}
