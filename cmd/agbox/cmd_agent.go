package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// agentSessionFlagVars holds the raw flag values for an agent session command.
type agentSessionFlagVars struct {
	rawCommand   string
	mode         string
	workspace    string
	builtinTools []string
	envs         []string
	cpuLimit     string
	memoryLimit  string
	diskLimit    string
	sandboxID    string
	mounts       []string
	ports        []string
	copies       []string
	labels       []string
	// Track which flags were explicitly set by the user:
	modeOverridden      bool
	workspaceOverridden bool
}

// registerAgentSessionFlags registers all agent session flags on a cobra command.
func registerAgentSessionFlags(cmd *cobra.Command, v *agentSessionFlagVars) {
	cmd.Flags().StringVar(&v.rawCommand, "command", "", "Override the agent's default command.\n  interactive mode: replaces the TTY command launched via docker exec.\n  long-running mode: replaces the container primary command (under tini).\nValue is split by whitespace via strings.Fields (no shell quoting).")
	cmd.Flags().StringVar(&v.mode, "mode", "", "Session mode: interactive or long-running (default depends on agent type)")
	cmd.Flags().StringVar(&v.workspace, "workspace", "", "Directory to copy into the sandbox as workspace")
	cmd.Flags().StringArrayVar(&v.builtinTools, "builtin-tool", nil, "Builtin tool to install (repeatable; appended to the agent type's defaults, deduped preserving first-occurrence order)")
	cmd.Flags().StringArrayVar(&v.envs, "env", nil, "Environment variable in KEY=VAL form (repeatable)")
	cmd.Flags().StringVar(&v.cpuLimit, "cpu-limit", "", "CPU limit (Docker --cpus format, e.g. 2, 0.5)")
	cmd.Flags().StringVar(&v.memoryLimit, "memory-limit", "", "Memory limit (Docker --memory format, e.g. 4g, 512m)")
	cmd.Flags().StringVar(&v.diskLimit, "disk-limit", "", "Disk limit (Docker --storage-opt size= format, e.g. 10g)")
	cmd.Flags().StringVar(&v.sandboxID, "sandbox-id", "", "Custom sandbox ID (overrides agent type default)")
	cmd.Flags().StringArrayVar(&v.mounts, "mount", nil, "Bind mount in host:container[:writable] form (repeatable; default read-only)")
	cmd.Flags().StringArrayVar(&v.ports, "port", nil, "Port mapping in host:container[/proto] form (repeatable; proto = tcp|udp|sctp, default tcp)")
	cmd.Flags().StringArrayVar(&v.copies, "copy", nil, "File/directory copy in host:container form (repeatable; appended after the workspace copy)")
	cmd.Flags().StringArrayVar(&v.labels, "label", nil, "Sandbox label in key=value form (repeatable; user values override built-in created-by/agent-type)")
}

// newPaseoTopLevelCommand builds the top-level `agbox paseo` command with the
// `url` subcommand. Unlike other agent types, paseo has a subcommand tree.
func newPaseoTopLevelCommand() *cobra.Command {
	cmd := newAgentTypeCommand("paseo")
	cmd.AddCommand(newPaseoURLCommand())
	return cmd
}

// buildAgentSessionRunE creates the RunE function for agent session commands.
// agentType is non-empty for top-level per-type commands (agbox claude, etc.)
// and empty for the `agbox agent --command` custom-command command.
func buildAgentSessionRunE(agentType string, v *agentSessionFlagVars) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		v.modeOverridden = cmd.Flags().Changed("mode")
		v.workspaceOverridden = cmd.Flags().Changed("workspace")
		parsed, err := resolveAgentSessionArgs(v, agentType)
		if err != nil {
			return err
		}
		return runAgentSession(cmd.Context(), parsed, cmd.OutOrStdout(), cmd.ErrOrStderr(), lookupEnvFromCmd(cmd))
	}
}

// agentSessionArgs holds the parsed arguments for an agent session.
type agentSessionArgs struct {
	agentType    string    // pre-registered agent type (empty when --command is used)
	command      []string  // custom command (empty when a registered type is used)
	mode         agentMode // resolved session mode
	workspace    string    // host directory to copy; empty means "don't copy"
	builtinTools []string
	envs         map[string]string
	cpuLimit     string
	memoryLimit  string
	diskLimit    string
	sandboxID    string                                       // custom sandbox ID (empty = daemon generates)
	configYaml   string                                       // embedded YAML config
	image        string                                       // container image (empty = daemon uses configYaml image)
	readyMessage func(sandboxID, containerName string) string // custom ready message
	// Parsed --mount / --port / --copy / --label values, ready to splice into
	// CreateSpec.Mounts/Ports/Copies/Labels at request build time. Built-in
	// labels (created-by, agent-type) are written first; userLabels overlay
	// last so users can override them via --label.
	userMounts []*agboxv1.MountSpec
	userPorts  []*agboxv1.PortMapping
	userCopies []*agboxv1.CopySpec
	userLabels map[string]string
}

// newAgentCommand builds `agbox agent --command "..."` for running a custom
// agent binary inside a sandbox. Registered agent types (claude, codex,
// openclaw) are exposed as top-level commands instead — they are not
// accepted here as positional arguments.
func newAgentCommand() *cobra.Command {
	var v agentSessionFlagVars
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Launch a custom agent session via --command",
		Long:  "Launch a sandbox and run a custom agent command specified via --command.\n\nFor pre-registered agents, use the dedicated top-level commands: agbox claude, agbox codex, agbox openclaw, agbox paseo.",
		Args:  cobra.NoArgs,
		RunE:  buildAgentSessionRunE("", &v),
	}
	registerAgentSessionFlags(cmd, &v)
	return cmd
}

// newAgentTypeCommand creates a top-level command for a specific registered
// agent type (e.g. "agbox claude").
func newAgentTypeCommand(agentType string) *cobra.Command {
	var v agentSessionFlagVars
	cmd := &cobra.Command{
		Use:   agentType,
		Short: fmt.Sprintf("Launch %s agent session", agentType),
		Long:  fmt.Sprintf("Launch %s agent session.", agentType),
		Args:  cobra.NoArgs,
		RunE:  buildAgentSessionRunE(agentType, &v),
	}
	registerAgentSessionFlags(cmd, &v)
	return cmd
}

// resolveAgentSessionArgs validates and resolves agent session arguments into
// an agentSessionArgs struct. It is a pure function suitable for unit testing.
func resolveAgentSessionArgs(
	v *agentSessionFlagVars,
	agentType string,
) (agentSessionArgs, error) {
	var parsed agentSessionArgs

	// Resolve the agent type definition when a registered type is used.
	var typeDef agentTypeDef
	var isRegistered bool

	if agentType != "" {
		var ok bool
		typeDef, ok = agentTypeDefs[agentType]
		if !ok {
			// Should not happen: agentType is injected by top-level per-type commands
			// which are registered only for known types.
			return agentSessionArgs{}, usageErrorf("unknown agent type %q", agentType)
		}
		isRegistered = true
		parsed.agentType = agentType
		parsed.command = typeDef.command
		// --builtin-tool appends to the agent type's defaults; the daemon
		// already dedupes in mergeCreateSpecs (see commit 9ceac68), but we
		// dedupe at the CLI as well so that observable behavior (e.g. logs,
		// echoed CreateSpec) matches what the daemon will actually act on.
		parsed.builtinTools = dedupePreserveOrder(append(append([]string(nil), typeDef.builtinTools...), v.builtinTools...))
	} else if v.rawCommand != "" {
		// Custom command: split the string into argv.
		parsed.command = strings.Fields(v.rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
		// Custom command has no defaults, but still dedupe user input for consistency.
		parsed.builtinTools = dedupePreserveOrder(v.builtinTools)
	} else {
		return agentSessionArgs{}, usageErrorf("agbox agent requires --command; for pre-registered agents use agbox claude / agbox codex / agbox openclaw / agbox paseo")
	}

	// Mode resolution.
	if v.modeOverridden {
		switch agentMode(v.mode) {
		case agentModeInteractive, agentModeLongRunning:
			parsed.mode = agentMode(v.mode)
		default:
			return agentSessionArgs{}, usageErrorf("--mode must be %q or %q", agentModeInteractive, agentModeLongRunning)
		}
	} else if isRegistered {
		parsed.mode = typeDef.mode
	} else {
		// Custom --command defaults to interactive.
		parsed.mode = agentModeInteractive
	}

	// --command override: for registered types with rawCommand, the rawCommand overrides typeDef.command.
	if isRegistered && v.rawCommand != "" {
		parsed.command = strings.Fields(v.rawCommand)
		if len(parsed.command) == 0 {
			return agentSessionArgs{}, usageErrorf("--command must not be empty")
		}
	}

	// Workspace resolution.
	if v.workspaceOverridden {
		// User explicitly provided --workspace; validate the path.
		if v.workspace == "" {
			return agentSessionArgs{}, usageErrorf("--workspace must not be empty")
		}
		resolved, err := validateWorkspacePath(v.workspace)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.workspace = resolved
	} else if isRegistered && typeDef.copyWorkspace {
		// Registered type with copyWorkspace: fill with cwd.
		cwd, err := os.Getwd()
		if err != nil {
			return agentSessionArgs{}, usageErrorf("--workspace: cannot determine current directory: %v", err)
		}
		resolved, err := validateWorkspacePath(cwd)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.workspace = resolved
	}
	// else: custom --command without --workspace → parsed.workspace stays "".

	// Parse --env flags into a map.
	if len(v.envs) > 0 {
		envMap := make(map[string]string, len(v.envs))
		for _, raw := range v.envs {
			key, value, err := parseKeyValueAssignment(raw, "--env")
			if err != nil {
				return agentSessionArgs{}, err
			}
			envMap[key] = value // last occurrence wins
		}
		parsed.envs = envMap
	}

	parsed.cpuLimit = v.cpuLimit
	parsed.memoryLimit = v.memoryLimit
	parsed.diskLimit = v.diskLimit

	// Parse repeatable structured flags. Each parser returns a usageError on
	// invalid input; failures are surfaced verbatim so cobra prints usage.
	for _, raw := range v.mounts {
		m, err := parseMountFlag(raw)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.userMounts = append(parsed.userMounts, m)
	}
	for _, raw := range v.ports {
		p, err := parsePortFlag(raw)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.userPorts = append(parsed.userPorts, p)
	}
	for _, raw := range v.copies {
		c, err := parseCopyFlag(raw)
		if err != nil {
			return agentSessionArgs{}, err
		}
		parsed.userCopies = append(parsed.userCopies, c)
	}
	if len(v.labels) > 0 {
		labelMap := make(map[string]string, len(v.labels))
		for _, raw := range v.labels {
			key, value, err := parseKeyValueAssignment(raw, "--label")
			if err != nil {
				return agentSessionArgs{}, err
			}
			// parseKeyValueAssignment only enforces the presence of '='; an
			// empty key (e.g. "=value") is meaningless for a label and is
			// rejected here so users see a usageError instead of a silently
			// dropped/odd map entry.
			if key == "" {
				return agentSessionArgs{}, usageErrorf("--label key must not be empty")
			}
			labelMap[key] = value // last occurrence wins
		}
		parsed.userLabels = labelMap
	}

	// Sandbox ID resolution: --sandbox-id overrides the type's generator.
	if v.sandboxID != "" {
		parsed.sandboxID = v.sandboxID
	} else if isRegistered && typeDef.sandboxIDGen != nil {
		parsed.sandboxID = typeDef.sandboxIDGen()
	}
	// else: parsed.sandboxID stays empty, daemon auto-generates.

	// Populate agent-type-specific fields.
	if isRegistered {
		parsed.configYaml = typeDef.configYaml
		parsed.readyMessage = typeDef.readyMessage
	}

	// Image resolution: if configYaml contains a top-level `image:`, the daemon
	// uses that image and we leave parsed.image empty. Otherwise use defaultImage.
	if isRegistered && typeDef.configYaml != "" {
		var yamlConfig struct {
			Image string `yaml:"image"`
		}
		if err := yaml.Unmarshal([]byte(typeDef.configYaml), &yamlConfig); err == nil && yamlConfig.Image != "" {
			parsed.image = ""
		} else {
			parsed.image = defaultImage
		}
	} else {
		parsed.image = defaultImage
	}

	return parsed, nil
}

// validateWorkspacePath resolves the workspace path to an absolute, symlink-evaluated
// path and rejects dangerous paths (root and home directories).
func validateWorkspacePath(workspace string) (string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", usageErrorf("--workspace path %q: %v", workspace, err)
	}
	realWorkspace, err := filepath.EvalSymlinks(absWorkspace)
	if err != nil {
		return "", usageErrorf("--workspace path %q: %v", workspace, err)
	}

	if realWorkspace == "/" {
		return "", usageErrorf("--workspace rejects root directory: copying the entire filesystem is not allowed; please specify a project directory instead")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", usageErrorf("--workspace: cannot determine home directory: %v", err)
	}
	realHome, err := filepath.EvalSymlinks(homeDir)
	if err != nil {
		return "", usageErrorf("--workspace: cannot resolve home directory: %v", err)
	}
	if realWorkspace == realHome {
		return "", usageErrorf("--workspace rejects home directory: copying the entire home directory is not allowed; please specify a project directory instead")
	}

	return realWorkspace, nil
}

// parseMountFlag parses a --mount value of the form "host:container" or
// "host:container:writable" into a MountSpec. The optional third component
// must be the literal word "writable"; any other suffix (e.g. ":ro", ":rw")
// is rejected to keep the surface small and the default (read-only) explicit.
func parseMountFlag(s string) (*agboxv1.MountSpec, error) {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return nil, usageErrorf("--mount %q: source and target must be non-empty", s)
		}
		return &agboxv1.MountSpec{Source: parts[0], Target: parts[1]}, nil
	case 3:
		if parts[0] == "" || parts[1] == "" {
			return nil, usageErrorf("--mount %q: source and target must be non-empty", s)
		}
		if parts[2] != "writable" {
			return nil, usageErrorf("--mount %q: only the literal suffix \":writable\" is supported (got %q); omit the suffix for read-only", s, parts[2])
		}
		return &agboxv1.MountSpec{Source: parts[0], Target: parts[1], Writable: true}, nil
	default:
		return nil, usageErrorf("--mount %q: must be host:container or host:container:writable", s)
	}
}

// parsePortFlag parses a --port value of the form "host:container[/proto]"
// into a PortMapping. host and container must be integers in [1, 65535];
// proto (case-insensitive) defaults to TCP and accepts tcp/udp/sctp.
func parsePortFlag(s string) (*agboxv1.PortMapping, error) {
	if s == "" {
		return nil, usageErrorf("--port: value must not be empty")
	}
	hostContainer := s
	protoStr := "tcp"
	if idx := strings.Index(s, "/"); idx >= 0 {
		hostContainer = s[:idx]
		protoStr = s[idx+1:]
		if protoStr == "" {
			return nil, usageErrorf("--port %q: protocol must not be empty after '/'", s)
		}
	}
	parts := strings.Split(hostContainer, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, usageErrorf("--port %q: must be host:container[/proto]", s)
	}
	hostPort, err := parsePortNumber(parts[0], "--port host_port", s)
	if err != nil {
		return nil, err
	}
	containerPort, err := parsePortNumber(parts[1], "--port container_port", s)
	if err != nil {
		return nil, err
	}
	var proto agboxv1.PortProtocol
	switch strings.ToLower(protoStr) {
	case "tcp":
		proto = agboxv1.PortProtocol_PORT_PROTOCOL_TCP
	case "udp":
		proto = agboxv1.PortProtocol_PORT_PROTOCOL_UDP
	case "sctp":
		proto = agboxv1.PortProtocol_PORT_PROTOCOL_SCTP
	default:
		return nil, usageErrorf("--port %q: unsupported protocol %q; must be tcp, udp, or sctp", s, protoStr)
	}
	return &agboxv1.PortMapping{
		HostPort:      uint32(hostPort),
		ContainerPort: uint32(containerPort),
		Protocol:      proto,
	}, nil
}

// parsePortNumber parses a port string and validates the [1, 65535] range.
func parsePortNumber(raw, role, full string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, usageErrorf("%s %q: %q is not an integer", role, full, raw)
	}
	if n < 1 || n > 65535 {
		return 0, usageErrorf("%s %q: %d is out of range (1..65535)", role, full, n)
	}
	return n, nil
}

// dedupePreserveOrder returns a slice with duplicates removed, preserving the
// first-occurrence order. Empty input returns nil so the resulting CreateSpec
// field stays nil (rather than becoming an empty non-nil slice).
func dedupePreserveOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// parseCopyFlag parses a --copy value of the form "host:container" into a
// CopySpec. The split is on the first ':' so the container path may itself
// contain colons; both source and target must be non-empty.
func parseCopyFlag(s string) (*agboxv1.CopySpec, error) {
	source, target, found := strings.Cut(s, ":")
	if !found {
		return nil, usageErrorf("--copy %q: must be host:container", s)
	}
	if source == "" || target == "" {
		return nil, usageErrorf("--copy %q: source and target must be non-empty", s)
	}
	return &agboxv1.CopySpec{Source: source, Target: target}, nil
}
