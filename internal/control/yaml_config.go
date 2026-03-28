package control

import (
	"bytes"
	"sort"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// YAMLConfig is the top-level schema for declarative sandbox configuration
// supplied via the config_yaml field in CreateSandboxRequest.
type YAMLConfig struct {
	Image            string                       `yaml:"image"`
	Mounts           []YAMLMountSpec              `yaml:"mounts"`
	Copies           []YAMLCopySpec               `yaml:"copies"`
	BuiltinResources []string                     `yaml:"builtin_resources"`
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
	Environment        map[string]string      `yaml:"environment"`
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
// Map keys for services and environment variables are sorted alphabetically
// to produce deterministic output.
func yamlConfigToCreateSpec(cfg *YAMLConfig) *agboxv1.CreateSpec {
	spec := &agboxv1.CreateSpec{
		Image:            cfg.Image,
		BuiltinResources: cfg.BuiltinResources,
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

	spec.RequiredServices = convertServiceMap(cfg.RequiredServices)
	spec.OptionalServices = convertServiceMap(cfg.OptionalServices)

	if len(cfg.Labels) > 0 {
		spec.Labels = make(map[string]string, len(cfg.Labels))
		for k, v := range cfg.Labels {
			spec.Labels[k] = v
		}
	}

	// Convert envs map to sorted KeyValue pairs.
	if len(cfg.Envs) > 0 {
		envKeys := make([]string, 0, len(cfg.Envs))
		for k := range cfg.Envs {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, ek := range envKeys {
			spec.Envs = append(spec.Envs, &agboxv1.KeyValue{
				Key:   ek,
				Value: cfg.Envs[ek],
			})
		}
	}

	return spec
}

// convertServiceMap converts a map of service name to YAMLServiceSpec into
// a sorted slice of proto ServiceSpec. The map key becomes ServiceSpec.Name.
func convertServiceMap(services map[string]YAMLServiceSpec) []*agboxv1.ServiceSpec {
	if len(services) == 0 {
		return nil
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

		// Convert environment map to sorted KeyValue pairs.
		if len(svc.Environment) > 0 {
			envKeys := make([]string, 0, len(svc.Environment))
			for k := range svc.Environment {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, ek := range envKeys {
				protoSvc.Environment = append(protoSvc.Environment, &agboxv1.KeyValue{
					Key:   ek,
					Value: svc.Environment[ek],
				})
			}
		}

		if svc.Healthcheck != nil {
			protoSvc.Healthcheck = &agboxv1.HealthcheckConfig{
				Test:          svc.Healthcheck.Test,
				Interval:      svc.Healthcheck.Interval,
				Timeout:       svc.Healthcheck.Timeout,
				Retries:       svc.Healthcheck.Retries,
				StartPeriod:   svc.Healthcheck.StartPeriod,
				StartInterval: svc.Healthcheck.StartInterval,
			}
		}

		result = append(result, protoSvc)
	}

	return result
}

// sortedKeyValues converts a map to a sorted slice of KeyValue pairs.
func sortedKeyValues(m map[string]string) []*agboxv1.KeyValue {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]*agboxv1.KeyValue, 0, len(keys))
	for _, k := range keys {
		result = append(result, &agboxv1.KeyValue{Key: k, Value: m[k]})
	}
	return result
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
	if len(override.GetBuiltinResources()) > 0 {
		result.BuiltinResources = append([]string(nil), override.GetBuiltinResources()...)
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
		merged := keyValuesToMap(result.GetEnvs())
		for _, kv := range override.GetEnvs() {
			merged[kv.GetKey()] = kv.GetValue()
		}
		result.Envs = sortedKeyValues(merged)
	}

	return result
}
