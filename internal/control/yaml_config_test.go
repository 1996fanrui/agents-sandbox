package control

import (
	"reflect"
	"strings"
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
companion_containers:
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
	if len(cfg.CompanionContainers) != 2 {
		t.Fatalf("expected 2 companion containers, got %d", len(cfg.CompanionContainers))
	}
	db := cfg.CompanionContainers["db"]
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
	cache := cfg.CompanionContainers["cache"]
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
		Image:        "test:latest",
		BuiltinTools: []string{"claude"},
		Mounts: []YAMLMountSpec{
			{Source: "/src", Target: "/dst", Writable: true},
		},
		Copies: []YAMLCopySpec{
			{Source: "/project", Target: "/workspace", ExcludePatterns: []string{".git"}},
		},
		CompanionContainers: map[string]YAMLCompanionContainerSpec{
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

	// Companion containers should be sorted alphabetically: alpha before beta before cache.
	cc := spec.GetCompanionContainers()
	if len(cc) != 3 {
		t.Fatalf("expected 3 companion containers, got %d", len(cc))
	}
	if cc[0].GetName() != "alpha" || cc[1].GetName() != "beta" || cc[2].GetName() != "cache" {
		t.Fatalf("companion containers not sorted: %s, %s, %s", cc[0].GetName(), cc[1].GetName(), cc[2].GetName())
	}

	// Beta's envs should contain A_KEY and Z_KEY.
	betaEnvs := cc[1].GetEnvs()
	if len(betaEnvs) != 2 {
		t.Fatalf("expected 2 env vars for beta, got %d", len(betaEnvs))
	}
	if betaEnvs["A_KEY"] != "a_val" || betaEnvs["Z_KEY"] != "z_val" {
		t.Fatalf("unexpected beta envs: %v", betaEnvs)
	}

	// Verify healthcheck fields including parsed duration.
	hc := cc[1].GetHealthcheck()
	if hc.GetRetries() != 3 {
		t.Fatalf("unexpected healthcheck retries: %#v", hc)
	}
	if hc.GetInterval().AsDuration() != time.Second {
		t.Fatalf("unexpected healthcheck interval: %v", hc.GetInterval())
	}

	// Verify post_start_on_primary.
	if len(cc[1].GetPostStartOnPrimary()) != 1 || cc[1].GetPostStartOnPrimary()[0] != "echo ready" {
		t.Fatalf("unexpected post_start_on_primary: %v", cc[1].GetPostStartOnPrimary())
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

func TestYAMLConfigCompanionContainerTypes(t *testing.T) {
	cfg := &YAMLConfig{
		Image: "test:latest",
		CompanionContainers: map[string]YAMLCompanionContainerSpec{
			"db": {
				Image: "postgres:16",
				Healthcheck: &YAMLHealthcheckConfig{
					Test: []string{"CMD", "pg_isready"},
				},
			},
			"cache": {Image: "redis:7"},
		},
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if len(spec.GetCompanionContainers()) != 2 {
		t.Fatalf("companion containers mismatch: %v", spec.GetCompanionContainers())
	}
	if spec.GetCompanionContainers()[0].GetName() != "cache" || spec.GetCompanionContainers()[1].GetName() != "db" {
		t.Fatalf("companion containers not sorted: %v", spec.GetCompanionContainers())
	}
}

func TestYAMLInvalidDurationRejected(t *testing.T) {
	cfg := &YAMLConfig{
		Image: "test:latest",
		CompanionContainers: map[string]YAMLCompanionContainerSpec{
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
		CompanionContainers: map[string]YAMLCompanionContainerSpec{
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

	hc := spec.GetCompanionContainers()[0].GetHealthcheck()
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

func TestYAMLConfigWithPorts(t *testing.T) {
	raw := []byte(`
image: "test:latest"
ports:
  - container_port: 8080
    host_port: 9090
    protocol: tcp
  - container_port: 53
    host_port: 5353
    protocol: udp
  - container_port: 3000
    host_port: 3000
`)
	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	if len(cfg.Ports) != 3 {
		t.Fatalf("expected 3 ports, got %d", len(cfg.Ports))
	}

	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}
	ports := spec.GetPorts()
	if len(ports) != 3 {
		t.Fatalf("expected 3 ports in spec, got %d", len(ports))
	}
	if ports[0].GetContainerPort() != 8080 || ports[0].GetHostPort() != 9090 || ports[0].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_TCP {
		t.Fatalf("unexpected port[0]: %v", ports[0])
	}
	if ports[1].GetContainerPort() != 53 || ports[1].GetHostPort() != 5353 || ports[1].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_UDP {
		t.Fatalf("unexpected port[1]: %v", ports[1])
	}
	// Empty protocol defaults to TCP.
	if ports[2].GetContainerPort() != 3000 || ports[2].GetHostPort() != 3000 || ports[2].GetProtocol() != agboxv1.PortProtocol_PORT_PROTOCOL_TCP {
		t.Fatalf("unexpected port[2]: %v", ports[2])
	}
}

func TestParsePortProtocol(t *testing.T) {
	testCases := []struct {
		input    string
		expected agboxv1.PortProtocol
		wantErr  bool
	}{
		{"", agboxv1.PortProtocol_PORT_PROTOCOL_TCP, false},
		{"tcp", agboxv1.PortProtocol_PORT_PROTOCOL_TCP, false},
		{"TCP", agboxv1.PortProtocol_PORT_PROTOCOL_TCP, false},
		{"udp", agboxv1.PortProtocol_PORT_PROTOCOL_UDP, false},
		{"UDP", agboxv1.PortProtocol_PORT_PROTOCOL_UDP, false},
		{"sctp", agboxv1.PortProtocol_PORT_PROTOCOL_SCTP, false},
		{"SCTP", agboxv1.PortProtocol_PORT_PROTOCOL_SCTP, false},
		{"ftp", 0, true},
		{"http", 0, true},
	}

	for _, tc := range testCases {
		t.Run("protocol_"+tc.input, func(t *testing.T) {
			got, err := parsePortProtocol(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for %q: %v", tc.input, err)
				}
				if got != tc.expected {
					t.Fatalf("expected %v for %q, got %v", tc.expected, tc.input, got)
				}
			}
		})
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
		{
			name: "ports_override",
			base: &agboxv1.CreateSpec{
				Ports: []*agboxv1.PortMapping{
					{ContainerPort: 8080, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
				},
			},
			override: &agboxv1.CreateSpec{
				Ports: []*agboxv1.PortMapping{
					{ContainerPort: 3000, HostPort: 3000, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
					{ContainerPort: 53, HostPort: 5353, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
				},
			},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if len(result.GetPorts()) != 2 {
					t.Fatalf("expected 2 ports, got %d", len(result.GetPorts()))
				}
				if result.GetPorts()[0].GetContainerPort() != 3000 {
					t.Fatalf("expected override port 3000, got %d", result.GetPorts()[0].GetContainerPort())
				}
			},
		},
		{
			name: "ports_no_override",
			base: &agboxv1.CreateSpec{
				Ports: []*agboxv1.PortMapping{
					{ContainerPort: 8080, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
				},
			},
			override: &agboxv1.CreateSpec{},
			check: func(t *testing.T, result *agboxv1.CreateSpec) {
				if len(result.GetPorts()) != 1 || result.GetPorts()[0].GetContainerPort() != 8080 {
					t.Fatalf("expected base ports preserved, got %v", result.GetPorts())
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

func TestYAMLParsePrimaryCommand(t *testing.T) {
	raw := []byte(`
image: "ghcr.io/agents-sandbox/coding-runtime:test"
command: ["myworker", "serve", "--foreground"]
`)
	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}
	want := []string{"myworker", "serve", "--foreground"}
	if got := spec.GetCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected primary command: got %#v want %#v", got, want)
	}

	base := &agboxv1.CreateSpec{
		Image:   "base",
		Command: []string{"base-cmd"},
	}
	override := &agboxv1.CreateSpec{
		Command: []string{"override", "serve"},
	}
	merged := mergeCreateSpecs(base, override)
	if got := merged.GetCommand(); !reflect.DeepEqual(got, []string{"override", "serve"}) {
		t.Fatalf("override should replace base command, got %#v", got)
	}

	emptyOverride := &agboxv1.CreateSpec{}
	merged = mergeCreateSpecs(base, emptyOverride)
	if got := merged.GetCommand(); !reflect.DeepEqual(got, []string{"base-cmd"}) {
		t.Fatalf("empty override should preserve base command, got %#v", got)
	}
}

func TestYAMLParseCompanionCommand(t *testing.T) {
	raw := []byte(`
image: "ghcr.io/agents-sandbox/coding-runtime:test"
companion_containers:
  cache:
    image: redis:7
    command: ["redis-server", "--appendonly", "yes"]
`)
	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}
	cc := spec.GetCompanionContainers()
	if len(cc) != 1 {
		t.Fatalf("expected 1 companion container, got %d", len(cc))
	}
	want := []string{"redis-server", "--appendonly", "yes"}
	if got := cc[0].GetCommand(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected companion command: got %#v want %#v", got, want)
	}
}

func TestYAMLRejectsEmptyCommandArray(t *testing.T) {
	primaryYAML := []byte(`
image: "img:test"
command: []
`)
	cfg, err := parseYAMLConfig(primaryYAML)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	_, err = yamlConfigToCreateSpec(cfg)
	if err == nil {
		t.Fatal("expected error for primary command: []")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty array") || !strings.Contains(msg, "command") {
		t.Fatalf("primary error should mention 'empty array' and 'command', got %q", msg)
	}

	companionYAML := []byte(`
image: "img:test"
companion_containers:
  cache:
    image: redis:7
    command: []
`)
	cfg, err = parseYAMLConfig(companionYAML)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	_, err = yamlConfigToCreateSpec(cfg)
	if err == nil {
		t.Fatal("expected error for companion command: []")
	}
	msg = err.Error()
	if !strings.Contains(msg, "empty array") || !strings.Contains(msg, "command") {
		t.Fatalf("companion error should mention 'empty array' and 'command', got %q", msg)
	}
	if !strings.Contains(msg, "cache") {
		t.Fatalf("companion error should include companion name 'cache', got %q", msg)
	}
}

func TestYAMLRejectsEmptyStringInPrimaryCommand(t *testing.T) {
	raw := []byte(`
image: "img:test"
command: ["foo", ""]
`)
	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	_, err = yamlConfigToCreateSpec(cfg)
	if err == nil {
		t.Fatal(`expected error for primary command with empty-string element`)
	}
	msg := err.Error()
	if !strings.Contains(msg, "command") {
		t.Fatalf("primary error should mention 'command', got %q", msg)
	}
	if !strings.Contains(msg, "[1]") {
		t.Fatalf("primary error should include offending index '[1]', got %q", msg)
	}
	if !strings.Contains(msg, "empty string") {
		t.Fatalf("primary error should mention 'empty string', got %q", msg)
	}
}

func TestYAMLRejectsEmptyStringInCompanionCommand(t *testing.T) {
	raw := []byte(`
image: "img:test"
companion_containers:
  redis:
    image: redis:7
    command: ["redis-server", ""]
`)
	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	_, err = yamlConfigToCreateSpec(cfg)
	if err == nil {
		t.Fatal(`expected error for companion command with empty-string element`)
	}
	msg := err.Error()
	if !strings.Contains(msg, "command") {
		t.Fatalf("companion error should mention 'command', got %q", msg)
	}
	if !strings.Contains(msg, "redis") {
		t.Fatalf("companion error should include companion name 'redis', got %q", msg)
	}
	if !strings.Contains(msg, "[1]") {
		t.Fatalf("companion error should include offending index '[1]', got %q", msg)
	}
	if !strings.Contains(msg, "empty string") {
		t.Fatalf("companion error should mention 'empty string', got %q", msg)
	}
}
