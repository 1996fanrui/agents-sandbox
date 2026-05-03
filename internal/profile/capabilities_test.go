package profile

import (
	"path"
	"testing"
)

func TestBuiltInToolingCapabilitiesExposeRequiredIDs(t *testing.T) {
	capabilities := BuiltInToolingCapabilities()
	if len(capabilities) != 7 {
		t.Fatalf("unexpected capability count: got %d want 7", len(capabilities))
	}

	expectedToolIDs := []string{
		string(ToolIDApt), string(ToolIDClaude), string(ToolIDCodex),
		string(ToolIDGit), string(ToolIDNPM), string(ToolIDOpenCode), string(ToolIDUV),
	}
	for _, toolID := range expectedToolIDs {
		capability, ok := CapabilityByID(toolID)
		if !ok {
			t.Fatalf("missing tool capability %q", toolID)
		}
		if len(capability.MountIDs) == 0 {
			t.Fatalf("tool %q has no mount IDs", toolID)
		}
		for _, mountID := range capability.MountIDs {
			if _, ok := MountByID(mountID); !ok {
				t.Fatalf("tool %q references unknown mount %q", toolID, mountID)
			}
		}
	}
}

func TestCodexMountsBothDirs(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDCodex))
	if !ok {
		t.Fatal("missing tool capability codex")
	}
	if len(capability.MountIDs) != 2 {
		t.Fatalf("expected codex to have 2 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDCodex || capability.MountIDs[1] != MountIDAgents {
		t.Fatalf("unexpected codex mount IDs: %v", capability.MountIDs)
	}
}

func TestUVMountsCacheAndPythonInstallDir(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDUV))
	if !ok {
		t.Fatal("missing tool capability uv")
	}
	if len(capability.MountIDs) != 2 {
		t.Fatalf("expected uv to have 2 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDUVCache || capability.MountIDs[1] != MountIDUVPython {
		t.Fatalf("unexpected uv mount IDs: %v", capability.MountIDs)
	}
}

func TestUVPythonMountUsesHostIdenticalTarget(t *testing.T) {
	mount, ok := MountByID(MountIDUVPython)
	if !ok {
		t.Fatal("missing mount uv-python")
	}
	if mount.DefaultHostPath != "~/.local/share/uv/python" {
		t.Fatalf("unexpected DefaultHostPath: got %q want %q", mount.DefaultHostPath, "~/.local/share/uv/python")
	}
	if mount.ContainerTarget != "" {
		t.Fatalf("uv-python must not use a fixed container-home target, got %q", mount.ContainerTarget)
	}
	if mount.ContainerTargetMode != CapabilityContainerTargetHostPath {
		t.Fatalf("unexpected ContainerTargetMode: got %q want %q", mount.ContainerTargetMode, CapabilityContainerTargetHostPath)
	}
	if mount.ContainerTargetEnvKey != "UV_PYTHON_INSTALL_DIR" {
		t.Fatalf("unexpected ContainerTargetEnvKey: got %q want %q", mount.ContainerTargetEnvKey, "UV_PYTHON_INSTALL_DIR")
	}
	if mount.Mode != CapabilityModeReadWrite {
		t.Fatalf("unexpected Mode: got %q want %q", mount.Mode, CapabilityModeReadWrite)
	}
	if !mount.Optional {
		t.Fatal("expected Optional to be true")
	}
}

func TestClaudeMountsIncludePulseAudio(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDClaude))
	if !ok {
		t.Fatal("missing tool capability claude")
	}
	if len(capability.MountIDs) != 3 {
		t.Fatalf("expected claude to have 3 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDClaude || capability.MountIDs[1] != MountIDClaudeJSON || capability.MountIDs[2] != MountIDPulseAudio {
		t.Fatalf("unexpected claude mount IDs: %v", capability.MountIDs)
	}
}

func TestGitMountsAllAuthResources(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDGit))
	if !ok {
		t.Fatal("missing tool capability git")
	}
	if len(capability.MountIDs) != 3 {
		t.Fatalf("expected git to have 3 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDSSHAgent || capability.MountIDs[1] != MountIDGHAuth || capability.MountIDs[2] != MountIDSSHKnownHosts {
		t.Fatalf("unexpected git mount IDs: %v", capability.MountIDs)
	}
}

func TestOpenCodeMountsBothDirs(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDOpenCode))
	if !ok {
		t.Fatal("missing tool capability opencode")
	}
	if len(capability.MountIDs) != 2 {
		t.Fatalf("expected opencode to have 2 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDOpenCodeConfig || capability.MountIDs[1] != MountIDOpenCodeData {
		t.Fatalf("unexpected opencode mount IDs: %v", capability.MountIDs)
	}
}

func TestOpenCodeMountAttributes(t *testing.T) {
	tests := []struct {
		mountID          MountID
		wantHostPath     string
		wantTargetSuffix string
	}{
		{MountIDOpenCodeConfig, "~/.config/opencode", ".config/opencode"},
		{MountIDOpenCodeData, "~/.local/share/opencode", ".local/share/opencode"},
	}
	for _, tt := range tests {
		mount, ok := MountByID(tt.mountID)
		if !ok {
			t.Fatalf("missing mount %s", tt.mountID)
		}
		if mount.DefaultHostPath != tt.wantHostPath {
			t.Fatalf("mount %s: unexpected DefaultHostPath: got %q want %q", tt.mountID, mount.DefaultHostPath, tt.wantHostPath)
		}
		wantTarget := path.Join(ContainerUserHome, tt.wantTargetSuffix)
		if mount.ContainerTarget != wantTarget {
			t.Fatalf("mount %s: unexpected ContainerTarget: got %q want %q", tt.mountID, mount.ContainerTarget, wantTarget)
		}
		if mount.Mode != CapabilityModeReadWrite {
			t.Fatalf("mount %s: unexpected Mode: got %q want %q", tt.mountID, mount.Mode, CapabilityModeReadWrite)
		}
		if !mount.Optional {
			t.Fatalf("mount %s: expected Optional to be true", tt.mountID)
		}
	}
}

func TestSSHKnownHostsMountAttributes(t *testing.T) {
	mount, ok := MountByID(MountIDSSHKnownHosts)
	if !ok {
		t.Fatal("missing mount ssh-known-hosts")
	}
	if mount.DefaultHostPath != "~/.ssh/known_hosts" {
		t.Fatalf("unexpected DefaultHostPath: got %q want %q", mount.DefaultHostPath, "~/.ssh/known_hosts")
	}
	want := path.Join(ContainerUserHome, ".ssh/known_hosts")
	if mount.ContainerTarget != want {
		t.Fatalf("unexpected ContainerTarget: got %q want %q", mount.ContainerTarget, want)
	}
	if mount.Mode != CapabilityModeReadWrite {
		t.Fatalf("unexpected Mode: got %q want %q", mount.Mode, CapabilityModeReadWrite)
	}
	if !mount.Optional {
		t.Fatal("expected Optional to be true")
	}
}
