package main

import (
	"context"
	"io"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
	"github.com/spf13/cobra"
)

func newPaseoURLCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "url <sandbox_id>",
		Short: "Print paseo pairing URL by running 'paseo daemon pair' inside the sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lookupEnv := lookupEnvFromCmd(cmd)
			socketPath, err := platform.SocketPath(lookupEnv)
			if err != nil {
				return runtimeErrorf("resolve daemon socket: %v", err)
			}
			client, err := rawclient.New(socketPath, rawclient.WithTimeout(0))
			if err != nil {
				return runtimeErrorf("connect daemon: %v", err)
			}
			defer client.Close()
			return runPaseoURL(cmd.Context(), client, args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func runPaseoURL(ctx context.Context, client sandboxExecClient, sandboxID string, stdout, stderr io.Writer) error {
	urlOutput, err := fetchPaseoPairURL(ctx, client, sandboxID, stderr)
	if err != nil {
		return err
	}
	if urlOutput != "" {
		_, _ = stdout.Write([]byte(urlOutput))
	}
	return nil
}
