package control

import (
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateSandboxGPUValidation(t *testing.T) {
	validValues := []string{"", "all"}
	for _, value := range validValues {
		name := value
		if name == "" {
			name = "empty"
		}
		t.Run("valid_"+name, func(t *testing.T) {
			spec := &agboxv1.CreateSpec{Image: "img:test", Gpus: value}
			if err := validateCreateSpec(spec); err != nil {
				t.Fatalf("validateCreateSpec(%q) failed: %v", value, err)
			}
		})
	}

	invalidValues := []string{
		"1",
		"0,1",
		"device=0",
		"GPU-12345678-1234-1234-1234-123456789abc",
		"vram=8g",
		"compute=50%",
	}
	for _, value := range invalidValues {
		t.Run("invalid_"+strings.NewReplacer("=", "_", ",", "_", "%", "pct").Replace(value), func(t *testing.T) {
			err := validateCreateSpec(&agboxv1.CreateSpec{Image: "img:test", Gpus: value})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument for %q, got %v", value, err)
			}
			if !strings.Contains(err.Error(), "gpus") || !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "all") {
				t.Fatalf("GPU validation error should name gpus and supported values, got %v", err)
			}
		})
	}

	t.Run("reject_reserved_supplemental_groups_env_without_gpus", func(t *testing.T) {
		err := validateCreateSpec(&agboxv1.CreateSpec{
			Image: "img:test",
			Envs: map[string]string{
				supplementalGroupsEnv: "999",
			},
		})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument for reserved env, got %v", err)
		}
		if !strings.Contains(err.Error(), "envs.") || !strings.Contains(err.Error(), supplementalGroupsEnv) || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("reserved env validation error should name the field and env key, got %v", err)
		}
	})
}
