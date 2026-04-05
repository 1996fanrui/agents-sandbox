//go:build !linux

package control

import "log/slog"

// newNftablesConnector returns a no-op connector on non-Linux platforms
// where nftables is not available.
func newNftablesConnector(_ *slog.Logger) nftablesConnector {
	return noopNftablesConnector{}
}
