package profile

import (
	"path"
	"slices"
)

type CapabilityMode string

const (
	CapabilityModeReadOnly  CapabilityMode = "read_only"
	CapabilityModeReadWrite CapabilityMode = "read_write"
	CapabilityModeSocket    CapabilityMode = "socket"
)

// CapabilityContainerTargetMode controls how a capability mount chooses the
// Docker mount target inside the container.
type CapabilityContainerTargetMode string

const (
	// CapabilityContainerTargetDeclared uses CapabilityMount.ContainerTarget as
	// the container path. This is the normal host-path -> container-home-path mode.
	CapabilityContainerTargetDeclared CapabilityContainerTargetMode = "declared_container_path"
	// CapabilityContainerTargetHostPath uses the resolved host source path as
	// the container path too. Host and container see the same absolute path.
	CapabilityContainerTargetHostPath CapabilityContainerTargetMode = "host_path"
)

// ContainerUserHome is the home directory of the default user inside
// AgentsSandbox runtime containers. This constant is the single source
// of truth; all container-side home-relative paths must derive from it.
const ContainerUserHome = "/home/agbox"

// MountID is the canonical identifier for a capability mount.
type MountID string

const (
	MountIDClaude         MountID = ".claude"
	MountIDClaudeJSON     MountID = ".claude.json"
	MountIDCodex          MountID = ".codex"
	MountIDAgents         MountID = ".agents"
	MountIDGHAuth         MountID = "gh-auth"
	MountIDSSHAgent       MountID = "ssh-agent"
	MountIDSSHKnownHosts  MountID = "ssh-known-hosts"
	MountIDUVCache        MountID = "uv-cache"
	MountIDUVPython       MountID = "uv-python"
	MountIDNPM            MountID = "npm"
	MountIDApt            MountID = "apt"
	MountIDPulseAudio     MountID = "pulse-audio"
	MountIDOpenCodeConfig MountID = "opencode-config"
	MountIDOpenCodeData   MountID = "opencode-data"
)

// ToolID is the canonical identifier for a tooling capability.
type ToolID string

const (
	ToolIDClaude   ToolID = "claude"
	ToolIDCodex    ToolID = "codex"
	ToolIDGit      ToolID = "git"
	ToolIDUV       ToolID = "uv"
	ToolIDNPM      ToolID = "npm"
	ToolIDApt      ToolID = "apt"
	ToolIDOpenCode ToolID = "opencode"
)

// MacOSKeychainCredential declares that a credential file may be absent from
// the host mount directory because macOS stores it in Keychain instead.
//
// Contract: if RelPath does not exist under the mount's DefaultHostPath, the
// daemon reads the credential from Keychain and writes it to that path before
// bind-mounting. On non-macOS platforms this is a no-op (the file already exists).
type MacOSKeychainCredential struct {
	// ServiceName is the Keychain service name used to read the credential
	// via `security find-generic-password -s <service> -w`.
	ServiceName string
	// RelPath is the credential file path relative to the mount's DefaultHostPath.
	RelPath string
}

// CapabilityMount is a named host-to-container mount unit.
// Multiple tools may reference the same mount; the daemon deduplicates by ID.
type CapabilityMount struct {
	ID                  MountID
	DefaultHostPath     string
	ContainerTarget     string
	ContainerTargetMode CapabilityContainerTargetMode
	Mode                CapabilityMode
	// Optional marks this mount as individually skippable when the host resource
	// is unavailable, even if the parent tool is required. This allows a required
	// tool (e.g. claude) to include mounts that may not exist on all hosts.
	Optional bool
	// ContainerTargetEnvKey injects an environment variable whose value is the
	// resolved container target when this mount is materialized.
	ContainerTargetEnvKey string
	// MacOSKeychain, when non-nil, triggers credential projection from macOS
	// Keychain before bind-mounting. See MacOSKeychainCredential for the full contract.
	MacOSKeychain *MacOSKeychainCredential
}

// ToolingCapability is a user-facing tool name that maps to one or more mount IDs.
// Users request tools by name; the daemon resolves and deduplicates the underlying mounts.
// Each mount's Optional field controls whether it is silently skipped when unavailable.
type ToolingCapability struct {
	MountIDs []MountID
}

var capabilityMounts = buildMountIndex([]CapabilityMount{
	{
		ID:              MountIDClaude,
		DefaultHostPath: "~/.claude",
		ContainerTarget: path.Join(ContainerUserHome, ".claude"),
		Mode:            CapabilityModeReadWrite,
		MacOSKeychain: &MacOSKeychainCredential{
			ServiceName: "Claude Code-credentials",
			RelPath:     ".credentials.json",
		},
	},
	{
		ID:              MountIDClaudeJSON,
		DefaultHostPath: "~/.claude.json",
		ContainerTarget: path.Join(ContainerUserHome, ".claude.json"),
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDCodex,
		DefaultHostPath: "~/.codex",
		ContainerTarget: path.Join(ContainerUserHome, ".codex"),
		Mode:            CapabilityModeReadWrite,
	},
	// .agents is the shared state directory consumed by Codex and potentially other tools.
	{
		ID:              MountIDAgents,
		DefaultHostPath: "~/.agents",
		ContainerTarget: path.Join(ContainerUserHome, ".agents"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	{
		ID:              MountIDGHAuth,
		DefaultHostPath: "~/.config/gh",
		ContainerTarget: path.Join(ContainerUserHome, ".config/gh"),
		Mode:            CapabilityModeReadOnly,
		Optional:        true,
	},
	{
		ID:              MountIDSSHAgent,
		DefaultHostPath: "SSH_AUTH_SOCK",
		ContainerTarget: "/ssh-agent",
		Mode:            CapabilityModeSocket,
		Optional:        true,
	},
	{
		ID:              MountIDSSHKnownHosts,
		DefaultHostPath: "~/.ssh/known_hosts",
		ContainerTarget: path.Join(ContainerUserHome, ".ssh/known_hosts"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	// uv-cache holds downloaded packages; uv-python holds uv-managed Python installations.
	{
		ID:              MountIDUVCache,
		DefaultHostPath: "~/.cache/uv",
		ContainerTarget: path.Join(ContainerUserHome, ".cache/uv"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	{
		ID:                    MountIDUVPython,
		DefaultHostPath:       "~/.local/share/uv/python",
		ContainerTargetMode:   CapabilityContainerTargetHostPath,
		Mode:                  CapabilityModeReadWrite,
		Optional:              true,
		ContainerTargetEnvKey: "UV_PYTHON_INSTALL_DIR",
	},
	{
		ID:              MountIDNPM,
		DefaultHostPath: "~/.npm",
		ContainerTarget: path.Join(ContainerUserHome, ".npm"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	{
		ID:              MountIDApt,
		DefaultHostPath: "~/.cache/agents-sandbox-apt",
		ContainerTarget: "/var/cache/apt/archives",
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	{
		ID:              MountIDPulseAudio,
		DefaultHostPath: "PULSE_AUDIO_SOCK",
		ContainerTarget: "/pulse-audio",
		Mode:            CapabilityModeSocket,
		Optional:        true,
	},
	{
		ID:              MountIDOpenCodeConfig,
		DefaultHostPath: "~/.config/opencode",
		ContainerTarget: path.Join(ContainerUserHome, ".config/opencode"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
	{
		ID:              MountIDOpenCodeData,
		DefaultHostPath: "~/.local/share/opencode",
		ContainerTarget: path.Join(ContainerUserHome, ".local/share/opencode"),
		Mode:            CapabilityModeReadWrite,
		Optional:        true,
	},
})

func buildMountIndex(mounts []CapabilityMount) map[MountID]CapabilityMount {
	index := make(map[MountID]CapabilityMount, len(mounts))
	for _, m := range mounts {
		index[m.ID] = m
	}
	return index
}

var builtInToolingCapabilities = map[ToolID]ToolingCapability{
	ToolIDClaude:   {MountIDs: []MountID{MountIDClaude, MountIDClaudeJSON, MountIDPulseAudio}},
	ToolIDCodex:    {MountIDs: []MountID{MountIDCodex, MountIDAgents}},
	ToolIDGit:      {MountIDs: []MountID{MountIDSSHAgent, MountIDGHAuth, MountIDSSHKnownHosts}},
	ToolIDUV:       {MountIDs: []MountID{MountIDUVCache, MountIDUVPython}},
	ToolIDNPM:      {MountIDs: []MountID{MountIDNPM}},
	ToolIDApt:      {MountIDs: []MountID{MountIDApt}},
	ToolIDOpenCode: {MountIDs: []MountID{MountIDOpenCodeConfig, MountIDOpenCodeData}},
}

func BuiltInToolingCapabilities() []ToolingCapability {
	capabilities := make([]ToolingCapability, 0, len(builtInToolingCapabilities))
	keys := make([]ToolID, 0, len(builtInToolingCapabilities))
	for key := range builtInToolingCapabilities {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		capabilities = append(capabilities, builtInToolingCapabilities[key])
	}
	return capabilities
}

// CapabilityByID returns the ToolingCapability for the given tool ID string.
// The string is validated against the known ToolID enum values.
func CapabilityByID(toolID string) (ToolingCapability, bool) {
	capability, exists := builtInToolingCapabilities[ToolID(toolID)]
	return capability, exists
}

// MountByID returns the CapabilityMount for the given mount ID.
func MountByID(mountID MountID) (CapabilityMount, bool) {
	mount, exists := capabilityMounts[mountID]
	return mount, exists
}
