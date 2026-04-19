package main

import (
	"context"
	"fmt"
	"io"
	"os"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
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
	createResp, err := client.CreateExec(ctx, &agboxv1.CreateExecRequest{
		SandboxId: sandboxID,
		Command:   []string{"/usr/local/bin/paseo", "daemon", "pair"},
	})
	if err != nil {
		return runtimeErrorf("create exec: %v", err)
	}

	if err := waitForExecDone(ctx, client, createResp.GetExecId(), sandboxID); err != nil {
		// Read stderr log if available.
		if stderrLogPath := createResp.GetStderrLogPath(); stderrLogPath != "" {
			if logData, readErr := os.ReadFile(stderrLogPath); readErr == nil && len(logData) > 0 {
				_, _ = fmt.Fprintf(stderr, "%s", logData)
			}
		}
		return err
	}

	// Read and print stdout log (contains the pair URL).
	stdoutLogPath := createResp.GetStdoutLogPath()
	if stdoutLogPath != "" {
		logData, readErr := os.ReadFile(stdoutLogPath)
		if readErr != nil {
			return runtimeErrorf("read stdout log: %v", readErr)
		}
		_, _ = stdout.Write(logData)
	}
	return nil
}
