package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/1996fanrui/agents-sandbox/internal/control"
)

const (
	defaultSocketPath = "/run/agbox/agboxd.sock"
	defaultLockPath   = "/run/agbox/agboxd.lock"
	socketEnvVar      = "AGBOX_SOCKET"
	configEnvVar      = "AGBOX_CONFIG_FILE"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, os.LookupEnv))
}

func run(
	ctx context.Context,
	args []string,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) int {
	return runWithDeps(ctx, args, stderr, lookupEnv, acquireHostLock, control.ListenAndServe)
}

func runWithDeps(
	ctx context.Context,
	args []string,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
	acquireLock func(string) (*hostLock, error),
	listenAndServe func(context.Context, string, *control.Service) error,
) int {
	startup, err := resolveStartupConfig(args, lookupEnv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	lockHandle, err := acquireLock(resolveLockPath(startup.socketPath))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if releaseErr := lockHandle.release(); releaseErr != nil {
			_, _ = fmt.Fprintln(stderr, releaseErr)
		}
	}()
	if err := listenAndServe(ctx, startup.socketPath, control.NewService(startup.serviceConfig)); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func resolveLockPath(socketPath string) string {
	if socketPath == "" || socketPath == defaultSocketPath {
		return defaultLockPath
	}
	return filepath.Join(filepath.Dir(socketPath), "agboxd.lock")
}
