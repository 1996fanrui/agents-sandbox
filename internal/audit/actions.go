package audit

import "fmt"

type ActionReason string

const (
	ActionReasonStartNewSession             ActionReason = "start_new_session"
	ActionReasonResumeSession               ActionReason = "resume_session"
	ActionReasonExecuteRun                  ActionReason = "execute_run"
	ActionReasonCleanupIdleSession          ActionReason = "cleanup_idle_session"
	ActionReasonCleanupLeakedSessionRuntime ActionReason = "cleanup_leaked_session_resources"
	ActionReasonCleanupArchivedSession      ActionReason = "cleanup_archived_session"
)

var validActionReasons = map[ActionReason]struct{}{
	ActionReasonStartNewSession:             {},
	ActionReasonResumeSession:               {},
	ActionReasonExecuteRun:                  {},
	ActionReasonCleanupIdleSession:          {},
	ActionReasonCleanupLeakedSessionRuntime: {},
	ActionReasonCleanupArchivedSession:      {},
}

type ActionStrategy string

const (
	ActionStrategyMaterializeNewSessionRuntime ActionStrategy = "materialize_new_session_runtime"
	ActionStrategyResumeExistingRuntime        ActionStrategy = "resume_existing_runtime"
	ActionStrategyCreateRunExec                ActionStrategy = "create_run_exec"
	ActionStrategyStartRunExec                 ActionStrategy = "start_run_exec"
	ActionStrategyCancelRunExec                ActionStrategy = "cancel_run_exec"
	ActionStrategyIdleSessionStop              ActionStrategy = "idle_session_stop"
	ActionStrategyDeleteSandboxRuntime         ActionStrategy = "delete_sandbox_runtime"
	ActionStrategyLeakedSidecarRemove          ActionStrategy = "leaked_sidecar_remove"
)

var validActionStrategies = map[ActionStrategy]struct{}{
	ActionStrategyMaterializeNewSessionRuntime: {},
	ActionStrategyResumeExistingRuntime:        {},
	ActionStrategyCreateRunExec:                {},
	ActionStrategyStartRunExec:                 {},
	ActionStrategyCancelRunExec:                {},
	ActionStrategyIdleSessionStop:              {},
	ActionStrategyDeleteSandboxRuntime:         {},
	ActionStrategyLeakedSidecarRemove:          {},
}

func ParseActionReason(raw string) (ActionReason, error) {
	reason := ActionReason(raw)
	if _, ok := validActionReasons[reason]; ok {
		return reason, nil
	}
	return "", fmt.Errorf("unsupported action reason %q", raw)
}

func ParseActionStrategy(raw string) (ActionStrategy, error) {
	strategy := ActionStrategy(raw)
	if _, ok := validActionStrategies[strategy]; ok {
		return strategy, nil
	}
	return "", fmt.Errorf("unsupported action strategy %q", raw)
}
