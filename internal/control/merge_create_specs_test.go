package control

import (
	"reflect"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// TestMergeCreateSpecs_RepeatedFieldsAppend covers AT-MZ5V: every repeated
// structured field (mounts / copies / builtin_tools / companion_containers /
// ports) must follow base+override append with base-first ordering.
func TestMergeCreateSpecs_RepeatedFieldsAppend(t *testing.T) {
	base := &agboxv1.CreateSpec{
		Mounts: []*agboxv1.MountSpec{
			{Source: "/base/m1", Target: "/base/m1"},
			{Source: "/base/m2", Target: "/base/m2"},
		},
		Copies: []*agboxv1.CopySpec{
			{Source: "/base/c1", Target: "/base/c1"},
		},
		BuiltinTools: []string{"claude"},
		CompanionContainers: []*agboxv1.CompanionContainerSpec{
			{Name: "base-cc", Image: "base:1"},
		},
		Ports: []*agboxv1.PortMapping{
			{ContainerPort: 8000, HostPort: 8000, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		},
	}
	override := &agboxv1.CreateSpec{
		Mounts: []*agboxv1.MountSpec{
			{Source: "/ovr/m3", Target: "/ovr/m3"},
		},
		Copies: []*agboxv1.CopySpec{
			{Source: "/ovr/c2", Target: "/ovr/c2"},
			{Source: "/ovr/c3", Target: "/ovr/c3"},
		},
		BuiltinTools: []string{"uv"},
		CompanionContainers: []*agboxv1.CompanionContainerSpec{
			{Name: "ovr-cc1", Image: "ovr:1"},
			{Name: "ovr-cc2", Image: "ovr:2"},
		},
		Ports: []*agboxv1.PortMapping{
			{ContainerPort: 9000, HostPort: 9000, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		},
	}

	result := mergeCreateSpecs(base, override)

	// mounts: 2 base + 1 override = 3, base-first.
	if got := len(result.GetMounts()); got != 3 {
		t.Fatalf("mounts: expected 3, got %d", got)
	}
	if result.GetMounts()[0].GetTarget() != "/base/m1" || result.GetMounts()[1].GetTarget() != "/base/m2" || result.GetMounts()[2].GetTarget() != "/ovr/m3" {
		t.Fatalf("mounts ordering wrong: %v", result.GetMounts())
	}

	// copies: 1 base + 2 override = 3, base-first.
	if got := len(result.GetCopies()); got != 3 {
		t.Fatalf("copies: expected 3, got %d", got)
	}
	if result.GetCopies()[0].GetTarget() != "/base/c1" || result.GetCopies()[1].GetTarget() != "/ovr/c2" || result.GetCopies()[2].GetTarget() != "/ovr/c3" {
		t.Fatalf("copies ordering wrong: %v", result.GetCopies())
	}

	// builtin_tools: 1 base + 1 override = 2, base-first.
	if got := result.GetBuiltinTools(); !reflect.DeepEqual(got, []string{"claude", "uv"}) {
		t.Fatalf("builtin_tools ordering wrong: %v", got)
	}

	// companion_containers: 1 base + 2 override = 3, base-first.
	if got := len(result.GetCompanionContainers()); got != 3 {
		t.Fatalf("companion_containers: expected 3, got %d", got)
	}
	cc := result.GetCompanionContainers()
	if cc[0].GetName() != "base-cc" || cc[1].GetName() != "ovr-cc1" || cc[2].GetName() != "ovr-cc2" {
		t.Fatalf("companion_containers ordering wrong: %v", cc)
	}

	// ports: 1 base + 1 override = 2, base-first.
	if got := len(result.GetPorts()); got != 2 {
		t.Fatalf("ports: expected 2, got %d", got)
	}
	if result.GetPorts()[0].GetHostPort() != 8000 || result.GetPorts()[1].GetHostPort() != 9000 {
		t.Fatalf("ports ordering wrong: %v", result.GetPorts())
	}
}

// TestMergeCreateSpecs_CommandReplace covers AT-MZ5V: command stays replace
// (single-command semantics, append has no executable meaning).
func TestMergeCreateSpecs_CommandReplace(t *testing.T) {
	base := &agboxv1.CreateSpec{Command: []string{"base", "serve"}}
	override := &agboxv1.CreateSpec{Command: []string{"override"}}
	result := mergeCreateSpecs(base, override)
	if got := result.GetCommand(); !reflect.DeepEqual(got, []string{"override"}) {
		t.Fatalf("expected override command to replace base, got %v", got)
	}

	// Empty override preserves base command.
	result = mergeCreateSpecs(base, &agboxv1.CreateSpec{})
	if got := result.GetCommand(); !reflect.DeepEqual(got, []string{"base", "serve"}) {
		t.Fatalf("expected base command preserved with empty override, got %v", got)
	}
}

// TestMergeCreateSpecs_MapKeyMerge covers AT-MZ5V: labels and envs merge by
// key — base-only keys preserved, same key in override wins, override-only
// keys appended.
func TestMergeCreateSpecs_MapKeyMerge(t *testing.T) {
	base := &agboxv1.CreateSpec{
		Labels: map[string]string{"a": "1", "b": "2"},
		Envs:   map[string]string{"a": "1", "b": "2"},
	}
	override := &agboxv1.CreateSpec{
		Labels: map[string]string{"b": "3", "c": "4"},
		Envs:   map[string]string{"b": "3", "c": "4"},
	}
	result := mergeCreateSpecs(base, override)

	wantMap := map[string]string{"a": "1", "b": "3", "c": "4"}
	if got := result.GetLabels(); !reflect.DeepEqual(got, wantMap) {
		t.Fatalf("labels merge wrong: got %v want %v", got, wantMap)
	}
	if got := result.GetEnvs(); !reflect.DeepEqual(got, wantMap) {
		t.Fatalf("envs merge wrong: got %v want %v", got, wantMap)
	}
}

func TestMergeCreateSpecsGPUsScalarOverride(t *testing.T) {
	base := &agboxv1.CreateSpec{Gpus: "all"}

	if got := mergeCreateSpecs(base, &agboxv1.CreateSpec{}).GetGpus(); got != "all" {
		t.Fatalf("empty override should preserve base gpus, got %q", got)
	}

	if got := mergeCreateSpecs(&agboxv1.CreateSpec{}, &agboxv1.CreateSpec{Gpus: "all"}).GetGpus(); got != "all" {
		t.Fatalf("non-empty override should set gpus, got %q", got)
	}
}

// TestMergeCreateSpecs_BuiltinToolsDedup covers AT-OJB2: builtin_tools is
// deduped after append, preserving first-occurrence order, so that declaring
// the same tool in both base and override is accepted (does not trigger the
// downstream duplicate check in validateCreateSpec).
func TestMergeCreateSpecs_BuiltinToolsDedup(t *testing.T) {
	base := &agboxv1.CreateSpec{BuiltinTools: []string{"claude", "git"}}
	override := &agboxv1.CreateSpec{BuiltinTools: []string{"git", "uv"}}
	result := mergeCreateSpecs(base, override)
	want := []string{"claude", "git", "uv"}
	if got := result.GetBuiltinTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v after dedupe, got %v", want, got)
	}
}

// openclawPresetCreateSpec mirrors cmd/agbox/openclaw.go's openclawConfigYaml
// at the YAMLConfig level. The CLI package is not importable from here, so
// the fixture is hand-coded to match the openclaw preset structure exactly:
// 1 mount (~/.openclaw → ~/.openclaw, writable) and 1 port (18789/tcp).
// If the openclaw preset YAML changes, this fixture must be updated to match.
func openclawPresetCreateSpec(t *testing.T) *agboxv1.CreateSpec {
	t.Helper()
	cfg := &YAMLConfig{
		Image: "ghcr.io/agents-sandbox/openclaw-runtime:latest",
		Mounts: []YAMLMountSpec{
			{Source: "/tmp/openclaw-fixture", Target: "/home/agbox/.openclaw", Writable: true},
		},
		Ports: []YAMLPortMapping{
			{HostPort: 18789, ContainerPort: 18789},
		},
		Envs: map[string]string{
			"OPENCLAW_STATE_DIR":   "/home/agbox/.openclaw",
			"OPENCLAW_CONFIG_PATH": "/home/agbox/.openclaw/config/openclaw.json",
		},
		Command: &[]string{"openclaw", "gateway", "run", "--port", "18789", "--bind", "lan"},
	}
	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("build openclaw preset spec: %v", err)
	}
	return spec
}

// TestMergeCreateSpecs_OpenClawPresetAppend covers AT-BLIH: the openclaw
// preset (1 mount + 1 port) plus a user override (1 mount with different
// target + 1 port with different host_port,protocol) must merge to 2 mounts
// and 2 ports, with openclaw's preset entries still present.
func TestMergeCreateSpecs_OpenClawPresetAppend(t *testing.T) {
	base := openclawPresetCreateSpec(t)
	override := &agboxv1.CreateSpec{
		Mounts: []*agboxv1.MountSpec{
			{Source: "/host/extra", Target: "/container/extra"},
		},
		Ports: []*agboxv1.PortMapping{
			{ContainerPort: 18789, HostPort: 9999, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_UDP},
		},
	}

	result := mergeCreateSpecs(base, override)

	if got := len(result.GetMounts()); got != 2 {
		t.Fatalf("expected 2 mounts after append, got %d", got)
	}
	foundOpenclaw := false
	for _, m := range result.GetMounts() {
		if m.GetTarget() == "/home/agbox/.openclaw" {
			foundOpenclaw = true
		}
	}
	if !foundOpenclaw {
		t.Fatalf("openclaw preset mount lost after append: %v", result.GetMounts())
	}

	if got := len(result.GetPorts()); got != 2 {
		t.Fatalf("expected 2 ports after append, got %d", got)
	}
	foundPreset := false
	for _, p := range result.GetPorts() {
		if p.GetHostPort() == 18789 && p.GetProtocol() == agboxv1.PortProtocol_PORT_PROTOCOL_TCP {
			foundPreset = true
		}
	}
	if !foundPreset {
		t.Fatalf("openclaw preset port (18789/tcp) lost after append: %v", result.GetPorts())
	}
}
