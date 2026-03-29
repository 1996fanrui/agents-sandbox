package control

import (
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestYAMLConfigParsing(t *testing.T) {
	raw := []byte(`
image: "ghcr.io/agents-sandbox/coding-runtime:latest"
mounts:
  - source: /host/data
    target: /container/data
    writable: true
copies:
  - source: /host/project
    target: /workspace/project
    exclude_patterns:
      - "*.log"
      - ".git"
builtin_tools:
  - "claude"
required_services:
  db:
    image: postgres:16
    envs:
      POSTGRES_USER: admin
      POSTGRES_DB: mydb
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "admin"]
      interval: "2s"
      timeout: "1s"
      retries: 5
      start_period: "10s"
      start_interval: "500ms"
    post_start_on_primary:
      - "echo db ready"
optional_services:
  cache:
    image: redis:7
    envs:
      REDIS_MAX_MEMORY: "256mb"
labels:
  owner: team-a
  env: dev
envs:
  APP_ENV: production
  DB_HOST: localhost
`)

	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}

	if cfg.Image != "ghcr.io/agents-sandbox/coding-runtime:latest" {
		t.Fatalf("unexpected image: %s", cfg.Image)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Source != "/host/data" || cfg.Mounts[0].Target != "/container/data" || !cfg.Mounts[0].Writable {
		t.Fatalf("unexpected mounts: %#v", cfg.Mounts)
	}
	if len(cfg.Copies) != 1 || cfg.Copies[0].Source != "/host/project" || len(cfg.Copies[0].ExcludePatterns) != 2 {
		t.Fatalf("unexpected copies: %#v", cfg.Copies)
	}
	if len(cfg.BuiltinTools) != 1 || cfg.BuiltinTools[0] != "claude" {
		t.Fatalf("unexpected builtin_tools: %v", cfg.BuiltinTools)
	}
	if len(cfg.RequiredServices) != 1 {
		t.Fatalf("expected 1 required service, got %d", len(cfg.RequiredServices))
	}
	db := cfg.RequiredServices["db"]
	if db.Image != "postgres:16" {
		t.Fatalf("unexpected db image: %s", db.Image)
	}
	if db.Envs["POSTGRES_USER"] != "admin" || db.Envs["POSTGRES_DB"] != "mydb" {
		t.Fatalf("unexpected db envs: %v", db.Envs)
	}
	if db.Healthcheck == nil || db.Healthcheck.Retries != 5 || db.Healthcheck.Interval != "2s" {
		t.Fatalf("unexpected db healthcheck: %#v", db.Healthcheck)
	}
	if len(db.PostStartOnPrimary) != 1 || db.PostStartOnPrimary[0] != "echo db ready" {
		t.Fatalf("unexpected post_start_on_primary: %v", db.PostStartOnPrimary)
	}
	if len(cfg.OptionalServices) != 1 {
		t.Fatalf("expected 1 optional service, got %d", len(cfg.OptionalServices))
	}
	cache := cfg.OptionalServices["cache"]
	if cache.Image != "redis:7" {
		t.Fatalf("unexpected cache image: %s", cache.Image)
	}
	if len(cfg.Labels) != 2 || cfg.Labels["owner"] != "team-a" || cfg.Labels["env"] != "dev" {
		t.Fatalf("unexpected labels: %v", cfg.Labels)
	}
	if len(cfg.Envs) != 2 || cfg.Envs["APP_ENV"] != "production" || cfg.Envs["DB_HOST"] != "localhost" {
		t.Fatalf("unexpected envs: %v", cfg.Envs)
	}
}

func TestYAMLConfigUnknownFields(t *testing.T) {
	raw := []byte(`
image: "test:latest"
env: prod
`)
	_, err := parseYAMLConfig(raw)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestYAMLConfigToCreateSpec(t *testing.T) {
	cfg := &YAMLConfig{
		Image:            "test:latest",
		BuiltinTools: []string{"claude"},
		Mounts: []YAMLMountSpec{
			{Source: "/src", Target: "/dst", Writable: true},
		},
		Copies: []YAMLCopySpec{
			{Source: "/project", Target: "/workspace", ExcludePatterns: []string{".git"}},
		},
		RequiredServices: map[string]YAMLServiceSpec{
			"beta": {
				Image: "beta:1",
				Envs: map[string]string{
					"Z_KEY": "z_val",
					"A_KEY": "a_val",
				},
				Healthcheck: &YAMLHealthcheckConfig{
					Test:     []string{"CMD", "true"},
					Interval: "1s",
					Retries:  3,
				},
				PostStartOnPrimary: []string{"echo ready"},
			},
			"alpha": {
				Image: "alpha:1",
				Healthcheck: &YAMLHealthcheckConfig{
					Test: []string{"CMD", "true"},
				},
			},
		},
		OptionalServices: map[string]YAMLServiceSpec{
			"cache": {Image: "redis:7"},
		},
		Labels: map[string]string{"owner": "team-a"},
		Envs:   map[string]string{"Z_VAR": "z_val", "A_VAR": "a_val"},
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if spec.GetImage() != "test:latest" {
		t.Fatalf("unexpected image: %s", spec.GetImage())
	}
	if len(spec.GetBuiltinTools()) != 1 || spec.GetBuiltinTools()[0] != "claude" {
		t.Fatalf("unexpected builtin_tools: %v", spec.GetBuiltinTools())
	}
	if len(spec.GetMounts()) != 1 || spec.GetMounts()[0].GetSource() != "/src" {
		t.Fatalf("unexpected mounts: %v", spec.GetMounts())
	}
	if len(spec.GetCopies()) != 1 || spec.GetCopies()[0].GetSource() != "/project" {
		t.Fatalf("unexpected copies: %v", spec.GetCopies())
	}

	// Required services should be sorted alphabetically: alpha before beta.
	required := spec.GetRequiredServices()
	if len(required) != 2 {
		t.Fatalf("expected 2 required services, got %d", len(required))
	}
	if required[0].GetName() != "alpha" || required[1].GetName() != "beta" {
		t.Fatalf("required services not sorted: %s, %s", required[0].GetName(), required[1].GetName())
	}

	// Beta's envs should contain A_KEY and Z_KEY.
	betaEnvs := required[1].GetEnvs()
	if len(betaEnvs) != 2 {
		t.Fatalf("expected 2 env vars for beta, got %d", len(betaEnvs))
	}
	if betaEnvs["A_KEY"] != "a_val" || betaEnvs["Z_KEY"] != "z_val" {
		t.Fatalf("unexpected beta envs: %v", betaEnvs)
	}

	// Verify healthcheck fields including parsed duration.
	hc := required[1].GetHealthcheck()
	if hc.GetRetries() != 3 {
		t.Fatalf("unexpected healthcheck retries: %#v", hc)
	}
	if hc.GetInterval().AsDuration() != time.Second {
		t.Fatalf("unexpected healthcheck interval: %v", hc.GetInterval())
	}

	// Verify post_start_on_primary.
	if len(required[1].GetPostStartOnPrimary()) != 1 || required[1].GetPostStartOnPrimary()[0] != "echo ready" {
		t.Fatalf("unexpected post_start_on_primary: %v", required[1].GetPostStartOnPrimary())
	}

	// Optional services.
	optional := spec.GetOptionalServices()
	if len(optional) != 1 || optional[0].GetName() != "cache" {
		t.Fatalf("unexpected optional services: %v", optional)
	}

	// Labels.
	if spec.GetLabels()["owner"] != "team-a" {
		t.Fatalf("unexpected labels: %v", spec.GetLabels())
	}

	// Envs are a map.
	envs := spec.GetEnvs()
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
	if envs["A_VAR"] != "a_val" || envs["Z_VAR"] != "z_val" {
		t.Fatalf("unexpected env values: %v", envs)
	}

	// idle_ttl not set in this config, so it should be nil.
	if spec.GetIdleTtl() != nil {
		t.Fatalf("expected nil idle_ttl, got %v", spec.GetIdleTtl())
	}
}

func TestYAMLConfigToCreateSpecIdleTTL(t *testing.T) {
	cfg := &YAMLConfig{
		Image:   "test:latest",
		IdleTTL: "10m",
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if spec.GetIdleTtl() == nil {
		t.Fatal("expected idle_ttl to be set, got nil")
	}
	if spec.GetIdleTtl().AsDuration() != 10*time.Minute {
		t.Fatalf("expected 10m, got %v", spec.GetIdleTtl().AsDuration())
	}
}

func TestYAMLConfigServiceTypes(t *testing.T) {
	cfg := &YAMLConfig{
		Image: "test:latest",
		RequiredServices: map[string]YAMLServiceSpec{
			"db": {
				Image: "postgres:16",
				Healthcheck: &YAMLHealthcheckConfig{
					Test: []string{"CMD", "pg_isready"},
				},
			},
		},
		OptionalServices: map[string]YAMLServiceSpec{
			"cache": {Image: "redis:7"},
		},
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if len(spec.GetRequiredServices()) != 1 || spec.GetRequiredServices()[0].GetName() != "db" {
		t.Fatalf("required services mismatch: %v", spec.GetRequiredServices())
	}
	if len(spec.GetOptionalServices()) != 1 || spec.GetOptionalServices()[0].GetName() != "cache" {
		t.Fatalf("optional services mismatch: %v", spec.GetOptionalServices())
	}
}

func TestYAMLInvalidDurationRejected(t *testing.T) {
	cfg := &YAMLConfig{
		Image: "test:latest",
		RequiredServices: map[string]YAMLServiceSpec{
			"db": {
				Image: "postgres:16",
				Healthcheck: &YAMLHealthcheckConfig{
					Test:     []string{"CMD", "pg_isready"},
					Interval: "invalid",
				},
			},
		},
	}

	_, err := yamlConfigToCreateSpec(cfg)
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
}

func TestYAMLAllDurationFieldsParsed(t *testing.T) {
	cfg := &YAMLConfig{
		Image:   "test:latest",
		IdleTTL: "15m",
		RequiredServices: map[string]YAMLServiceSpec{
			"db": {
				Image: "postgres:16",
				Healthcheck: &YAMLHealthcheckConfig{
					Test:          []string{"CMD", "pg_isready"},
					Interval:      "2s",
					Timeout:       "500ms",
					StartPeriod:   "30s",
					StartInterval: "1s",
				},
			},
		},
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if spec.GetIdleTtl() == nil || spec.GetIdleTtl().AsDuration() != 15*time.Minute {
		t.Fatalf("unexpected idle_ttl: %v", spec.GetIdleTtl())
	}

	hc := spec.GetRequiredServices()[0].GetHealthcheck()
	if hc.GetInterval().AsDuration() != 2*time.Second {
		t.Fatalf("unexpected interval: %v", hc.GetInterval())
	}
	if hc.GetTimeout().AsDuration() != 500*time.Millisecond {
		t.Fatalf("unexpected timeout: %v", hc.GetTimeout())
	}
	if hc.GetStartPeriod().AsDuration() != 30*time.Second {
		t.Fatalf("unexpected start_period: %v", hc.GetStartPeriod())
	}
	if hc.GetStartInterval().AsDuration() != time.Second {
		t.Fatalf("unexpected start_interval: %v", hc.GetStartInterval())
	}
}

func TestMergeCreateSpecs(t *testing.T) {
	tests := []struct {
		name     string
		base     *agboxv1.CreateSpec
		override *agboxv1.CreateSpec
		check    func(t *testing.T, result *agboxv1.CreateSpec)
	}{
		{
			name:     "image_override",
			base:     &agboxv1.CreateSpec{Image: "base:v1"},
			override: &agboxv1.CreateSpec{Image: "override:v2"},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetImage() != "override:v2" {
					t.Fatalf("expected override:v2, got %s", result.GetImage())
				}
			},
		},
		{
			name:     "image_no_override",
			base:     &agboxv1.CreateSpec{Image: "base:v1"},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetImage() != "base:v1" {
					t.Fatalf("expected base:v1, got %s", result.GetImage())
				}
			},
		},
		{
			name: "repeated_override",
			base: &agboxv1.CreateSpec{
				Mounts: []*agboxv1.MountSpec{{Source: "/base", Target: "/base"}},
			},
			override: &agboxv1.CreateSpec{
				Mounts: []*agboxv1.MountSpec{{Source: "/override", Target: "/override"}},
			},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if len(result.GetMounts()) != 1 || result.GetMounts()[0].GetSource() != "/override" {
					t.Fatalf("expected override mounts, got %v", result.GetMounts())
				}
			},
		},
		{
			name: "repeated_no_override",
			base: &agboxv1.CreateSpec{
				Mounts: []*agboxv1.MountSpec{{Source: "/base", Target: "/base"}},
			},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if len(result.GetMounts()) != 1 || result.GetMounts()[0].GetSource() != "/base" {
					t.Fatalf("expected base mounts, got %v", result.GetMounts())
				}
			},
		},
		{
			name: "labels_merge",
			base: &agboxv1.CreateSpec{
				Labels: map[string]string{"a": "1", "b": "2"},
			},
			override: &agboxv1.CreateSpec{
				Labels: map[string]string{"b": "3", "c": "4"},
			},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				labels := result.GetLabels()
				if labels["a"] != "1" || labels["b"] != "3" || labels["c"] != "4" {
					t.Fatalf("unexpected merged labels: %v", labels)
				}
				if len(labels) != 3 {
					t.Fatalf("expected 3 labels, got %d", len(labels))
				}
			},
		},
		{
			name: "labels_no_override",
			base: &agboxv1.CreateSpec{
				Labels: map[string]string{"a": "1"},
			},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetLabels()["a"] != "1" {
					t.Fatalf("expected base labels preserved, got %v", result.GetLabels())
				}
			},
		},
		{
			name: "envs_merge",
			base: &agboxv1.CreateSpec{
				Envs: map[string]string{"A": "1", "B": "2"},
			},
			override: &agboxv1.CreateSpec{
				Envs: map[string]string{"B": "3", "C": "4"},
			},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				envs := result.GetEnvs()
				if envs["A"] != "1" || envs["B"] != "3" || envs["C"] != "4" {
					t.Fatalf("unexpected merged envs: %v", envs)
				}
				if len(envs) != 3 {
					t.Fatalf("expected 3 envs, got %d", len(envs))
				}
			},
		},
		{
			name: "envs_no_override",
			base: &agboxv1.CreateSpec{
				Envs: map[string]string{"A": "1"},
			},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetEnvs()["A"] != "1" {
					t.Fatalf("expected base envs preserved, got %v", result.GetEnvs())
				}
			},
		},
		{
			name: "nil_base",
			base: nil,
			override: &agboxv1.CreateSpec{
				Image:  "override:v1",
				Labels: map[string]string{"x": "y"},
			},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetImage() != "override:v1" {
					t.Fatalf("expected override:v1, got %s", result.GetImage())
				}
				if result.GetLabels()["x"] != "y" {
					t.Fatalf("expected override labels, got %v", result.GetLabels())
				}
			},
		},
		{
			name:     "nil_override",
			base:     &agboxv1.CreateSpec{Image: "base:v1"},
			override: nil,
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetImage() != "base:v1" {
					t.Fatalf("expected base:v1, got %s", result.GetImage())
				}
			},
		},
		{
			name:     "idle_ttl_override",
			base:     &agboxv1.CreateSpec{IdleTtl: durationpb.New(5 * time.Minute)},
			override: &agboxv1.CreateSpec{IdleTtl: durationpb.New(10 * time.Minute)},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetIdleTtl() == nil || result.GetIdleTtl().AsDuration() != 10*time.Minute {
					t.Fatalf("expected 10m idle_ttl, got %v", result.GetIdleTtl())
				}
			},
		},
		{
			name:     "idle_ttl_no_override",
			base:     &agboxv1.CreateSpec{IdleTtl: durationpb.New(5 * time.Minute)},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if result.GetIdleTtl() == nil || result.GetIdleTtl().AsDuration() != 5*time.Minute {
					t.Fatalf("expected base 5m idle_ttl preserved, got %v", result.GetIdleTtl())
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeCreateSpecs(tc.base, tc.override)
			tc.check(t, result)
		})
	}
}
