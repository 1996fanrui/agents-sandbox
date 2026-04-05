package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/internal/logging"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
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
	if err := platform.CheckNetAdminCapability(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return runWithDeps(ctx, args, stderr, lookupEnv, acquireHostLock, control.ListenAndServe, control.NewServiceWithPersistentIDStore)
}

func runWithDeps(
	ctx context.Context,
	args []string,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
	acquireLock func(string) (*hostLock, error),
	listenAndServe func(context.Context, string, *control.Service, *slog.Logger) error,
	newService func(context.Context, control.ServiceConfig, string) (*control.Service, io.Closer, error),
) int {
	startup, err := resolveStartupConfig(args, lookupEnv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	logger, err := logging.SetupLogger(startup.serviceConfig.LogLevel)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	logger = logger.With(
		slog.String("daemon", "agboxd"),
		slog.String("version", startup.serviceConfig.Version),
	)
	startup.serviceConfig.Logger = logger
	slog.SetDefault(logger)

	logger.Info("starting",
		slog.String("socket_path", startup.socketPath),
		slog.String("id_store_path", startup.idStorePath),
		slog.String("idle_ttl", startup.serviceConfig.IdleTTL.String()),
		slog.String("cleanup_ttl", startup.serviceConfig.CleanupTTL.String()),
		slog.String("log_level", startup.serviceConfig.LogLevel),
	)

	lockHandle, err := acquireLock(startup.lockPath)
	if err != nil {
		logger.Error("failed to acquire lock", slog.String("error", err.Error()))
		return 1
	}
	defer func() {
		if releaseErr := lockHandle.release(); releaseErr != nil {
			logger.Error("failed to release lock", slog.String("error", releaseErr.Error()))
		}
	}()
	service, registryCloser, err := newService(ctx, startup.serviceConfig, startup.idStorePath)
	if err != nil {
		logger.Error("failed to create service", slog.String("error", err.Error()))
		return 1
	}
	defer func() {
		if registryCloser == nil {
			return
		}
		if closeErr := registryCloser.Close(); closeErr != nil {
			logger.Error("failed to close registry", slog.String("error", closeErr.Error()))
		}
	}()
	if err := listenAndServe(ctx, startup.socketPath, service, logger); err != nil {
		logger.Error("server stopped with error", slog.String("error", err.Error()))
		return 1
	}
	return 0
}
