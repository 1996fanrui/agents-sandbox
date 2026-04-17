package control

import (
	"context"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"google.golang.org/grpc/codes"
)

// baseCapsSystemdV2 returns the capability snapshot that reflects a healthy
// Linux host: cgroup v2 + systemd cgroup driver. Used by tests that want to
// isolate resource-limit string validation from cgroup-gating.
func baseCapsSystemdV2() hostCapabilities {
	return hostCapabilities{CgroupDriver: "systemd", CgroupV2Available: true}
}

// AT-1KJ2: Invalid resource limit strings on CreateSpec must surface as
// InvalidArgument with the offending field name in the message.
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
		TransitionDelay:  5 * time.Millisecond,
		PollInterval:     2 * time.Millisecond,
		HostCapabilities: baseCapsSystemdV2(),
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

// AT-WIWG: cpu_limit / memory_limit must be rejected as FailedPrecondition
// when the host lacks systemd + cgroup v2.
func TestCreateSandboxRejectsCPUWithoutSystemdCgroup(t *testing.T) {
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay:  5 * time.Millisecond,
		PollInterval:     2 * time.Millisecond,
		HostCapabilities: hostCapabilities{CgroupDriver: "cgroupfs", CgroupV2Available: true},
	})
	_, err := client.CreateSandbox(context.Background(), &agboxv1.CreateSandboxRequest{
		SandboxId: "res-nocgroup",
		CreateSpec: &agboxv1.CreateSpec{
			Image:    "img:test",
			CpuLimit: "2",
		},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := statusCode(err); got != codes.FailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %s: %v", got, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "cgroup v2") || !strings.Contains(msg, "systemd") {
		t.Fatalf("expected message to mention 'cgroup v2' and 'systemd', got %v", err)
	}
}
