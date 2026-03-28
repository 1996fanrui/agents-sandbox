// Package version provides the build version for all agents-sandbox binaries.
// The Version variable is overridden at build time via:
//
//	go build -ldflags "-X github.com/1996fanrui/agents-sandbox/internal/version.Version=0.1.1"
package version

var Version = "dev"
