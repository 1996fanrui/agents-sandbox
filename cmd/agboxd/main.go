package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/1996fanrui/agents-sandbox/internal/control"
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
	return runWithDeps(ctx, args, stderr, lookupEnv, acquireHostLock, control.ListenAndServe, control.NewServiceWithPersistentIDStore)
}

func runWithDeps(
	ctx context.Context,
	args []string,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
	acquireLock func(string) (*hostLock, error),
	listenAndServe func(context.Context, string, *control.Service) error,
	newService func(control.ServiceConfig, string) (*control.Service, io.Closer, error),
) int {
	startup, err := resolveStartupConfig(args, lookupEnv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	lockHandle, err := acquireLock(startup.lockPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if releaseErr := lockHandle.release(); releaseErr != nil {
			_, _ = fmt.Fprintln(stderr, releaseErr)
		}
	}()
	service, registryCloser, err := newService(startup.serviceConfig, startup.idStorePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if registryCloser == nil {
			return
		}
		if closeErr := registryCloser.Close(); closeErr != nil {
			_, _ = fmt.Fprintln(stderr, closeErr)
		}
	}()
	if err := listenAndServe(ctx, startup.socketPath, service); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
