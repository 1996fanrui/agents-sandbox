package control

import (
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

// TestValidateCommandRejectsEmptyStringEntry covers AT-Y0OM at the daemon
// validation layer for both primary and companion commands.
func TestValidateCommandRejectsEmptyStringEntry(t *testing.T) {
	t.Run("primary", func(t *testing.T) {
		spec := &agboxv1.CreateSpec{
			Image:   "img:test",
			Command: []string{"foo", "", "bar"},
		}
		err := validateCreateSpec(spec)
		if err == nil {
			t.Fatal("expected validation error for empty string entry")
		}
		if !strings.Contains(err.Error(), "command[1]") {
			t.Fatalf("expected 'command[1]' in error, got %v", err)
		}
	})
	t.Run("companion", func(t *testing.T) {
		spec := &agboxv1.CreateSpec{
			Image: "img:test",
			CompanionContainers: []*agboxv1.CompanionContainerSpec{
				{
					Name:    "cache",
					Image:   "redis:7",
					Command: []string{"redis-server", ""},
				},
			},
		}
		err := validateCreateSpec(spec)
		if err == nil {
			t.Fatal("expected validation error for companion empty-string entry")
		}
		msg := err.Error()
		if !strings.Contains(msg, "cache") {
			t.Fatalf("expected companion name 'cache' in error, got %v", err)
		}
		if !strings.Contains(msg, "command[1]") {
			t.Fatalf("expected 'command[1]' in error, got %v", err)
		}
	})
}
