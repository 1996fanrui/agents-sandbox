package main

import (
	"fmt"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"github.com/spf13/cobra"
)

func newPingCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check daemon reachability",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPing(cmd)
		},
	}
}

func runPing(cmd *cobra.Command) error {
	lookupEnv := lookupEnvFromCmd(cmd)

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath)
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	resp, err := client.Ping(cmd.Context())
	if err != nil {
		return runtimeErrorf("ping daemon: %v", err)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon=%s version=%s\n", resp.GetDaemon(), resp.GetVersion())
	return nil
}
