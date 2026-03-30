package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"github.com/spf13/cobra"
)

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
		// Args prevents cobra from silently accepting unknown positional arguments
		// (which would otherwise be passed to RunE as args).
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return usageErrorf(
				"sandbox command requires a subcommand\navailable subcommands: create, list, get, delete, exec",
			)
		},
	}

	cmd.AddCommand(
		newSandboxCreateCommand(),
		newSandboxListCommand(),
		newSandboxGetCommand(),
		newSandboxDeleteCommand(),
		newSandboxExecCommand(),
	)

	return cmd
}

func newSandboxCreateCommand() *cobra.Command {
	var (
		image      string
		labels     []string
		idleTTLStr string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			labelMap, err := parseLabels(labels)
			if err != nil {
				return err
			}

			parsed := sandboxCreateArgs{
				image:  image,
				labels: labelMap,
				json:   jsonOutput,
			}

			if idleTTLStr != "" {
				d, err := time.ParseDuration(idleTTLStr)
				if err != nil {
					return usageErrorf("--idle-ttl: invalid duration %q: %v", idleTTLStr, err)
				}
				if d < 0 {
					return usageErrorf("--idle-ttl must not be negative")
				}
				parsed.idleTTL = &d
			}

			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxCreate(cmd.Context(), client, parsed, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "Container image for the sandbox")
	_ = cmd.MarkFlagRequired("image")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label in key=value form (repeatable)")
	cmd.Flags().StringVar(&idleTTLStr, "idle-ttl", "", "Idle TTL duration (e.g. 5m, 0 to disable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newSandboxListCommand() *cobra.Command {
	var (
		includeDeleted bool
		labels         []string
		jsonOutput     bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			labelMap, err := parseLabels(labels)
			if err != nil {
				return err
			}

			parsed := sandboxListArgs{
				includeDeleted: includeDeleted,
				labels:         labelMap,
				json:           jsonOutput,
			}

			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxList(cmd.Context(), client, parsed, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "Include deleted sandboxes")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label filter in key=value form (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newSandboxGetCommand() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "get <sandbox_id>",
		Short: "Get sandbox details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed := sandboxGetArgs{
				sandboxID: args[0],
				json:      jsonOutput,
			}

			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxGet(cmd.Context(), client, parsed, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newSandboxDeleteCommand() *cobra.Command {
	var (
		labels     []string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "delete [sandbox_id]",
		Short: "Delete sandbox(es)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			labelMap, err := parseLabels(labels)
			if err != nil {
				return err
			}

			parsed := sandboxDeleteArgs{
				labels: labelMap,
				json:   jsonOutput,
			}
			if len(args) > 0 {
				parsed.sandboxID = args[0]
			}

			client, cleanup, err := newSandboxClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxDelete(cmd.Context(), client, parsed, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label filter in key=value form (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func newSandboxExecCommand() *cobra.Command {
	var (
		cwd          string
		envOverrides []string
		jsonOutput   bool
	)

	cmd := &cobra.Command{
		Use:   "exec <sandbox_id> [options] -- <command> [args...]",
		Short: "Execute a command in a sandbox",
		// Disable flag parsing after -- so that the command after -- is captured as args.
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOutput {
				return usageErrorf("sandbox exec does not support --json")
			}

			// Use ArgsLenAtDash to split positional args before and after --.
			dashIndex := cmd.ArgsLenAtDash()
			if dashIndex < 0 {
				return usageErrorf("sandbox exec requires -- <command> [args...]")
			}

			beforeDash := args[:dashIndex]
			afterDash := args[dashIndex:]

			if len(beforeDash) == 0 {
				return usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
			}
			if len(beforeDash) > 1 {
				return usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
			}
			if len(afterDash) == 0 {
				return usageErrorf("sandbox exec requires <sandbox_id> -- <command> [args...]")
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
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

// newSandboxClient creates a rawclient connected to the daemon socket.
// Returns the client and a cleanup function.
func newSandboxClient(cmd *cobra.Command) (*rawclient.RawClient, func(), error) {
	lookupEnv := lookupEnvFromCmd(cmd)

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return nil, nil, runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath)
	if err != nil {
		return nil, nil, runtimeErrorf("connect daemon: %v", err)
	}

	return client, func() { client.Close() }, nil
}

// parseLabels converts a slice of "key=value" strings into a label map.
func parseLabels(labels []string) (map[string]string, error) {
	labelMap := make(map[string]string)
	for _, raw := range labels {
		key, value, err := parseLabelAssignment(raw)
		if err != nil {
			return nil, err
		}
		labelMap[key] = value
	}
	return labelMap, nil
}

// parseKeyValueAssignment splits "key=value" and returns a usage error on failure.
func parseKeyValueAssignment(raw string, flagName string) (string, string, error) {
	key, value, found := strings.Cut(raw, "=")
	if !found {
		return "", "", usageErrorf("%s must be in key=value form", flagName)
	}
	return key, value, nil
}

func parseLabelAssignment(raw string) (string, string, error) {
	return parseKeyValueAssignment(raw, "--label")
}

func labelsToPairs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return pairs
}
