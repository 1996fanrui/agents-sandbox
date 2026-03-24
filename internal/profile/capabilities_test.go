package profile

import "testing"

func TestBuiltInToolingCapabilitiesExposeRequiredIDs(t *testing.T) {
	capabilities := BuiltInToolingCapabilities()
	if len(capabilities) != 5 {
		t.Fatalf("unexpected capability count: got %d want 5", len(capabilities))
	}

	expectedIDs := []string{".agents", ".claude", ".codex", "gh-auth", "ssh-agent"}
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
