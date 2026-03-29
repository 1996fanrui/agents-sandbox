package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/1996fanrui/agents-sandbox/internal/control"
	"github.com/1996fanrui/agents-sandbox/internal/platform"
	"github.com/pelletier/go-toml/v2"
)

type startupConfig struct {
	socketPath    string
	lockPath      string
	idStorePath   string
	serviceConfig control.ServiceConfig
}

type daemonFileConfig struct {
	Runtime struct {
		IdleTTL           string `toml:"idle_ttl"`
		CleanupTTL string `toml:"cleanup_ttl"`
		StateRoot         string `toml:"state_root"`
		LogLevel          string `toml:"log_level"`
	} `toml:"runtime"`
	Artifacts struct {
		ExecOutputRoot     string `toml:"exec_output_root"`
		ExecOutputTemplate string `toml:"exec_output_template"`
	} `toml:"artifacts"`
}

func resolveStartupConfig(args []string, lookupEnv func(string) (string, bool)) (startupConfig, error) {
	if len(args) != 0 {
		return startupConfig{}, fmt.Errorf("agboxd does not accept CLI path overrides: %v", args)
	}

	socketPath, err := platform.SocketPath(lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	lockPath, err := platform.LockPath(lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	configPath, err := platform.ConfigFilePath(lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	idStorePath, err := platform.IDStorePath(lookupEnv)
	if err != nil {
		return startupConfig{}, err
	}
	serviceConfig := control.DefaultServiceConfig()
	fileConfig, err := loadDaemonFileConfigIfPresent(configPath)
	if err != nil {
		return startupConfig{}, err
	}
	serviceConfig, err = applyFileConfig(serviceConfig, configPath, fileConfig)
	if err != nil {
		return startupConfig{}, err
	}
	if serviceConfig.ArtifactOutputRoot == "" {
		serviceConfig.ArtifactOutputRoot = platform.ExecLogRoot(lookupEnv)
	}
	return startupConfig{
		socketPath:    socketPath,
		lockPath:      lockPath,
		idStorePath:   idStorePath,
		serviceConfig: serviceConfig,
	}, nil
}

func loadDaemonFileConfigIfPresent(path string) (daemonFileConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return daemonFileConfig{}, nil
		}
		return daemonFileConfig{}, fmt.Errorf("read daemon config file %s: %w", path, err)
	}
	var config daemonFileConfig
	if err := toml.Unmarshal(content, &config); err != nil {
		return daemonFileConfig{}, fmt.Errorf("decode daemon config file %s: %w", path, err)
	}
	return config, nil
}

func applyFileConfig(
	serviceConfig control.ServiceConfig,
	configPath string,
	fileConfig daemonFileConfig,
) (control.ServiceConfig, error) {
	if fileConfig.Runtime.IdleTTL != "" {
		idleTTL, err := time.ParseDuration(fileConfig.Runtime.IdleTTL)
		if err != nil {
			return control.ServiceConfig{}, fmt.Errorf("parse runtime.idle_ttl from %s: %w", configPath, err)
		}
		serviceConfig.IdleTTL = idleTTL
	}
	if fileConfig.Runtime.CleanupTTL != "" {
		cleanupTTL, err := time.ParseDuration(fileConfig.Runtime.CleanupTTL)
		if err != nil {
			return control.ServiceConfig{}, fmt.Errorf("parse runtime.cleanup_ttl from %s: %w", configPath, err)
		}
		serviceConfig.CleanupTTL = cleanupTTL
	}
	if fileConfig.Runtime.StateRoot != "" {
		serviceConfig.StateRoot = fileConfig.Runtime.StateRoot
	}
	if fileConfig.Runtime.LogLevel != "" {
		serviceConfig.LogLevel = fileConfig.Runtime.LogLevel
	}
	if fileConfig.Artifacts.ExecOutputRoot != "" {
		serviceConfig.ArtifactOutputRoot = fileConfig.Artifacts.ExecOutputRoot
	}
	if fileConfig.Artifacts.ExecOutputTemplate != "" {
		serviceConfig.ArtifactOutputTemplate = fileConfig.Artifacts.ExecOutputTemplate
	}
	return serviceConfig, nil
}
