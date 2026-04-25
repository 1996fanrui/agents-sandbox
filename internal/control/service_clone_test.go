package control

import (
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// TestCloneCompanionContainerSpecsPreservesResourceLimits guards against a
// regression where cloneCompanionContainerSpecs silently dropped CpuLimit
// and MemoryLimit, so any code path going through this clone (e.g. merging
// companion_containers) lost these fields before reaching the runtime.
func TestCloneCompanionContainerSpecsPreservesResourceLimits(t *testing.T) {
	src := []*agboxv1.CompanionContainerSpec{
		{
			Name:        "redis",
			Image:       "redis:7",
			CpuLimit:    "0.5",
			MemoryLimit: "256m",
			DiskLimit:   "1g",
		},
	}
	cloned := cloneCompanionContainerSpecs(src)
	if len(cloned) != 1 {
		t.Fatalf("expected 1 clone, got %d", len(cloned))
	}
	if got, want := cloned[0].GetCpuLimit(), "0.5"; got != want {
		t.Fatalf("CpuLimit: got %q want %q", got, want)
	}
	if got, want := cloned[0].GetMemoryLimit(), "256m"; got != want {
		t.Fatalf("MemoryLimit: got %q want %q", got, want)
	}
	if got, want := cloned[0].GetDiskLimit(), "1g"; got != want {
		t.Fatalf("DiskLimit: got %q want %q", got, want)
	}
}
