package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
)

func (backend *dockerRuntimeBackend) dockerWaitRequiredServiceHealthy(ctx context.Context, name string, healthcheck *agboxv1.HealthcheckConfig) error {
	if healthcheck == nil {
		return fmt.Errorf("required service %s is missing healthcheck", name)
	}
	upperBound, err := requiredServiceHealthWaitUpperBound(healthcheck)
	if err != nil {
		return fmt.Errorf("compute health wait upper bound for %s: %w", name, err)
	}
	deadline := time.Now().Add(upperBound)
	var lastLogTime time.Time
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			state, err := backend.dockerContainerState(ctx, name)
			if err != nil {
				return err
			}
			if !state.Running {
				return fmt.Errorf("container %s is not running while waiting for health", name)
			}
			if state.Health == nil {
				return fmt.Errorf("container %s does not expose structured health state", name)
			}
			// Structured health fields are the source of truth.
			healthStatus := strings.ToLower(strings.TrimSpace(state.Health.Status))
			failingStreak := state.Health.FailingStreak
			latestLogTime := latestHealthLogTimestamp(state.Health.Log)
			if !latestLogTime.IsZero() {
				lastLogTime = latestLogTime
			}
			if healthStatus == "healthy" {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf(
					"service %s did not become healthy within %s (status=%s failing_streak=%d last_log=%s)",
					name,
					upperBound,
					healthStatus,
					failingStreak,
					lastLogTime.UTC().Format(time.RFC3339Nano),
				)
			}
		}
	}
}

func requiredServiceHealthWaitUpperBound(healthcheck *agboxv1.HealthcheckConfig) (time.Duration, error) {
	const (
		defaultInterval      = 30 * time.Second
		defaultTimeout       = 30 * time.Second
		defaultStartInterval = 5 * time.Second
		defaultRetries       = uint32(3)
		maxUpperBound        = 5 * time.Minute
	)
	startPeriod, err := parseHealthDuration(healthcheck.GetStartPeriod(), 0)
	if err != nil {
		return 0, err
	}
	interval, err := parseHealthDuration(healthcheck.GetInterval(), defaultInterval)
	if err != nil {
		return 0, err
	}
	timeout, err := parseHealthDuration(healthcheck.GetTimeout(), defaultTimeout)
	if err != nil {
		return 0, err
	}
	startIntervalDefault := time.Duration(0)
	if startPeriod > 0 {
		startIntervalDefault = defaultStartInterval
	}
	startInterval, err := parseHealthDuration(healthcheck.GetStartInterval(), startIntervalDefault)
	if err != nil {
		return 0, err
	}
	retries := healthcheck.GetRetries()
	if retries == 0 {
		retries = defaultRetries
	}
	startupGraceCheckWindow := time.Duration(0)
	if startPeriod > 0 {
		startupGraceCheckWindow = maxDuration(startInterval, timeout)
	}
	countedCheckWindow := maxDuration(interval, timeout)
	theoreticalUpperBound := startPeriod + startupGraceCheckWindow + countedCheckWindow*time.Duration(retries+1)
	return minDuration(theoreticalUpperBound, maxUpperBound), nil
}

func parseHealthDuration(raw string, defaultValue time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	return time.ParseDuration(raw)
}

func latestHealthLogTimestamp(items []*container.HealthcheckResult) time.Time {
	var latest time.Time
	for _, item := range items {
		if item == nil {
			continue
		}
		candidate := item.End
		if candidate.IsZero() {
			candidate = item.Start
		}
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

func maxDuration(left time.Duration, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func minDuration(left time.Duration, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func toContainerHealthConfig(healthcheck *agboxv1.HealthcheckConfig) (*container.HealthConfig, error) {
	if healthcheck == nil {
		return nil, nil
	}
	config := &container.HealthConfig{
		Test: append([]string(nil), healthcheck.GetTest()...),
	}
	if healthcheck.GetInterval() != "" {
		interval, err := time.ParseDuration(healthcheck.GetInterval())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck interval: %w", err)
		}
		config.Interval = interval
	}
	if healthcheck.GetTimeout() != "" {
		timeout, err := time.ParseDuration(healthcheck.GetTimeout())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck timeout: %w", err)
		}
		config.Timeout = timeout
	}
	if healthcheck.GetStartPeriod() != "" {
		startPeriod, err := time.ParseDuration(healthcheck.GetStartPeriod())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck start period: %w", err)
		}
		config.StartPeriod = startPeriod
	}
	if healthcheck.GetStartInterval() != "" {
		startInterval, err := time.ParseDuration(healthcheck.GetStartInterval())
		if err != nil {
			return nil, fmt.Errorf("parse healthcheck start interval: %w", err)
		}
		config.StartInterval = startInterval
	}
	if healthcheck.GetRetries() > 0 {
		config.Retries = int(healthcheck.GetRetries())
	}
	return config, nil
}
