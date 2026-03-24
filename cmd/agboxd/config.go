package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/pelletier/go-toml/v2"
)

type startupConfig struct {
	socketPath    string
	serviceConfig control.ServiceConfig
}

type daemonFileConfig struct {
	Server struct {
		SocketPath string `toml:"socket_path"`
	} `toml:"server"`
	Runtime struct {
		IdleTTL string `toml:"idle_ttl"`
	} `toml:"runtime"`
}

type startupFlags struct {
	socketPath         string
	configPath         string
	socketPathFromEnv  bool
	socketPathProvided bool
}

func resolveStartupConfig(args []string, lookupEnv func(string) (string, bool)) (startupConfig, error) {
	parsedFlags, err := parseFlags(args, lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	serviceConfig := control.DefaultServiceConfig()
	socketPath := defaultSocketPath

	if parsedFlags.configPath != "" {
		fileConfig, err := loadDaemonFileConfig(parsedFlags.configPath)
		if err != nil {
			return startupConfig{}, err
		}
		if fileConfig.Server.SocketPath != "" {
			socketPath = fileConfig.Server.SocketPath
		}
		if fileConfig.Runtime.IdleTTL != "" {
			idleTTL, err := time.ParseDuration(fileConfig.Runtime.IdleTTL)
			if err != nil {
				return startupConfig{}, fmt.Errorf("parse runtime.idle_ttl from %s: %w", parsedFlags.configPath, err)
			}
			serviceConfig.IdleTTL = idleTTL
		}
	}
	if parsedFlags.socketPathFromEnv || parsedFlags.socketPathProvided {
		socketPath = parsedFlags.socketPath
	}
	return startupConfig{
		socketPath:    socketPath,
		serviceConfig: serviceConfig,
	}, nil
}

func parseFlags(args []string, lookupEnv func(string) (string, bool)) (startupFlags, error) {
	flagSet := flag.NewFlagSet("agboxd", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	flags := startupFlags{}
	if envValue, ok := lookupEnv(socketEnvVar); ok && envValue != "" {
		flags.socketPath = envValue
		flags.socketPathFromEnv = true
	}
	if envValue, ok := lookupEnv(configEnvVar); ok && envValue != "" {
		flags.configPath = envValue
	}
	flagSet.Var(&stringFlag{target: &flags.socketPath, wasSet: &flags.socketPathProvided}, "socket", "Unix domain socket path for the daemon.")
	flagSet.StringVar(&flags.configPath, "config", flags.configPath, "Path to the daemon TOML config file.")
	if err := flagSet.Parse(args); err != nil {
		return startupFlags{}, err
	}
	return flags, nil
}

type stringFlag struct {
	target *string
	wasSet *bool
}

func (value *stringFlag) String() string {
	if value.target == nil {
		return ""
	}
	return *value.target
}

func (value *stringFlag) Set(raw string) error {
	if value.target != nil {
		*value.target = raw
	}
	if value.wasSet != nil {
		*value.wasSet = true
	}
	return nil
}

func loadDaemonFileConfig(path string) (daemonFileConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return daemonFileConfig{}, fmt.Errorf("daemon config file %s does not exist", path)
		}
		return daemonFileConfig{}, fmt.Errorf("read daemon config file %s: %w", path, err)
	}
	var config daemonFileConfig
	if err := toml.Unmarshal(content, &config); err != nil {
		return daemonFileConfig{}, fmt.Errorf("decode daemon config file %s: %w", path, err)
	}
	return config, nil
}
