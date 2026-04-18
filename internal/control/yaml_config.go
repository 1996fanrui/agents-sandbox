package control

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"gopkg.in/yaml.v3"
)

// YAMLConfig is the top-level schema for declarative sandbox configuration
// supplied via the config_yaml field in CreateSandboxRequest.
type YAMLConfig struct {
	Image               string                                `yaml:"image"`
	Mounts              []YAMLMountSpec                       `yaml:"mounts"`
	Copies              []YAMLCopySpec                        `yaml:"copies"`
	Ports               []YAMLPortMapping                     `yaml:"ports"`
	BuiltinTools        []string                              `yaml:"builtin_tools"`
	CompanionContainers map[string]YAMLCompanionContainerSpec `yaml:"companion_containers"`
	Labels              map[string]string                     `yaml:"labels"`
	Envs                map[string]string                     `yaml:"envs"`
	// IdleTTL is the per-sandbox idle TTL override. Empty = use global daemon
	// default; "0" = disable idle stop for this sandbox.
	IdleTTL string `yaml:"idle_ttl"`
	// Command overrides the primary container's Docker CMD. A pointer type
	// preserves YAML presence semantics: nil = field omitted (daemon falls
	// back to the sleep-loop default); non-nil with len == 0 = explicit empty
	// array (rejected as a misconfiguration).
	Command *[]string `yaml:"command"`
	// CPULimit / MemoryLimit / DiskLimit follow Docker CLI syntax. Empty =
	// unlimited (no enforcement). See CreateSpec in api/proto/service.proto.
	CPULimit    string `yaml:"cpu_limit"`
	MemoryLimit string `yaml:"memory_limit"`
	DiskLimit   string `yaml:"disk_limit"`
}

// YAMLMountSpec describes a bind-mount from host to container.
type YAMLMountSpec struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	Writable bool   `yaml:"writable"`
}

// YAMLCopySpec describes a file-copy from host to container.
type YAMLCopySpec struct {
	Source          string   `yaml:"source"`
	Target          string   `yaml:"target"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
}

// YAMLPortMapping describes a port mapping from container to host.
type YAMLPortMapping struct {
	ContainerPort uint32 `yaml:"container_port"`
	HostPort      uint32 `yaml:"host_port"`
	Protocol      string `yaml:"protocol"`
}

// YAMLCompanionContainerSpec describes a companion container.
type YAMLCompanionContainerSpec struct {
	Image              string                 `yaml:"image"`
	Envs               map[string]string      `yaml:"envs"`
	Healthcheck        *YAMLHealthcheckConfig `yaml:"healthcheck"`
	PostStartOnPrimary []string               `yaml:"post_start_on_primary"`
	// Command overrides the companion container's Docker CMD. A pointer type
	// preserves YAML presence semantics: nil = field omitted (image CMD
	// applies); non-nil with len == 0 = explicit empty array (rejected).
	Command *[]string `yaml:"command"`
	// CPULimit / MemoryLimit / DiskLimit follow the same Docker CLI syntax as
	// the top-level CreateSpec fields, scoped to this companion container.
	// Empty = unlimited.
	CPULimit    string `yaml:"cpu_limit"`
	MemoryLimit string `yaml:"memory_limit"`
	DiskLimit   string `yaml:"disk_limit"`
}

// YAMLHealthcheckConfig describes the healthcheck for a companion container.
type YAMLHealthcheckConfig struct {
	Test          []string `yaml:"test"`
	Interval      string   `yaml:"interval"`
	Timeout       string   `yaml:"timeout"`
	Retries       uint32   `yaml:"retries"`
	StartPeriod   string   `yaml:"start_period"`
	StartInterval string   `yaml:"start_interval"`
}

// parseYAMLConfig strictly parses raw YAML bytes into a YAMLConfig,
// rejecting any unknown fields.
func parseYAMLConfig(raw []byte) (*YAMLConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)

	var cfg YAMLConfig
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validateYAMLCommand enforces YAML-layer command constraints. A nil field
// means the user omitted the key entirely and is accepted. A non-nil field
// with len == 0 is an explicit empty array and is rejected because proto3
// cannot distinguish omit from [] downstream; any element equal to the empty
// string is also rejected with the offending index reported in the error.
func validateYAMLCommand(field *[]string, path string) error {
	if field == nil {
		return nil
	}
	if len(*field) == 0 {
		return fmt.Errorf("%s: empty array is not allowed; omit the field to use the default", path)
	}
	for i, token := range *field {
		if token == "" {
			return fmt.Errorf("%s[%d]: empty string entry is not allowed", path, i)
		}
	}
	return nil
}

// yamlConfigToCreateSpec converts a parsed YAMLConfig into a proto CreateSpec.
// Map keys for companion containers are sorted alphabetically to produce deterministic output.
// Returns an error if any duration field in a companion container healthcheck is unparseable.
func yamlConfigToCreateSpec(cfg *YAMLConfig) (*agboxv1.CreateSpec, error) {
	if err := validateYAMLCommand(cfg.Command, "command"); err != nil {
		return nil, err
	}
	spec := &agboxv1.CreateSpec{
		Image:        cfg.Image,
		BuiltinTools: cfg.BuiltinTools,
		CpuLimit:     cfg.CPULimit,
		MemoryLimit:  cfg.MemoryLimit,
		DiskLimit:    cfg.DiskLimit,
	}
	if cfg.Command != nil {
		spec.Command = append([]string(nil), (*cfg.Command)...)
	}

	for _, m := range cfg.Mounts {
		spec.Mounts = append(spec.Mounts, &agboxv1.MountSpec{
			Source:   m.Source,
			Target:   m.Target,
			Writable: m.Writable,
		})
	}

	for _, c := range cfg.Copies {
		spec.Copies = append(spec.Copies, &agboxv1.CopySpec{
			Source:          c.Source,
			Target:          c.Target,
			ExcludePatterns: c.ExcludePatterns,
		})
	}

	for _, p := range cfg.Ports {
		proto, err := parsePortProtocol(p.Protocol)
		if err != nil {
			return nil, err
		}
		spec.Ports = append(spec.Ports, &agboxv1.PortMapping{
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			Protocol:      proto,
		})
	}

	var err error
	if spec.CompanionContainers, err = convertCompanionContainerMap(cfg.CompanionContainers); err != nil {
		return nil, err
	}

	if len(cfg.Labels) > 0 {
		spec.Labels = make(map[string]string, len(cfg.Labels))
		for k, v := range cfg.Labels {
			spec.Labels[k] = v
		}
	}

	if len(cfg.Envs) > 0 {
		spec.Envs = make(map[string]string, len(cfg.Envs))
		for k, v := range cfg.Envs {
			spec.Envs[k] = v
		}
	}

	if cfg.IdleTTL != "" {
		d, err := time.ParseDuration(cfg.IdleTTL)
		if err != nil {
			return nil, fmt.Errorf("invalid duration for idle_ttl: %q: %w", cfg.IdleTTL, err)
		}
		spec.IdleTtl = durationpb.New(d)
	}

	return spec, nil
}

// convertCompanionContainerMap converts a map of container name to YAMLCompanionContainerSpec
// into a sorted slice of proto CompanionContainerSpec. The map key becomes CompanionContainerSpec.Name.
// Returns an error if any healthcheck duration field contains an unparseable value.
func convertCompanionContainerMap(containers map[string]YAMLCompanionContainerSpec) ([]*agboxv1.CompanionContainerSpec, error) {
	if len(containers) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(containers))
	for k := range containers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]*agboxv1.CompanionContainerSpec, 0, len(keys))
	for _, name := range keys {
		cc := containers[name]
		if err := validateYAMLCommand(cc.Command, fmt.Sprintf("companion_containers[%s].command", name)); err != nil {
			return nil, err
		}
		protoCC := &agboxv1.CompanionContainerSpec{
			Name:               name,
			Image:              cc.Image,
			PostStartOnPrimary: cc.PostStartOnPrimary,
			CpuLimit:           cc.CPULimit,
			MemoryLimit:        cc.MemoryLimit,
			DiskLimit:          cc.DiskLimit,
		}
		if cc.Command != nil {
			protoCC.Command = append([]string(nil), (*cc.Command)...)
		}

		if len(cc.Envs) > 0 {
			protoCC.Envs = make(map[string]string, len(cc.Envs))
			for k, v := range cc.Envs {
				protoCC.Envs[k] = v
			}
		}

		if cc.Healthcheck != nil {
			hc := &agboxv1.HealthcheckConfig{
				Test:    cc.Healthcheck.Test,
				Retries: cc.Healthcheck.Retries,
			}
			var err error
			if hc.Interval, err = parseOptionalDuration("interval", cc.Healthcheck.Interval); err != nil {
				return nil, fmt.Errorf("companion container %q: %w", name, err)
			}
			if hc.Timeout, err = parseOptionalDuration("timeout", cc.Healthcheck.Timeout); err != nil {
				return nil, fmt.Errorf("companion container %q: %w", name, err)
			}
			if hc.StartPeriod, err = parseOptionalDuration("start_period", cc.Healthcheck.StartPeriod); err != nil {
				return nil, fmt.Errorf("companion container %q: %w", name, err)
			}
			if hc.StartInterval, err = parseOptionalDuration("start_interval", cc.Healthcheck.StartInterval); err != nil {
				return nil, fmt.Errorf("companion container %q: %w", name, err)
			}
			protoCC.Healthcheck = hc
		}

		result = append(result, protoCC)
	}

	return result, nil
}

// parseOptionalDuration parses a human-readable duration string (e.g., "10s", "1m30s")
// into a proto Duration. Returns nil for empty strings.
// Returns an error for non-empty but unparseable values.
func parseOptionalDuration(field, value string) (*durationpb.Duration, error) {
	if value == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return nil, fmt.Errorf("invalid duration for %s: %q: %w", field, value, err)
	}
	return durationpb.New(d), nil
}

// parsePortProtocol converts a user-friendly protocol string to the proto enum.
// Empty string and "tcp" map to TCP (default). Case-insensitive.
func parsePortProtocol(raw string) (agboxv1.PortProtocol, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "", "tcp":
		return agboxv1.PortProtocol_PORT_PROTOCOL_TCP, nil
	case "udp":
		return agboxv1.PortProtocol_PORT_PROTOCOL_UDP, nil
	case "sctp":
		return agboxv1.PortProtocol_PORT_PROTOCOL_SCTP, nil
	default:
		return 0, fmt.Errorf("unsupported port protocol %q; must be tcp, udp, or sctp", raw)
	}
}

// mergeCreateSpecs merges two CreateSpecs: base provides defaults, override
// takes precedence. Scalar strings overwrite when non-empty; repeated fields
// replace entirely when non-nil (len > 0); map fields merge at key level.
func mergeCreateSpecs(base, override *agboxv1.CreateSpec) *agboxv1.CreateSpec {
	if base == nil && override == nil {
		return &agboxv1.CreateSpec{}
	}
	if base == nil {
		return proto.Clone(override).(*agboxv1.CreateSpec)
	}
	if override == nil {
		return proto.Clone(base).(*agboxv1.CreateSpec)
	}

	result := proto.Clone(base).(*agboxv1.CreateSpec)

	// Scalar: override non-empty overwrites.
	if override.GetImage() != "" {
		result.Image = override.GetImage()
	}
	if override.GetCpuLimit() != "" {
		result.CpuLimit = override.GetCpuLimit()
	}
	if override.GetMemoryLimit() != "" {
		result.MemoryLimit = override.GetMemoryLimit()
	}
	if override.GetDiskLimit() != "" {
		result.DiskLimit = override.GetDiskLimit()
	}

	// Repeated: override non-nil (len > 0) replaces entirely.
	if len(override.GetMounts()) > 0 {
		result.Mounts = cloneMounts(override.GetMounts())
	}
	if len(override.GetCopies()) > 0 {
		result.Copies = cloneCopies(override.GetCopies())
	}
	if len(override.GetBuiltinTools()) > 0 {
		result.BuiltinTools = append([]string(nil), override.GetBuiltinTools()...)
	}
	if len(override.GetCompanionContainers()) > 0 {
		result.CompanionContainers = cloneCompanionContainerSpecs(override.GetCompanionContainers())
	}
	if len(override.GetPorts()) > 0 {
		result.Ports = clonePortMappings(override.GetPorts())
	}
	// command: len > 0 override replaces base entirely. Companion container
	// commands ride along with the whole-spec replacement above.
	if len(override.GetCommand()) > 0 {
		result.Command = append([]string(nil), override.GetCommand()...)
	}

	// Map: key-level merge — override keys overwrite, base-only keys preserved.
	if len(override.GetLabels()) > 0 {
		if result.Labels == nil {
			result.Labels = make(map[string]string)
		}
		for k, v := range override.GetLabels() {
			result.Labels[k] = v
		}
	}

	// Envs: key-level merge (same semantics as labels).
	if len(override.GetEnvs()) > 0 {
		if result.Envs == nil {
			result.Envs = make(map[string]string)
		}
		for k, v := range override.GetEnvs() {
			result.Envs[k] = v
		}
	}

	// idle_ttl: non-nil override replaces base (nil = not set, use global).
	if override.GetIdleTtl() != nil {
		result.IdleTtl = durationpb.New(override.GetIdleTtl().AsDuration())
	}

	return result
}
