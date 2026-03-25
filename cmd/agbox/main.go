package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
		return 0
	}

	switch args[0] {
	case "version":
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case "ping":
		if err := runPing(ctx, args[1:], stdout, lookupEnv); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runPing(ctx context.Context, args []string, stdout io.Writer, lookupEnv func(string) (string, bool)) error {
	if len(args) != 0 {
		return fmt.Errorf("ping command does not accept arguments: %v", args)
	}
	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return err
	}

	conn, err := grpc.NewClient(
		"passthrough:///agboxd",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return fmt.Errorf("connect daemon: %w", err)
	}
	defer conn.Close()

	resp, err := agboxv1.NewSandboxServiceClient(conn).Ping(ctx, &agboxv1.PingRequest{})
	if err != nil {
		return fmt.Errorf("ping daemon: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "daemon=%s version=%s\n", resp.GetDaemon(), resp.GetVersion())
	return nil
}
