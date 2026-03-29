package control

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"gopkg.in/yaml.v3"
)

// YAMLConfig is the top-level schema for declarative sandbox configuration
// supplied via the config_yaml field in CreateSandboxRequest.
type YAMLConfig struct {
	Image            string                       `yaml:"image"`
	Mounts           []YAMLMountSpec              `yaml:"mounts"`
	Copies           []YAMLCopySpec               `yaml:"copies"`
	BuiltinTools []string                     `yaml:"builtin_tools"`
	RequiredServices map[string]YAMLServiceSpec   `yaml:"required_services"`
	OptionalServices map[string]YAMLServiceSpec   `yaml:"optional_services"`
	Labels           map[string]string            `yaml:"labels"`
	Envs             map[string]string            `yaml:"envs"`
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

// YAMLServiceSpec describes a sidecar service container.
type YAMLServiceSpec struct {
	Image              string                 `yaml:"image"`
	Envs               map[string]string      `yaml:"envs"`
	Healthcheck        *YAMLHealthcheckConfig `yaml:"healthcheck"`
	PostStartOnPrimary []string               `yaml:"post_start_on_primary"`
}

// YAMLHealthcheckConfig describes the healthcheck for a service container.
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

// yamlConfigToCreateSpec converts a parsed YAMLConfig into a proto CreateSpec.
// Map keys for services are sorted alphabetically to produce deterministic output.
// Returns an error if any duration field in a service healthcheck is unparseable.
func yamlConfigToCreateSpec(cfg *YAMLConfig) (*agboxv1.CreateSpec, error) {
	spec := &agboxv1.CreateSpec{
		Image:        cfg.Image,
		BuiltinTools: cfg.BuiltinTools,
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

	var err error
	if spec.RequiredServices, err = convertServiceMap(cfg.RequiredServices); err != nil {
		return nil, err
	}
	if spec.OptionalServices, err = convertServiceMap(cfg.OptionalServices); err != nil {
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

	return spec, nil
}

// convertServiceMap converts a map of service name to YAMLServiceSpec into
// a sorted slice of proto ServiceSpec. The map key becomes ServiceSpec.Name.
// Returns an error if any healthcheck duration field contains an unparseable value.
func convertServiceMap(services map[string]YAMLServiceSpec) ([]*agboxv1.ServiceSpec, error) {
	if len(services) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(services))
	for k := range services {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]*agboxv1.ServiceSpec, 0, len(keys))
	for _, name := range keys {
		svc := services[name]
		protoSvc := &agboxv1.ServiceSpec{
			Name:               name,
			Image:              svc.Image,
			PostStartOnPrimary: svc.PostStartOnPrimary,
		}

		if len(svc.Envs) > 0 {
			protoSvc.Envs = make(map[string]string, len(svc.Envs))
			for k, v := range svc.Envs {
				protoSvc.Envs[k] = v
			}
		}

		if svc.Healthcheck != nil {
			hc := &agboxv1.HealthcheckConfig{
				Test:    svc.Healthcheck.Test,
				Retries: svc.Healthcheck.Retries,
			}
			var err error
			if hc.Interval, err = parseOptionalDuration("interval", svc.Healthcheck.Interval); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			if hc.Timeout, err = parseOptionalDuration("timeout", svc.Healthcheck.Timeout); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			if hc.StartPeriod, err = parseOptionalDuration("start_period", svc.Healthcheck.StartPeriod); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			if hc.StartInterval, err = parseOptionalDuration("start_interval", svc.Healthcheck.StartInterval); err != nil {
				return nil, fmt.Errorf("service %q: %w", name, err)
			}
			protoSvc.Healthcheck = hc
		}

		result = append(result, protoSvc)
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
	if len(override.GetRequiredServices()) > 0 {
		result.RequiredServices = cloneServiceSpecs(override.GetRequiredServices())
	}
	if len(override.GetOptionalServices()) > 0 {
		result.OptionalServices = cloneServiceSpecs(override.GetOptionalServices())
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

	return result
}
