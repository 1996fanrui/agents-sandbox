package reslimits

import (
	"strings"
	"testing"
)

func TestParseHappy(t *testing.T) {
	cpuCases := []struct {
		raw  string
		want int64
	}{
		{"2", 2000},
		{"0.5", 500},
		{"0.1", 100},
	}
	for _, tc := range cpuCases {
		got, err := ParseCPU(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ParseCPU(%q) = (%d, %v), want (%d, nil)", tc.raw, got, err, tc.want)
		}
	}

	sizeCases := []struct {
		raw  string
		want int64
	}{
		{"4g", 4294967296},
		{"512m", 536870912},
		{"1k", 1024},
		{"10737418240", 10737418240},
	}
	for _, tc := range sizeCases {
		for _, field := range []string{"memory_limit", "disk_limit"} {
			got, err := ParseMemoryOrDisk(tc.raw, field)
			if err != nil || got != tc.want {
				t.Errorf("ParseMemoryOrDisk(%q, %q) = (%d, %v), want (%d, nil)", tc.raw, field, got, err, tc.want)
			}
		}
	}
}

func TestParseRejects(t *testing.T) {
	cpuBad := []string{"abc", "-1", "0", "NaN", "Inf", "0.0005"}
	for _, raw := range cpuBad {
		_, err := ParseCPU(raw)
		if err == nil {
			t.Errorf("ParseCPU(%q) expected error, got nil", raw)
			continue
		}
		if !strings.Contains(err.Error(), "cpu_limit") {
			t.Errorf("ParseCPU(%q) error %q missing field name", raw, err.Error())
		}
	}

	sizeBad := []string{"abc", "-1g", "0", "1x"}
	for _, field := range []string{"memory_limit", "disk_limit"} {
		for _, raw := range sizeBad {
			_, err := ParseMemoryOrDisk(raw, field)
			if err == nil {
				t.Errorf("ParseMemoryOrDisk(%q, %q) expected error, got nil", raw, field)
				continue
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("ParseMemoryOrDisk(%q, %q) error %q missing field name", raw, field, err.Error())
			}
		}
	}
}

func TestParseEmpty(t *testing.T) {
	if v, err := ParseCPU(""); v != 0 || err != nil {
		t.Errorf("ParseCPU(\"\") = (%d, %v), want (0, nil)", v, err)
	}
	for _, field := range []string{"memory_limit", "disk_limit"} {
		if v, err := ParseMemoryOrDisk("", field); v != 0 || err != nil {
			t.Errorf("ParseMemoryOrDisk(\"\", %q) = (%d, %v), want (0, nil)", field, v, err)
		}
	}
}

// TestLimitsHasSymmetricCompanionMaps asserts the Limits struct exposes
// parallel per-companion maps for cpu / memory / disk limits. Guards against
// regressing back to a disk-only companion model.
func TestLimitsHasSymmetricCompanionMaps(t *testing.T) {
	limits := Limits{
		CompanionCPUMillicores: map[string]int64{"db": 500},
		CompanionMemoryBytes:   map[string]int64{"db": 536870912},
		CompanionDiskBytes:     map[string]int64{"db": 5368709120},
	}
	if limits.CompanionCPUMillicores["db"] != 500 {
		t.Fatalf("CompanionCPUMillicores[db]=%d", limits.CompanionCPUMillicores["db"])
	}
	if limits.CompanionMemoryBytes["db"] != 536870912 {
		t.Fatalf("CompanionMemoryBytes[db]=%d", limits.CompanionMemoryBytes["db"])
	}
	if limits.CompanionDiskBytes["db"] != 5368709120 {
		t.Fatalf("CompanionDiskBytes[db]=%d", limits.CompanionDiskBytes["db"])
	}
}
