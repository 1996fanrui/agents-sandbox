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
				"sandbox command requires a subcommand\navailable subcommands: create, list, get, delete, stop, resume",
			)
		},
	}

	cmd.AddCommand(
		newSandboxCreateCommand(),
		newSandboxListCommand(),
		newSandboxGetCommand(),
		newSandboxDeleteCommand(),
		newSandboxStopCommand(),
		newSandboxResumeCommand(),
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

			client, cleanup, err := newSandboxStreamingClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxCreate(cmd.Context(), client, parsed, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringVar(&image, "image", defaultImage, "Container image for the sandbox")
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
	var labels []string

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
			}
			if len(args) > 0 {
				parsed.sandboxID = args[0]
			}

			client, cleanup, err := newSandboxStreamingClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxDelete(cmd.Context(), client, parsed, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringArrayVar(&labels, "label", nil, "Label filter in key=value form (repeatable)")

	return cmd
}

func newSandboxStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <sandbox_id>",
		Short: "Stop a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newSandboxStreamingClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxStop(cmd.Context(), client, args[0], cmd.ErrOrStderr())
		},
	}

	return cmd
}

func newSandboxResumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <sandbox_id>",
		Short: "Resume a stopped sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cleanup, err := newSandboxStreamingClient(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			return runSandboxResume(cmd.Context(), client, args[0], cmd.ErrOrStderr())
		},
	}

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

// newSandboxStreamingClient creates a rawclient with no timeout, suitable for
// commands that subscribe to event streams (create with wait, delete with wait).
func newSandboxStreamingClient(cmd *cobra.Command) (*rawclient.RawClient, func(), error) {
	lookupEnv := lookupEnvFromCmd(cmd)

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return nil, nil, runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath, rawclient.WithTimeout(0))
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
