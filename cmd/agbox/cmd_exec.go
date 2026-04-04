package main

import (
	"github.com/spf13/cobra"
)

func newExecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Manage command executions in sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return usageErrorf(
				"exec command requires a subcommand\navailable subcommands: run, get, cancel, list",
			)
		},
	}

	cmd.AddCommand(
		newExecRunCommand(),
		newExecGetCommand(),
		newExecCancelCommand(),
		newExecListCommand(),
	)

	return cmd
}

func newExecRunCommand() *cobra.Command {
	var (
		cwd          string
		envOverrides []string
	)

	cmd := &cobra.Command{
		Use:   "run <sandbox_id> [options] -- <command> [args...]",
		Short: "Execute a command in a sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ArgsLenAtDash splits positional args at the -- separator.
			dashIndex := cmd.ArgsLenAtDash()
			if dashIndex < 0 {
				return usageErrorf("exec run requires -- <command> [args...]")
			}

			beforeDash := args[:dashIndex]
			afterDash := args[dashIndex:]

			if len(beforeDash) == 0 {
				return usageErrorf("exec run requires <sandbox_id> -- <command> [args...]")
			}
			if len(beforeDash) > 1 {
				return usageErrorf("exec run requires <sandbox_id> -- <command> [args...]")
			}
			if len(afterDash) == 0 {
				return usageErrorf("exec run requires <sandbox_id> -- <command> [args...]")
			}

			envMap := make(map[string]string)
			for _, raw := range envOverrides {
				key, value, err := parseKeyValueAssignment(raw, "--env-overrides")
				if err != nil {
					return err
				}
				envMap[key] = value
			}

			parsed := sandboxExecArgs{
				sandboxID:    beforeDash[0],
				cwd:          cwd,
				envOverrides: envMap,
				command:      afterDash,
			}

			return runSandboxExec(cmd.Context(), cmd, parsed)
		},
	}

	cmd.Flags().StringVar(&cwd, "cwd", "", "Working directory inside the sandbox")
	cmd.Flags().StringArrayVar(&envOverrides, "env-overrides", nil, "Environment override in key=value form (repeatable)")

	return cmd
}

func newExecGetCommand() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "get <exec_id>",
		Short: "Get exec status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runExecGet(cmd.Context(), client, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newExecCancelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <exec_id>",
		Short: "Cancel a running exec",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newSandboxStreamingClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runExecCancel(cmd.Context(), client, args[0], cmd.ErrOrStderr())
		},
	}

	return cmd
}

func newExecListCommand() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list [sandbox_id]",
		Short: "List active execs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			var sandboxID string
			if len(args) > 0 {
				sandboxID = args[0]
			}

			return runExecList(cmd.Context(), client, sandboxID, jsonOutput, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}
