package control

import (
	"fmt"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control/reslimits"
)

// buildLimits re-parses every resource-limit string on a CreateSpec into the
// strongly-typed reslimits.Limits consumed by the Docker runtime paths. The
// async create path and the daemon restart reconcile path both call this
// helper so the two flows stay symmetric.
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
		name := cc.GetName()
		if cc.GetCpuLimit() != "" {
			v, err := reslimits.ParseCPU(cc.GetCpuLimit())
			if err != nil {
				return nil, fmt.Errorf("companion_containers[%s].cpu_limit: %w", name, err)
			}
			if limits.CompanionCPUMillicores == nil {
				limits.CompanionCPUMillicores = make(map[string]int64)
			}
			limits.CompanionCPUMillicores[name] = v
		}
		if cc.GetMemoryLimit() != "" {
			v, err := reslimits.ParseMemoryOrDisk(cc.GetMemoryLimit(), fmt.Sprintf("companion_containers[%s].memory_limit", name))
			if err != nil {
				return nil, err
			}
			if limits.CompanionMemoryBytes == nil {
				limits.CompanionMemoryBytes = make(map[string]int64)
			}
			limits.CompanionMemoryBytes[name] = v
		}
		if cc.GetDiskLimit() != "" {
			v, err := reslimits.ParseMemoryOrDisk(cc.GetDiskLimit(), fmt.Sprintf("companion_containers[%s].disk_limit", name))
			if err != nil {
				return nil, err
			}
			if limits.CompanionDiskBytes == nil {
				limits.CompanionDiskBytes = make(map[string]int64)
			}
			limits.CompanionDiskBytes[name] = v
		}
	}
	return limits, nil
}
