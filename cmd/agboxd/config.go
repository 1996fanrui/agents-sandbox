package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
		IdleTTL      string `toml:"idle_ttl"`
		StateRoot    string `toml:"state_root"`
		ReplayWindow int    `toml:"event_replay_window"`
	} `toml:"runtime"`
	Artifacts struct {
		ExecOutputRoot     string `toml:"exec_output_root"`
		ExecOutputTemplate string `toml:"exec_output_template"`
	} `toml:"artifacts"`
}

type startupFlags struct {
	socketPath         string
	configPath         string
	socketPathFromEnv  bool
	socketPathProvided bool
	configPathFromEnv  bool
	configPathProvided bool
}

func resolveStartupConfig(args []string, lookupEnv func(string) (string, bool)) (startupConfig, error) {
	parsedFlags, err := parseFlags(args, lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	serviceConfig := control.DefaultServiceConfig()
	socketPath := defaultSocketPathForPlatform(lookupEnv)

	if parsedFlags.configPath != "" {
		fileConfig, err := loadDaemonFileConfig(parsedFlags.configPath)
		if err != nil {
			return startupConfig{}, err
		}
		socketPath, serviceConfig, err = applyFileConfig(socketPath, serviceConfig, parsedFlags.configPath, fileConfig)
		if err != nil {
			return startupConfig{}, err
		}
	} else if detectedConfigPath := detectDefaultConfigPath(lookupEnv); detectedConfigPath != "" {
		fileConfig, err := loadDaemonFileConfig(detectedConfigPath)
		if err != nil {
			return startupConfig{}, err
		}
		socketPath, serviceConfig, err = applyFileConfig(socketPath, serviceConfig, detectedConfigPath, fileConfig)
		if err != nil {
			return startupConfig{}, err
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
		flags.configPathFromEnv = true
	}
	flagSet.Var(&stringFlag{target: &flags.socketPath, wasSet: &flags.socketPathProvided}, "socket", "Unix domain socket path for the daemon.")
	flagSet.Var(&stringFlag{target: &flags.configPath, wasSet: &flags.configPathProvided}, "config", "Path to the daemon TOML config file.")
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

func detectDefaultConfigPath(lookupEnv func(string) (string, bool)) string {
	var candidate string
	switch runtime.GOOS {
	case "darwin":
		if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
			candidate = filepath.Join(homeDir, "Library", "Application Support", "agents-sandbox", "config.toml")
		}
	default:
		configRoot := ""
		if envValue, ok := lookupEnv("XDG_CONFIG_HOME"); ok && envValue != "" {
			configRoot = envValue
		} else if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
			configRoot = filepath.Join(homeDir, ".config")
		}
		if configRoot != "" {
			candidate = filepath.Join(configRoot, "agents-sandbox", "config.toml")
		}
	}
	if candidate == "" {
		return ""
	}
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func defaultSocketPathForPlatform(lookupEnv func(string) (string, bool)) string {
	switch runtime.GOOS {
	case "darwin":
		if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
			return filepath.Join(homeDir, "Library", "Application Support", "agbox", "run", "agboxd.sock")
		}
	default:
		if runtimeDir, ok := lookupEnv("XDG_RUNTIME_DIR"); ok && runtimeDir != "" {
			return filepath.Join(runtimeDir, "agbox", "agboxd.sock")
		}
	}
	return defaultSocketPath
}

func applyFileConfig(
	socketPath string,
	serviceConfig control.ServiceConfig,
	configPath string,
	fileConfig daemonFileConfig,
) (string, control.ServiceConfig, error) {
	if fileConfig.Server.SocketPath != "" {
		socketPath = fileConfig.Server.SocketPath
	}
	if fileConfig.Runtime.IdleTTL != "" {
		idleTTL, err := time.ParseDuration(fileConfig.Runtime.IdleTTL)
		if err != nil {
			return "", control.ServiceConfig{}, fmt.Errorf("parse runtime.idle_ttl from %s: %w", configPath, err)
		}
		serviceConfig.IdleTTL = idleTTL
	}
	if fileConfig.Runtime.StateRoot != "" {
		serviceConfig.StateRoot = fileConfig.Runtime.StateRoot
	}
	if fileConfig.Runtime.ReplayWindow > 0 {
		serviceConfig.ReplayLimit = fileConfig.Runtime.ReplayWindow
	}
	if fileConfig.Artifacts.ExecOutputRoot != "" {
		serviceConfig.ArtifactOutputRoot = fileConfig.Artifacts.ExecOutputRoot
	}
	if fileConfig.Artifacts.ExecOutputTemplate != "" {
		serviceConfig.ArtifactOutputTemplate = fileConfig.Artifacts.ExecOutputTemplate
	}
	return socketPath, serviceConfig, nil
}
