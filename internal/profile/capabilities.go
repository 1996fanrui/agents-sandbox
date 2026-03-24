package profile

import (
	"sort"

	"github.com/1996fanrui/agents-sandbox/internal/control"
)

type ToolingCapability struct {
	ID              string
	DefaultHostPath string
	ContainerTarget string
	Mode            control.CapabilityMode
}

var builtInToolingCapabilities = map[string]ToolingCapability{
	".claude": {
		ID:              ".claude",
		DefaultHostPath: "~/.claude",
		ContainerTarget: "/home/sandbox/.claude",
		Mode:            control.CapabilityModeReadWrite,
	},
	".codex": {
		ID:              ".codex",
		DefaultHostPath: "~/.codex",
		ContainerTarget: "/home/sandbox/.codex",
		Mode:            control.CapabilityModeReadWrite,
	},
	".agents": {
		ID:              ".agents",
		DefaultHostPath: "~/.agents",
		ContainerTarget: "/home/sandbox/.agents",
		Mode:            control.CapabilityModeReadWrite,
	},
	"gh-auth": {
		ID:              "gh-auth",
		DefaultHostPath: "~/.config/gh",
		ContainerTarget: "/home/sandbox/.config/gh",
		Mode:            control.CapabilityModeReadOnly,
	},
	"ssh-agent": {
		ID:              "ssh-agent",
		DefaultHostPath: "SSH_AUTH_SOCK",
		ContainerTarget: "/ssh-agent",
		Mode:            control.CapabilityModeSocket,
	},
}

func BuiltInToolingCapabilities() []ToolingCapability {
	capabilities := make([]ToolingCapability, 0, len(builtInToolingCapabilities))
	keys := make([]string, 0, len(builtInToolingCapabilities))
	for key := range builtInToolingCapabilities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		capabilities = append(capabilities, builtInToolingCapabilities[key])
	}
	return capabilities
}

func CapabilityByID(capabilityID string) (ToolingCapability, bool) {
	capability, exists := builtInToolingCapabilities[capabilityID]
	return capability, exists
}
