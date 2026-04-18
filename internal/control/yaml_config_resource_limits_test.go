package control

import (
	"strings"
	"testing"
)

// TestYAMLResourceLimitsPassthrough covers AT-YAM1: resource limit strings
// on both the primary spec and companion containers flow verbatim from YAML
// into the generated proto CreateSpec.
func TestYAMLResourceLimitsPassthrough(t *testing.T) {
	raw := []byte(`
image: "example:latest"
cpu_limit: "2"
memory_limit: "4g"
disk_limit: "10g"
companion_containers:
  db:
    image: postgres:16
    cpu_limit: "1"
    memory_limit: "512m"
    disk_limit: "5g"
`)

	cfg, err := parseYAMLConfig(raw)
	if err != nil {
		t.Fatalf("parseYAMLConfig failed: %v", err)
	}
	spec, err := yamlConfigToCreateSpec(cfg)
	if err != nil {
		t.Fatalf("yamlConfigToCreateSpec failed: %v", err)
	}

	if spec.GetCpuLimit() != "2" {
		t.Fatalf("cpu_limit mismatch: got %q, want %q", spec.GetCpuLimit(), "2")
	}
	if spec.GetMemoryLimit() != "4g" {
		t.Fatalf("memory_limit mismatch: got %q, want %q", spec.GetMemoryLimit(), "4g")
	}
	if spec.GetDiskLimit() != "10g" {
		t.Fatalf("disk_limit mismatch: got %q, want %q", spec.GetDiskLimit(), "10g")
	}

	if len(spec.GetCompanionContainers()) != 1 {
		t.Fatalf("expected 1 companion container, got %d", len(spec.GetCompanionContainers()))
	}
	cc := spec.GetCompanionContainers()[0]
	if cc.GetName() != "db" {
		t.Fatalf("companion name mismatch: got %q", cc.GetName())
	}
	if cc.GetCpuLimit() != "1" {
		t.Fatalf("companion cpu_limit: got %q, want %q", cc.GetCpuLimit(), "1")
	}
	if cc.GetMemoryLimit() != "512m" {
		t.Fatalf("companion memory_limit: got %q, want %q", cc.GetMemoryLimit(), "512m")
	}
	if cc.GetDiskLimit() != "5g" {
		t.Fatalf("companion disk_limit: got %q, want %q", cc.GetDiskLimit(), "5g")
	}
}

// TestYAMLCompanionUnknownResourceLimitFieldRejected guards against
// regressing back to a disk-only YAML schema by smuggling an unknown
// companion field past KnownFields(true).
func TestYAMLCompanionUnknownResourceLimitFieldRejected(t *testing.T) {
	raw := []byte(`
image: "example:latest"
companion_containers:
  db:
    image: postgres:16
    bogus_limit: "1"
`)
	if _, err := parseYAMLConfig(raw); err == nil {
		t.Fatal("expected parse to reject unknown companion field")
	} else if !strings.Contains(err.Error(), "bogus_limit") {
		t.Fatalf("error should mention unknown field, got %v", err)
	}
}
