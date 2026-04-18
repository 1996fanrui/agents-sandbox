package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// agentSessionFlagVars holds the raw flag values for an agent session command.
type agentSessionFlagVars struct {
	rawCommand   string
	mode         string
	workspace    string
	builtinTools []string
	// Pre-reserved for ISSUE-188 (Commit 2 will consume these):
	envs        []string
	cpuLimit    string
	memoryLimit string
	diskLimit   string
	sandboxID   string
	// Track which flags were explicitly set by the user:
	modeOverridden         bool
	workspaceOverridden    bool
	builtinToolsOverridden bool
}

// registerAgentSessionFlags registers all agent session flags on a cobra command.
func registerAgentSessionFlags(cmd *cobra.Command, v *agentSessionFlagVars) {
	cmd.Flags().StringVar(&v.rawCommand, "command", "", "Custom command to run (mutually exclusive with agent type)")
	cmd.Flags().StringVar(&v.mode, "mode", "", "Session mode: interactive or long-running (default depends on agent type)")
	cmd.Flags().StringVar(&v.workspace, "workspace", "", "Directory to copy into the sandbox as workspace")
	cmd.Flags().StringArrayVar(&v.builtinTools, "builtin-tool", nil, "Builtin tool to install (repeatable, overrides defaults)")
	// ISSUE-188 flags (registered now, consumed in Commit 2):
	cmd.Flags().StringArrayVar(&v.envs, "env", nil, "Environment variable in KEY=VAL form (repeatable)")
	cmd.Flags().StringVar(&v.cpuLimit, "cpu-limit", "", "CPU limit (Docker --cpus format, e.g. 2, 0.5)")
	cmd.Flags().StringVar(&v.memoryLimit, "memory-limit", "", "Memory limit (Docker --memory format, e.g. 4g, 512m)")
	cmd.Flags().StringVar(&v.diskLimit, "disk-limit", "", "Disk limit (Docker --storage-opt size= format, e.g. 10g)")
	cmd.Flags().StringVar(&v.sandboxID, "sandbox-id", "", "Custom sandbox ID (overrides agent type default)")
}

// buildAgentSessionRunE creates the RunE function for agent session commands.
// When agentType is non-empty (top-level commands), it skips positional arg parsing.
func buildAgentSessionRunE(agentType string, v *agentSessionFlagVars) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		resolvedType := agentType
		if resolvedType == "" && len(args) > 0 {
			resolvedType = args[0]
		}
		v.modeOverridden = cmd.Flags().Changed("mode")
		v.workspaceOverridden = cmd.Flags().Changed("workspace")
		v.builtinToolsOverridden = cmd.Flags().Changed("builtin-tool")
		parsed, err := resolveAgentSessionArgs(v, resolvedType)
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
	sandboxID    string                                       // custom sandbox ID (empty = daemon generates)
	configYaml   string                                       // embedded YAML config
	phases       []execPhase                                  // multi-phase startup (non-empty replaces command)
	readyMessage func(sandboxID, containerName string) string // custom ready message
}

// registeredAgentNames returns the sorted list of pre-registered agent type names.
func registeredAgentNames() []string {
	names := make([]string, 0, len(agentTypeDefs))
	for name := range agentTypeDefs {
		names = append(names, name)
	}
	// Sort for deterministic output.
	sort.Strings(names)
	return names
}

func newAgentCommand() *cobra.Command {
	var v agentSessionFlagVars
	agentNames := registeredAgentNames()
	cmd := &cobra.Command{
		Use:       "agent [agent_type]",
		Short:     "Launch agent session",
		Long:      "Launch agent session.\n\nAvailable agent types: " + strings.Join(agentNames, ", ") + "\nOr use --command for a custom agent.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: agentNames,
		RunE:      buildAgentSessionRunE("", &v),
	}
	registerAgentSessionFlags(cmd, &v)
	return cmd
}

// newAgentTypeCommand creates a top-level command for a specific agent type
// (e.g. "agbox claude"), equivalent to "agbox agent <type>".
func newAgentTypeCommand(agentType string) *cobra.Command {
	var v agentSessionFlagVars
	cmd := &cobra.Command{
		Use:   agentType,
		Short: fmt.Sprintf("Launch %s agent session", agentType),
		Long:  fmt.Sprintf("Launch %s agent session (equivalent to 'agbox agent %s').", agentType, agentType),
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

	// Validate mutual exclusion: agent type vs --command.
	if agentType != "" && v.rawCommand != "" {
		return agentSessionArgs{}, usageErrorf("cannot use --command with agent type %q", agentType)
	}

	// Resolve the agent type definition when a registered type is used.
	var typeDef agentTypeDef
	var isRegistered bool

	if agentType != "" {
		var ok bool
		typeDef, ok = agentTypeDefs[agentType]
		if !ok {
			return agentSessionArgs{}, usageErrorf("unknown agent type %q; use --command for custom agents", agentType)
		}
		isRegistered = true
		parsed.agentType = agentType
		parsed.command = typeDef.command
		if v.builtinToolsOverridden {
			parsed.builtinTools = v.builtinTools
		} else {
			parsed.builtinTools = typeDef.builtinTools
		}
	} else if v.rawCommand != "" {
		// Custom command: split the string into argv.
		parsed.command = strings.Fields(v.rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
		parsed.builtinTools = v.builtinTools
	} else {
		return agentSessionArgs{}, usageErrorf("agbox agent requires an agent type or --command")
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

	// Sandbox ID resolution: use the type's generator if defined.
	if isRegistered && typeDef.sandboxIDGen != nil {
		parsed.sandboxID = typeDef.sandboxIDGen()
	}
	// else: parsed.sandboxID stays empty, daemon auto-generates.

	// Populate agent-type-specific fields.
	if isRegistered {
		parsed.configYaml = typeDef.configYaml
		parsed.phases = typeDef.phases
		parsed.readyMessage = typeDef.readyMessage
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
