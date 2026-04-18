package control

import (
	"context"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
)

// AT-3BDF: Invalid resource limit strings on CreateSpec (primary and every
// companion's cpu/memory/disk) must surface as InvalidArgument with the
// offending field path in the message.
func TestCreateSandboxResourceLimitValidation(t *testing.T) {
	cases := []struct {
		name       string
		spec       *agboxv1.CreateSpec
		wantSubstr string
	}{
		{
			name:       "cpu_limit_negative",
			spec:       &agboxv1.CreateSpec{Image: "img:test", CpuLimit: "-1"},
			wantSubstr: "cpu_limit",
		},
		{
			name:       "memory_limit_garbage",
			spec:       &agboxv1.CreateSpec{Image: "img:test", MemoryLimit: "abc"},
			wantSubstr: "memory_limit",
		},
		{
			name:       "disk_limit_zero",
			spec:       &agboxv1.CreateSpec{Image: "img:test", DiskLimit: "0"},
			wantSubstr: "disk_limit",
		},
		{
			name: "companion_cpu_limit_bad",
			spec: &agboxv1.CreateSpec{
				Image: "img:test",
				CompanionContainers: []*agboxv1.CompanionContainerSpec{
					{Name: "cache", Image: "redis:7", CpuLimit: "abc"},
				},
			},
			wantSubstr: "companion_containers[cache].cpu_limit",
		},
		{
			name: "companion_memory_limit_bad",
			spec: &agboxv1.CreateSpec{
				Image: "img:test",
				CompanionContainers: []*agboxv1.CompanionContainerSpec{
					{Name: "cache", Image: "redis:7", MemoryLimit: "abc"},
				},
			},
			wantSubstr: "companion_containers[cache].memory_limit",
		},
		{
			name: "companion_disk_limit_bad",
			spec: &agboxv1.CreateSpec{
				Image: "img:test",
				CompanionContainers: []*agboxv1.CompanionContainerSpec{
					{Name: "cache", Image: "redis:7", DiskLimit: "bad"},
				},
			},
			wantSubstr: "companion_containers[cache].disk_limit",
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
				SandboxId:  "res-invalid-" + tc.name,
				CreateSpec: tc.spec,
			})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if got := statusCode(err); got != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %s: %v", got, err)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("expected error to contain %q, got %v", tc.wantSubstr, err)
			}
		})
	}
}
