package profile

import "sort"

type CapabilityMode string

const (
	CapabilityModeReadOnly  CapabilityMode = "read_only"
	CapabilityModeReadWrite CapabilityMode = "read_write"
	CapabilityModeSocket    CapabilityMode = "socket"
)

// MountID is the canonical identifier for a capability mount.
type MountID string

const (
	MountIDClaude     MountID = ".claude"
	MountIDClaudeJSON MountID = ".claude.json"
	MountIDCodex      MountID = ".codex"
	MountIDAgents     MountID = ".agents"
	MountIDGHAuth     MountID = "gh-auth"
	MountIDSSHAgent   MountID = "ssh-agent"
	MountIDUVCache    MountID = "uv-cache"
	MountIDUVData     MountID = "uv-data"
	MountIDNPM        MountID = "npm"
	MountIDApt        MountID = "apt"
)

// ToolID is the canonical identifier for a tooling capability.
type ToolID string

const (
	ToolIDClaude ToolID = "claude"
	ToolIDCodex  ToolID = "codex"
	ToolIDGit    ToolID = "git"
	ToolIDUV     ToolID = "uv"
	ToolIDNPM    ToolID = "npm"
	ToolIDApt    ToolID = "apt"
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
	ID              MountID
	DefaultHostPath string
	ContainerTarget string
	Mode            CapabilityMode
	// MacOSKeychain, when non-nil, triggers credential projection from macOS
	// Keychain before bind-mounting. See MacOSKeychainCredential for the full contract.
	MacOSKeychain *MacOSKeychainCredential
}

// ToolingCapability is a user-facing tool name that maps to one or more mount IDs.
// Users request tools by name; the daemon resolves and deduplicates the underlying mounts.
// Optional tools are silently skipped when their host paths do not exist.
type ToolingCapability struct {
	MountIDs []MountID
	Optional bool
}

var capabilityMounts = buildMountIndex([]CapabilityMount{
	{
		ID:              MountIDClaude,
		DefaultHostPath: "~/.claude",
		ContainerTarget: "/home/agbox/.claude",
		Mode:            CapabilityModeReadWrite,
		MacOSKeychain: &MacOSKeychainCredential{
			ServiceName: "Claude Code-credentials",
			RelPath:     ".credentials.json",
		},
	},
	{
		ID:              MountIDClaudeJSON,
		DefaultHostPath: "~/.claude.json",
		ContainerTarget: "/home/agbox/.claude.json",
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDCodex,
		DefaultHostPath: "~/.codex",
		ContainerTarget: "/home/agbox/.codex",
		Mode:            CapabilityModeReadWrite,
	},
	// .agents is the shared state directory consumed by Codex and potentially other tools.
	{
		ID:              MountIDAgents,
		DefaultHostPath: "~/.agents",
		ContainerTarget: "/home/agbox/.agents",
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDGHAuth,
		DefaultHostPath: "~/.config/gh",
		ContainerTarget: "/home/agbox/.config/gh",
		Mode:            CapabilityModeReadOnly,
	},
	{
		ID:              MountIDSSHAgent,
		DefaultHostPath: "SSH_AUTH_SOCK",
		ContainerTarget: "/ssh-agent",
		Mode:            CapabilityModeSocket,
	},
	// uv-cache holds downloaded packages; uv-data holds uv-managed Python interpreters and global tools.
	{
		ID:              MountIDUVCache,
		DefaultHostPath: "~/.cache/uv",
		ContainerTarget: "/home/agbox/.cache/uv",
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDUVData,
		DefaultHostPath: "~/.local/share/uv",
		ContainerTarget: "/home/agbox/.local/share/uv",
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDNPM,
		DefaultHostPath: "~/.npm",
		ContainerTarget: "/home/agbox/.npm",
		Mode:            CapabilityModeReadWrite,
	},
	{
		ID:              MountIDApt,
		DefaultHostPath: "~/.cache/agents-sandbox-apt",
		ContainerTarget: "/var/cache/apt/archives",
		Mode:            CapabilityModeReadWrite,
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
	ToolIDClaude: {MountIDs: []MountID{MountIDClaude, MountIDClaudeJSON}},
	// codex requires its own config dir and the shared agents state directory.
	ToolIDCodex: {MountIDs: []MountID{MountIDCodex, MountIDAgents}},
	// git mounts are optional: SSH agent may not be running and gh CLI may not be configured.
	// Each mount is independently skipped if its host path is unavailable.
	ToolIDGit: {MountIDs: []MountID{MountIDSSHAgent, MountIDGHAuth}, Optional: true},
	// uv, npm, apt are cache/acceleration mounts; the host may not have them installed.
	ToolIDUV:  {MountIDs: []MountID{MountIDUVCache, MountIDUVData}, Optional: true},
	ToolIDNPM: {MountIDs: []MountID{MountIDNPM}, Optional: true},
	ToolIDApt: {MountIDs: []MountID{MountIDApt}, Optional: true},
}

func BuiltInToolingCapabilities() []ToolingCapability {
	capabilities := make([]ToolingCapability, 0, len(builtInToolingCapabilities))
	keys := make([]ToolID, 0, len(builtInToolingCapabilities))
	for key := range builtInToolingCapabilities {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
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
