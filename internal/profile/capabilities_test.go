package profile

import "testing"

func TestBuiltInToolingCapabilitiesExposeRequiredIDs(t *testing.T) {
	capabilities := BuiltInToolingCapabilities()
	if len(capabilities) != 6 {
		t.Fatalf("unexpected capability count: got %d want 6", len(capabilities))
	}

	expectedToolIDs := []string{
		string(ToolIDApt), string(ToolIDClaude), string(ToolIDCodex),
		string(ToolIDGit), string(ToolIDNPM), string(ToolIDUV),
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

func TestUVMountsBothDirs(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDUV))
	if !ok {
		t.Fatal("missing tool capability uv")
	}
	if len(capability.MountIDs) != 2 {
		t.Fatalf("expected uv to have 2 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDUVCache || capability.MountIDs[1] != MountIDUVData {
		t.Fatalf("unexpected uv mount IDs: %v", capability.MountIDs)
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

func TestGitMountsBothAuthResources(t *testing.T) {
	capability, ok := CapabilityByID(string(ToolIDGit))
	if !ok {
		t.Fatal("missing tool capability git")
	}
	if len(capability.MountIDs) != 2 {
		t.Fatalf("expected git to have 2 mount IDs, got %d", len(capability.MountIDs))
	}
	if capability.MountIDs[0] != MountIDSSHAgent || capability.MountIDs[1] != MountIDGHAuth {
		t.Fatalf("unexpected git mount IDs: %v", capability.MountIDs)
	}
}
