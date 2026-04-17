package control

import (
	"fmt"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control/reslimits"
)

// buildLimits re-parses every resource-limit string on a CreateSpec into the
// strongly-typed reslimits.Limits consumed by the Docker + systemd-slice
// runtime paths. The async create path and the daemon restart reconcile path
// both call this helper so the two flows stay symmetric.
func buildLimits(spec *agboxv1.CreateSpec) (*reslimits.Limits, error) {
	cpu, err := reslimits.ParseCPU(spec.GetCpuLimit())
	if err != nil {
		return nil, err
	}
	mem, err := reslimits.ParseMemoryOrDisk(spec.GetMemoryLimit(), "memory_limit")
	if err != nil {
		return nil, err
	}
	primaryDisk, err := reslimits.ParseMemoryOrDisk(spec.GetDiskLimit(), "disk_limit")
	if err != nil {
		return nil, err
	}
	limits := &reslimits.Limits{
		CPUMillicores:    cpu,
		MemoryBytes:      mem,
		PrimaryDiskBytes: primaryDisk,
	}
	for _, cc := range spec.GetCompanionContainers() {
		if cc.GetDiskLimit() == "" {
			continue
		}
		bytes, err := reslimits.ParseMemoryOrDisk(cc.GetDiskLimit(), fmt.Sprintf("companion_containers[%s].disk_limit", cc.GetName()))
		if err != nil {
			return nil, err
		}
		if limits.CompanionDiskBytes == nil {
			limits.CompanionDiskBytes = make(map[string]int64)
		}
		limits.CompanionDiskBytes[cc.GetName()] = bytes
	}
	return limits, nil
}
