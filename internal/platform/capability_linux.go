//go:build linux

package platform

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// CheckNetAdminCapability verifies the current process has CAP_NET_ADMIN.
// Returns nil if present, error with instructions if missing.
func CheckNetAdminCapability() error {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return fmt.Errorf("cannot read /proc/self/status: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "CapEff:") {
			hexStr := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			capEff, err := strconv.ParseUint(hexStr, 16, 64)
			if err != nil {
				return fmt.Errorf("parse effective capabilities: %w", err)
			}
			if capEff&(1<<12) != 0 {
				return nil
			}
			return fmt.Errorf("AgentsSandbox daemon requires CAP_NET_ADMIN for sandbox network isolation.\n" +
				"Grant it with: sudo setcap cap_net_admin+ep $(which agboxd)")
		}
	}
	return fmt.Errorf("cannot determine effective capabilities from /proc/self/status")
}
