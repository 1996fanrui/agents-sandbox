//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestCheckNetAdminCapability(t *testing.T) {
	// In a standard test environment without CAP_NET_ADMIN, the function
	// should return a non-nil error with a clear message.
	err := CheckNetAdminCapability()

	// Read actual CapEff to determine expected result.
	hasCapNetAdmin := checkCapEffBit12()
	if hasCapNetAdmin {
		if err != nil {
			t.Fatalf("expected nil error with CAP_NET_ADMIN present, got: %v", err)
		}
	} else {
		if err == nil {
			t.Fatal("expected error without CAP_NET_ADMIN, got nil")
		}
		if !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
			t.Fatalf("error should mention CAP_NET_ADMIN, got: %v", err)
		}
		if !strings.Contains(err.Error(), "setcap") {
			t.Fatalf("error should include setcap instruction, got: %v", err)
		}
	}
}

// checkCapEffBit12 reads /proc/self/status to check if CAP_NET_ADMIN (bit 12) is set.
func checkCapEffBit12() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			hexStr := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			capEff, err := strconv.ParseUint(hexStr, 16, 64)
			if err != nil {
				return false
			}
			return capEff&(1<<12) != 0
		}
	}
	return false
}
