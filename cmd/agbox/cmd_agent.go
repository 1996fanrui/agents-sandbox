package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

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
	var (
		rawCommand   string
		mode         string
		workspace    string
		builtinTools []string
	)

	agentNames := registeredAgentNames()

	cmd := &cobra.Command{
		Use:       "agent [agent_type]",
		Short:     "Launch agent session",
		Long:      "Launch agent session.\n\nAvailable agent types: " + strings.Join(agentNames, ", ") + "\nOr use --command for a custom agent.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: agentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			agentType := ""
			if len(args) > 0 {
				agentType = args[0]
			}

			modeOverridden := cmd.Flags().Changed("mode")
			workspaceOverridden := cmd.Flags().Changed("workspace")
			builtinToolsOverridden := cmd.Flags().Changed("builtin-tool")

			parsed, err := resolveAgentSessionArgs(
				agentType, rawCommand,
				mode, modeOverridden,
				workspace, workspaceOverridden,
				builtinTools, builtinToolsOverridden,
			)
			if err != nil {
				return err
			}

			return runAgentSession(cmd.Context(), parsed, cmd.OutOrStdout(), cmd.ErrOrStderr(), lookupEnvFromCmd(cmd))
		},
	}

	cmd.Flags().StringVar(&rawCommand, "command", "", "Custom command to run (mutually exclusive with agent type)")
	cmd.Flags().StringVar(&mode, "mode", "", "Session mode: interactive or long-running (default depends on agent type)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Directory to copy into the sandbox as workspace")
	cmd.Flags().StringArrayVar(&builtinTools, "builtin-tool", nil, "Builtin tool to install (repeatable, overrides defaults)")

	return cmd
}

// resolveAgentSessionArgs validates and resolves agent session arguments into
// an agentSessionArgs struct. It is a pure function suitable for unit testing.
func resolveAgentSessionArgs(
	agentType string,
	rawCommand string,
	mode string,
	modeOverridden bool,
	workspace string,
	workspaceOverridden bool,
	builtinTools []string,
	builtinToolsOverridden bool,
) (agentSessionArgs, error) {
	var parsed agentSessionArgs

	// Validate mutual exclusion: agent type vs --command.
	if agentType != "" && rawCommand != "" {
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
		if builtinToolsOverridden {
			parsed.builtinTools = builtinTools
		} else {
			parsed.builtinTools = typeDef.builtinTools
		}
	} else if rawCommand != "" {
		// Custom command: split the string into argv.
		parsed.command = strings.Fields(rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
		parsed.builtinTools = builtinTools
	} else {
		return agentSessionArgs{}, usageErrorf("agbox agent requires an agent type or --command")
	}

	// Mode resolution.
	if modeOverridden {
		switch agentMode(mode) {
		case agentModeInteractive, agentModeLongRunning:
			parsed.mode = agentMode(mode)
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
	if workspaceOverridden {
		// User explicitly provided --workspace; validate the path.
		if workspace == "" {
			return agentSessionArgs{}, usageErrorf("--workspace must not be empty")
		}
		resolved, err := validateWorkspacePath(workspace)
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
