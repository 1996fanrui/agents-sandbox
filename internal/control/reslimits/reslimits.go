// Package reslimits parses sandbox resource limit strings into typed values.
package reslimits

import (
	"fmt"
	"math"
	"strconv"

	units "github.com/docker/go-units"
)

type Limits struct {
	CPUMillicores      int64
	MemoryBytes        int64
	PrimaryDiskBytes   int64
	CompanionDiskBytes map[string]int64
}

func ParseCPU(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("cpu_limit %q: %w", raw, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("cpu_limit %q: must be a finite number", raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("cpu_limit %q: must be > 0", raw)
	}
	millicores := int64(math.Floor(v * 1000))
	if millicores <= 0 {
		return 0, fmt.Errorf("cpu_limit %q: must resolve to at least 1 millicore (0.001)", raw)
	}
	return millicores, nil
}

func ParseMemoryOrDisk(raw, fieldName string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := units.RAMInBytes(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", fieldName, raw, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s %q: must be > 0", fieldName, raw)
	}
	return v, nil
}
