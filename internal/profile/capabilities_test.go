package profile

import "testing"

func TestBuiltInToolingCapabilitiesExposeRequiredIDs(t *testing.T) {
	capabilities := BuiltInToolingCapabilities()
	if len(capabilities) != 8 {
		t.Fatalf("unexpected capability count: got %d want 8", len(capabilities))
	}

	expectedIDs := []string{".agents", ".claude", ".codex", "apt", "gh-auth", "npm", "ssh-agent", "uv"}
	for _, capabilityID := range expectedIDs {
		capability, ok := CapabilityByID(capabilityID)
		if !ok {
			t.Fatalf("missing capability %q", capabilityID)
		}
		if capability.ID != capabilityID {
			t.Fatalf("unexpected capability id: got %q want %q", capability.ID, capabilityID)
		}
	}
}
