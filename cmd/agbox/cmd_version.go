package main

import (
	"fmt"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/internal/version"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"github.com/spf13/cobra"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print agbox and agboxd versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agbox: %s\n", version.Version)

			// Try to fetch daemon version via ping.
			lookupEnv := lookupEnvFromCmd(cmd)
			socketPath, err := platform.SocketPath(lookupEnv)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "agboxd: unavailable")
				return nil
			}

			client, err := rawclient.New(socketPath)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "agboxd: unavailable")
				return nil
			}
			defer client.Close()

			resp, err := client.Ping(cmd.Context())
			if err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "agboxd: unavailable")
				return nil
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agboxd: %s\n", resp.GetVersion())
			return nil
		},
	}
}
