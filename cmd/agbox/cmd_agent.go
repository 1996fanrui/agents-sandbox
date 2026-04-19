package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// agentSessionFlagVars holds the raw flag values for an agent session command.
type agentSessionFlagVars struct {
	rawCommand   string
	mode         string
	workspace    string
	builtinTools []string
	envs         []string
	cpuLimit     string
	memoryLimit  string
	diskLimit    string
	sandboxID    string
	// Track which flags were explicitly set by the user:
	modeOverridden         bool
	workspaceOverridden    bool
	builtinToolsOverridden bool
}

// registerAgentSessionFlags registers all agent session flags on a cobra command.
func registerAgentSessionFlags(cmd *cobra.Command, v *agentSessionFlagVars) {
	cmd.Flags().StringVar(&v.rawCommand, "command", "", "Override the agent's default command.\n  interactive mode: replaces the TTY command launched via docker exec.\n  long-running mode: replaces the container primary command (under tini).\nValue is split by whitespace via strings.Fields (no shell quoting).")
	cmd.Flags().StringVar(&v.mode, "mode", "", "Session mode: interactive or long-running (default depends on agent type)")
	cmd.Flags().StringVar(&v.workspace, "workspace", "", "Directory to copy into the sandbox as workspace")
	cmd.Flags().StringArrayVar(&v.builtinTools, "builtin-tool", nil, "Builtin tool to install (repeatable, overrides defaults)")
	cmd.Flags().StringArrayVar(&v.envs, "env", nil, "Environment variable in KEY=VAL form (repeatable)")
	cmd.Flags().StringVar(&v.cpuLimit, "cpu-limit", "", "CPU limit (Docker --cpus format, e.g. 2, 0.5)")
	cmd.Flags().StringVar(&v.memoryLimit, "memory-limit", "", "Memory limit (Docker --memory format, e.g. 4g, 512m)")
	cmd.Flags().StringVar(&v.diskLimit, "disk-limit", "", "Disk limit (Docker --storage-opt size= format, e.g. 10g)")
	cmd.Flags().StringVar(&v.sandboxID, "sandbox-id", "", "Custom sandbox ID (overrides agent type default)")
}

// newPaseoTopLevelCommand builds the top-level `agbox paseo` command with the
// `url` subcommand. Unlike other agent types, paseo has a subcommand tree.
func newPaseoTopLevelCommand() *cobra.Command {
	cmd := newAgentTypeCommand("paseo")
	cmd.AddCommand(newPaseoURLCommand())
	return cmd
}

// buildAgentSessionRunE creates the RunE function for agent session commands.
// agentType is non-empty for top-level per-type commands (agbox claude, etc.)
// and empty for the `agbox agent --command` custom-command command.
func buildAgentSessionRunE(agentType string, v *agentSessionFlagVars) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		v.modeOverridden = cmd.Flags().Changed("mode")
		v.workspaceOverridden = cmd.Flags().Changed("workspace")
		v.builtinToolsOverridden = cmd.Flags().Changed("builtin-tool")
		parsed, err := resolveAgentSessionArgs(v, agentType)
		if err != nil {
			return err
		}
		return runAgentSession(cmd.Context(), parsed, cmd.OutOrStdout(), cmd.ErrOrStderr(), lookupEnvFromCmd(cmd))
	}
}

// agentSessionArgs holds the parsed arguments for an agent session.
type agentSessionArgs struct {
	agentType    string    // pre-registered agent type (empty when --command is used)
	command      []string  // custom command (empty when a registered type is used)
	mode         agentMode // resolved session mode
	workspace    string    // host directory to copy; empty means "don't copy"
	builtinTools []string
	envs         map[string]string
	cpuLimit     string
	memoryLimit  string
	diskLimit    string
	sandboxID    string                                       // custom sandbox ID (empty = daemon generates)
	configYaml   string                                       // embedded YAML config
	image        string                                       // container image (empty = daemon uses configYaml image)
	readyMessage func(sandboxID, containerName string) string // custom ready message
}

// newAgentCommand builds `agbox agent --command "..."` for running a custom
// agent binary inside a sandbox. Registered agent types (claude, codex,
// openclaw) are exposed as top-level commands instead — they are not
// accepted here as positional arguments.
func newAgentCommand() *cobra.Command {
	var v agentSessionFlagVars
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Launch a custom agent session via --command",
		Long:  "Launch a sandbox and run a custom agent command specified via --command.\n\nFor pre-registered agents, use the dedicated top-level commands: agbox claude, agbox codex, agbox openclaw, agbox paseo.",
		Args:  cobra.NoArgs,
		RunE:  buildAgentSessionRunE("", &v),
	}
	registerAgentSessionFlags(cmd, &v)
	return cmd
}

// newAgentTypeCommand creates a top-level command for a specific registered
// agent type (e.g. "agbox claude").
func newAgentTypeCommand(agentType string) *cobra.Command {
	var v agentSessionFlagVars
	cmd := &cobra.Command{
		Use:   agentType,
		Short: fmt.Sprintf("Launch %s agent session", agentType),
		Long:  fmt.Sprintf("Launch %s agent session.", agentType),
		Args:  cobra.NoArgs,
		RunE:  buildAgentSessionRunE(agentType, &v),
	}
	registerAgentSessionFlags(cmd, &v)
	return cmd
}

// resolveAgentSessionArgs validates and resolves agent session arguments into
// an agentSessionArgs struct. It is a pure function suitable for unit testing.
func resolveAgentSessionArgs(
	v *agentSessionFlagVars,
	agentType string,
) (agentSessionArgs, error) {
	var parsed agentSessionArgs

	// Resolve the agent type definition when a registered type is used.
	var typeDef agentTypeDef
	var isRegistered bool

	if agentType != "" {
		var ok bool
		typeDef, ok = agentTypeDefs[agentType]
		if !ok {
			// Should not happen: agentType is injected by top-level per-type commands
			// which are registered only for known types.
			return agentSessionArgs{}, usageErrorf("unknown agent type %q", agentType)
		}
		isRegistered = true
		parsed.agentType = agentType
		parsed.command = typeDef.command
		if v.builtinToolsOverridden {
			parsed.builtinTools = v.builtinTools
		} else {
			parsed.builtinTools = append([]string(nil), typeDef.builtinTools...)
		}
	} else if v.rawCommand != "" {
		// Custom command: split the string into argv.
		parsed.command = strings.Fields(v.rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
		parsed.builtinTools = v.builtinTools
	} else {
		return agentSessionArgs{}, usageErrorf("agbox agent requires --command; for pre-registered agents use agbox claude / agbox codex / agbox openclaw / agbox paseo")
	}

	// Mode resolution.
	if v.modeOverridden {
		switch agentMode(v.mode) {
		case agentModeInteractive, agentModeLongRunning:
			parsed.mode = agentMode(v.mode)
		default:
			return agentSessionArgs{}, usageErrorf("--mode must be %q or %q", agentModeInteractive, agentModeLongRunning)
		}
	} else if isRegistered {
		parsed.mode = typeDef.mode
	} else {
		// Custom --command defaults to interactive.
		parsed.mode = agentModeInteractive
	}

	// --command override: for registered types with rawCommand, the rawCommand overrides typeDef.command.
	if isRegistered && v.rawCommand != "" {
		parsed.command = strings.Fields(v.rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
	}

	// Workspace resolution.
	if v.workspaceOverridden {
		// User explicitly provided --workspace; validate the path.
		if v.workspace == "" {
			return agentSessionArgs{}, usageErrorf("--workspace must not be empty")
		}
		resolved, err := validateWorkspacePath(v.workspace)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.workspace = resolved
	} else if isRegistered && typeDef.copyWorkspace {
		// Registered type with copyWorkspace: fill with cwd.
		cwd, err := os.Getwd()
		if err != nil {
			return agentSessionArgs{}, usageErrorf("--workspace: cannot determine current directory: %v", err)
		}
		resolved, err := validateWorkspacePath(cwd)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.workspace = resolved
	}
	// else: custom --command without --workspace → parsed.workspace stays "".

	// Parse --env flags into a map.
	if len(v.envs) > 0 {
		envMap := make(map[string]string, len(v.envs))
		for _, raw := range v.envs {
			key, value, err := parseKeyValueAssignment(raw, "--env")
			if err != nil {
				return agentSessionArgs{}, err
			}
			envMap[key] = value // last occurrence wins
		}
		parsed.envs = envMap
	}

	parsed.cpuLimit = v.cpuLimit
	parsed.memoryLimit = v.memoryLimit
	parsed.diskLimit = v.diskLimit

	// Sandbox ID resolution: --sandbox-id overrides the type's generator.
	if v.sandboxID != "" {
		parsed.sandboxID = v.sandboxID
	} else if isRegistered && typeDef.sandboxIDGen != nil {
		parsed.sandboxID = typeDef.sandboxIDGen()
	}
	// else: parsed.sandboxID stays empty, daemon auto-generates.

	// Populate agent-type-specific fields.
	if isRegistered {
		parsed.configYaml = typeDef.configYaml
		parsed.readyMessage = typeDef.readyMessage
	}

	// Image resolution: if configYaml contains a top-level `image:`, the daemon
	// uses that image and we leave parsed.image empty. Otherwise use defaultImage.
	if isRegistered && typeDef.configYaml != "" {
		var yamlConfig struct {
			Image string `yaml:"image"`
		}
		if err := yaml.Unmarshal([]byte(typeDef.configYaml), &yamlConfig); err == nil && yamlConfig.Image != "" {
			parsed.image = ""
		} else {
			parsed.image = defaultImage
		}
	} else {
		parsed.image = defaultImage
	}

	return parsed, nil
}

// validateWorkspacePath resolves the workspace path to an absolute, symlink-evaluated
// path and rejects dangerous paths (root and home directories).
func validateWorkspacePath(workspace string) (string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", usageErrorf("--workspace path %q: %v", workspace, err)
	}
	realWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		return "", usageErrorf("--workspace path %q: %v", workspace, err)
	}

	if realWorkspace == "/" {
		return "", usageErrorf("--workspace rejects root directory: copying the entire filesystem is not allowed; please specify a project directory instead")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", usageErrorf("--workspace: cannot determine home directory: %v", err)
	}
	realHome, err := filepath.EvalSymlinks(homeDir)
	if err != nil {
		return "", usageErrorf("--workspace: cannot resolve home directory: %v", err)
	}
	if realWorkspace == realHome {
		return "", usageErrorf("--workspace rejects home directory: copying the entire home directory is not allowed; please specify a project directory instead")
	}

	return realWorkspace, nil
}
