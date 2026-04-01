package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/docker/docker/api/types/container"
	"google.golang.org/protobuf/types/known/durationpb"
)

func (backend *dockerRuntimeBackend) dockerWaitCompanionContainerHealthy(ctx context.Context, name string, healthcheck *agboxv1.HealthcheckConfig) error {
	if healthcheck == nil {
		return fmt.Errorf("companion container %s is missing healthcheck", name)
	}
	upperBound, err := companionContainerHealthWaitUpperBound(healthcheck)
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
					"companion container %s did not become healthy within %s (status=%s failing_streak=%d last_log=%s)",
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

func companionContainerHealthWaitUpperBound(healthcheck *agboxv1.HealthcheckConfig) (time.Duration, error) {
	const (
		defaultInterval      = 30 * time.Second
		defaultTimeout       = 30 * time.Second
		defaultStartInterval = 5 * time.Second
		defaultRetries       = uint32(3)
		maxUpperBound        = 5 * time.Minute
	)
	startPeriod := protoOrDefault(healthcheck.GetStartPeriod(), 0)
	interval := protoOrDefault(healthcheck.GetInterval(), defaultInterval)
	timeout := protoOrDefault(healthcheck.GetTimeout(), defaultTimeout)
	startIntervalDefault := time.Duration(0)
	if startPeriod > 0 {
		startIntervalDefault = defaultStartInterval
	}
	startInterval := protoOrDefault(healthcheck.GetStartInterval(), startIntervalDefault)
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

// protoOrDefault returns the Duration value or the default when nil or zero.
func protoOrDefault(d *durationpb.Duration, defaultValue time.Duration) time.Duration {
	if d == nil {
		return defaultValue
	}
	v := d.AsDuration()
	if v == 0 {
		return defaultValue
	}
	return v
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
	if healthcheck.GetInterval() != nil {
		config.Interval = healthcheck.GetInterval().AsDuration()
	}
	if healthcheck.GetTimeout() != nil {
		config.Timeout = healthcheck.GetTimeout().AsDuration()
	}
	if healthcheck.GetStartPeriod() != nil {
		config.StartPeriod = healthcheck.GetStartPeriod().AsDuration()
	}
	if healthcheck.GetStartInterval() != nil {
		config.StartInterval = healthcheck.GetStartInterval().AsDuration()
	}
	if healthcheck.GetRetries() > 0 {
		config.Retries = int(healthcheck.GetRetries())
	}
	return config, nil
}
