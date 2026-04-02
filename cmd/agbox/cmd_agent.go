package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// agentSessionArgs holds the parsed arguments for an interactive agent session.
type agentSessionArgs struct {
	agentType    string   // pre-registered agent type (empty when --command is used)
	command      []string // custom command (empty when a registered type is used)
	workspace    string
	builtinTools []string
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
		workspace    string
		builtinTools []string
	)

	agentNames := registeredAgentNames()

	cmd := &cobra.Command{
		Use:       "agent [agent_type]",
		Short:     "Launch interactive agent session",
		Long:      "Launch interactive agent session.\n\nAvailable agent types: " + strings.Join(agentNames, ", ") + "\nOr use --command for a custom agent.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: agentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			agentType := ""
			if len(args) > 0 {
				agentType = args[0]
			}

			builtinToolsOverridden := cmd.Flags().Changed("builtin-tool")

			parsed, err := resolveAgentSessionArgs(agentType, rawCommand, workspace, builtinTools, builtinToolsOverridden)
			if err != nil {
				return err
			}

			return runAgentSession(cmd.Context(), parsed, cmd.OutOrStdout(), cmd.ErrOrStderr(), lookupEnvFromCmd(cmd))
		},
	}

	cwd, _ := os.Getwd()
	cmd.Flags().StringVar(&rawCommand, "command", "", "Custom command to run (mutually exclusive with agent type)")
	cmd.Flags().StringVar(&workspace, "workspace", cwd, "Directory to copy into the sandbox as workspace")
	cmd.Flags().StringArrayVar(&builtinTools, "builtin-tool", nil, "Builtin tool to install (repeatable, overrides defaults)")

	return cmd
}

// resolveAgentSessionArgs validates and resolves agent session arguments into
// an agentSessionArgs struct. It is a pure function suitable for unit testing.
func resolveAgentSessionArgs(
	agentType string,
	rawCommand string,
	workspace string,
	builtinTools []string,
	builtinToolsOverridden bool,
) (agentSessionArgs, error) {
	var parsed agentSessionArgs

	// Validate mutual exclusion: agent type vs --command.
	if agentType != "" && rawCommand != "" {
		return agentSessionArgs{}, usageErrorf("cannot use --command with agent type %q", agentType)
	}

	if agentType != "" {
		typeDef, ok := agentTypeDefs[agentType]
		if !ok {
			return agentSessionArgs{}, usageErrorf("unknown agent type %q; use --command for custom agents", agentType)
		}
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

	// Validate workspace path: resolve symlinks and reject dangerous paths.
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return agentSessionArgs{}, usageErrorf("--workspace path %q: %v", workspace, err)
	}
	realWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		return agentSessionArgs{}, usageErrorf("--workspace path %q: %v", workspace, err)
	}

	if realWorkspace == "/" {
		return agentSessionArgs{}, usageErrorf("--workspace rejects root directory: copying the entire filesystem is not allowed; please specify a project directory instead")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return agentSessionArgs{}, usageErrorf("--workspace: cannot determine home directory: %v", err)
	}
	realHome, err := filepath.EvalSymlinks(homeDir)
	if err != nil {
		return agentSessionArgs{}, usageErrorf("--workspace: cannot resolve home directory: %v", err)
	}
	if realWorkspace == realHome {
		return agentSessionArgs{}, usageErrorf("--workspace rejects home directory: copying the entire home directory is not allowed; please specify a project directory instead")
	}

	parsed.workspace = realWorkspace

	return parsed, nil
}
