//go:build !linux

package platform

// CheckNetAdminCapability is a no-op on non-Linux platforms.
// macOS uses --add-host for isolation which requires no special privileges.
func CheckNetAdminCapability() error {
	return nil
}
