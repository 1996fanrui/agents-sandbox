package profile

import "sort"

type CapabilityMode string

const (
	CapabilityModeReadOnly  CapabilityMode = "read_only"
	CapabilityModeReadWrite CapabilityMode = "read_write"
	CapabilityModeSocket    CapabilityMode = "socket"
)

type ToolingCapability struct {
	ID              string
	DefaultHostPath string
	ContainerTarget string
	Mode            CapabilityMode
}

var builtInToolingCapabilities = map[string]ToolingCapability{
	".claude": {
		ID:              ".claude",
		DefaultHostPath: "~/.claude",
		ContainerTarget: "/home/agbox/.claude",
		Mode:            CapabilityModeReadWrite,
	},
	".codex": {
		ID:              ".codex",
		DefaultHostPath: "~/.codex",
		ContainerTarget: "/home/agbox/.codex",
		Mode:            CapabilityModeReadWrite,
	},
	".agents": {
		ID:              ".agents",
		DefaultHostPath: "~/.agents",
		ContainerTarget: "/home/agbox/.agents",
		Mode:            CapabilityModeReadWrite,
	},
	"gh-auth": {
		ID:              "gh-auth",
		DefaultHostPath: "~/.config/gh",
		ContainerTarget: "/home/agbox/.config/gh",
		Mode:            CapabilityModeReadOnly,
	},
	"ssh-agent": {
		ID:              "ssh-agent",
		DefaultHostPath: "SSH_AUTH_SOCK",
		ContainerTarget: "/ssh-agent",
		Mode:            CapabilityModeSocket,
	},
	"uv": {
		ID:              "uv",
		DefaultHostPath: "~/.cache/uv",
		ContainerTarget: "/home/agbox/.cache/uv",
		Mode:            CapabilityModeReadWrite,
	},
	"uv-python": {
		ID:              "uv-python",
		DefaultHostPath: "~/.local/share/uv",
		ContainerTarget: "/home/agbox/.local/share/uv",
		Mode:            CapabilityModeReadWrite,
	},
	"npm": {
		ID:              "npm",
		DefaultHostPath: "~/.npm",
		ContainerTarget: "/home/agbox/.npm",
		Mode:            CapabilityModeReadWrite,
	},
	"apt": {
		ID:              "apt",
		DefaultHostPath: "~/.cache/agents-sandbox-apt",
		ContainerTarget: "/var/cache/apt/archives",
		Mode:            CapabilityModeReadWrite,
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
