package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/1996fanrui/agents-sandbox/sdk/go/rawclient"
)

const version = "0.1.0"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintf(stdout, "agbox %s\n", version)
		return exitCodeSuccess
	}

	var err error
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, version)
		return exitCodeSuccess
	case "ping":
		err = runPing(ctx, args[1:], stdout, lookupEnv)
	case "sandbox":
		err = runSandbox(ctx, args[1:], stdout, stderr, lookupEnv)
	default:
		err = usageErrorf("unknown command %q", args[0])
	}

	if err != nil {
		if shouldPrintError(err) {
			_, _ = fmt.Fprintln(stderr, err)
		}
	}

	return exitCodeForError(err)
}

func runPing(ctx context.Context, args []string, stdout io.Writer, lookupEnv func(string) (string, bool)) error {
	if len(args) != 0 {
		return usageErrorf("ping command does not accept arguments: %v", args)
	}

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return runtimeErrorf("resolve daemon socket: %v", err)
	}

	client, err := rawclient.New(socketPath)
	if err != nil {
		return runtimeErrorf("connect daemon: %v", err)
	}
	defer client.Close()

	resp, err := client.Ping(ctx)
	if err != nil {
		return runtimeErrorf("ping daemon: %v", err)
	}

	_, _ = fmt.Fprintf(stdout, "daemon=%s version=%s\n", resp.GetDaemon(), resp.GetVersion())
	return nil
}

var sandboxSubcommands = []string{"create", "list", "get", "delete", "exec"}
