package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/1996fanrui/agents-sandbox/internal/version"
	"github.com/spf13/cobra"
)

// contextKey is an unexported type for context keys defined in this package.
type contextKey struct{ name string }

var lookupEnvKey = &contextKey{"lookupEnv"}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) int {
	// Store lookupEnv in context so subcommands can retrieve it.
	ctx = context.WithValue(ctx, lookupEnvKey, lookupEnv)

	rootCmd := &cobra.Command{
		Use:   "agbox",
		Short: "AgentsSandbox CLI",
		// No-args: print version + hint, exit 0.
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agbox %s\n", version.Version)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), `Run "agbox --help" for usage information.`)
			return nil
		},
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}

	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	rootCmd.AddCommand(
		newVersionCommand(),
		newSandboxCommand(),
		newAgentCommand(),
	)

	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		return exitCodeSuccess
	}

	// If it's already a cliError, handle normally.
	var cliErr *cliError
	if errors.As(err, &cliErr) {
		if shouldPrintError(err) {
			_, _ = fmt.Fprintln(stderr, err)
		}
		return exitCodeForError(err)
	}

	// Cobra flag parse errors are usage errors: print error + usage to stderr.
	_, _ = fmt.Fprintln(stderr, err)
	return exitCodeUsageError
}

// lookupEnvFromCmd retrieves the lookupEnv function stored in the command's context.
func lookupEnvFromCmd(cmd *cobra.Command) func(string) (string, bool) {
	if fn, ok := cmd.Context().Value(lookupEnvKey).(func(string) (string, bool)); ok {
		return fn
	}
	// Fallback (should not happen in normal flow).
	return func(string) (string, bool) { return "", false }
}
