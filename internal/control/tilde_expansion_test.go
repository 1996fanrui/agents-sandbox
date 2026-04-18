package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
)

func TestExpandHomePath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	tests := []struct {
		input string
		want  string
	}{
		{"~", homeDir},
		{"~/foo", filepath.Join(homeDir, "foo")},
		{"~/.config/bar", filepath.Join(homeDir, ".config/bar")},
		{"/absolute/path", "/absolute/path"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := expandHomePath(tt.input)
			if err != nil {
				t.Fatalf("expandHomePath(%q): %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("expandHomePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateTildePath(t *testing.T) {
	valid := []string{"", "~", "~/foo", "/absolute/path", "relative"}
	for _, path := range valid {
		if err := validateTildePath(path); err != nil {
			t.Errorf("validateTildePath(%q) unexpected error: %v", path, err)
		}
	}
	invalid := []string{"~alice/foo", "~bob", "~user/dir/file"}
	for _, path := range invalid {
		err := validateTildePath(path)
		if err == nil {
			t.Errorf("validateTildePath(%q) expected error, got nil", path)
			continue
		}
		if !strings.Contains(err.Error(), "~username syntax is not supported") {
			t.Errorf("validateTildePath(%q) error = %q, want ~username message", path, err.Error())
		}
	}
}

func TestExpandContainerHomePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"~", profile.ContainerUserHome},
		{"~/.config", profile.ContainerUserHome + "/.config"},
		{"~/dir/sub", profile.ContainerUserHome + "/dir/sub"},
		{"/absolute", "/absolute"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandContainerHomePath(tt.input)
			if got != tt.want {
				t.Errorf("expandContainerHomePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateCreateSpecTildeExpansion(t *testing.T) {
	mountSource := filepath.Join(t.TempDir(), "mount-src")
	if err := os.MkdirAll(mountSource, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copySource := filepath.Join(t.TempDir(), "copy-src")
	if err := os.MkdirAll(copySource, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	spec := &agboxv1.CreateSpec{
		Image: "test:latest",
		Mounts: []*agboxv1.MountSpec{
			{Source: mountSource, Target: "~/.config/foo"},
		},
		Copies: []*agboxv1.CopySpec{
			{Source: copySource, Target: "~/some-dir"},
		},
	}
	if err := validateCreateSpec(spec); err != nil {
		t.Fatalf("validateCreateSpec: %v", err)
	}
	if spec.Mounts[0].Target != profile.ContainerUserHome+"/.config/foo" {
		t.Errorf("mount target = %q, want %s/.config/foo", spec.Mounts[0].Target, profile.ContainerUserHome)
	}
	if spec.Copies[0].Target != profile.ContainerUserHome+"/some-dir" {
		t.Errorf("copy target = %q, want %s/some-dir", spec.Copies[0].Target, profile.ContainerUserHome)
	}
}

func TestValidateCreateSpecTildeSourceExpansion(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	testDir := filepath.Join(homeDir, ".agents-sandbox-test-tilde")
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(testDir) })

	spec := &agboxv1.CreateSpec{
		Image: "test:latest",
		Mounts: []*agboxv1.MountSpec{
			{Source: "~/.agents-sandbox-test-tilde", Target: "/container/test"},
		},
	}
	if err := validateCreateSpec(spec); err != nil {
		t.Fatalf("validateCreateSpec: %v", err)
	}
	if spec.Mounts[0].Source != testDir {
		t.Errorf("mount source = %q, want %q", spec.Mounts[0].Source, testDir)
	}
}

func TestValidateCreateSpecTildeBareMountSource(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	spec := &agboxv1.CreateSpec{
		Image: "test:latest",
		Mounts: []*agboxv1.MountSpec{
			{Source: "~", Target: "/container/home"},
		},
	}
	if err := validateCreateSpec(spec); err != nil {
		t.Fatalf("validateCreateSpec: %v", err)
	}
	if spec.Mounts[0].Source != homeDir {
		t.Errorf("mount source = %q, want %q", spec.Mounts[0].Source, homeDir)
	}
}

func TestValidateCreateSpecTildeUsernameRejected(t *testing.T) {
	tests := []struct {
		name string
		spec *agboxv1.CreateSpec
	}{
		{
			name: "mount_source_tilde_username",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Mounts: []*agboxv1.MountSpec{{Source: "~alice/.config", Target: "/container/cfg"}},
			},
		},
		{
			name: "mount_target_tilde_username",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Mounts: []*agboxv1.MountSpec{{Source: "/tmp", Target: "~bob/data"}},
			},
		},
		{
			name: "copy_source_tilde_username",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Copies: []*agboxv1.CopySpec{{Source: "~charlie/dir", Target: "/container/out"}},
			},
		},
		{
			name: "copy_target_tilde_username",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Copies: []*agboxv1.CopySpec{{Source: "/tmp", Target: "~dave/output"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateSpec(tt.spec)
			if err == nil {
				t.Fatal("expected error for ~username syntax")
			}
			if !strings.Contains(err.Error(), "~username syntax is not supported") {
				t.Errorf("error = %q, want ~username message", err.Error())
			}
		})
	}
}

func TestValidateCreateSpecNonTildeAbsolutePathsUnchanged(t *testing.T) {
	mountSource := filepath.Join(t.TempDir(), "abs-mount")
	if err := os.MkdirAll(mountSource, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copySource := filepath.Join(t.TempDir(), "abs-copy")
	if err := os.MkdirAll(copySource, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	spec := &agboxv1.CreateSpec{
		Image: "test:latest",
		Mounts: []*agboxv1.MountSpec{
			{Source: mountSource, Target: "/container/mount"},
		},
		Copies: []*agboxv1.CopySpec{
			{Source: copySource, Target: "/container/copy"},
		},
	}
	if err := validateCreateSpec(spec); err != nil {
		t.Fatalf("validateCreateSpec: %v", err)
	}
	if spec.Mounts[0].Source != mountSource {
		t.Errorf("mount source changed: %q", spec.Mounts[0].Source)
	}
	if spec.Mounts[0].Target != "/container/mount" {
		t.Errorf("mount target changed: %q", spec.Mounts[0].Target)
	}
	if spec.Copies[0].Source != copySource {
		t.Errorf("copy source changed: %q", spec.Copies[0].Source)
	}
	if spec.Copies[0].Target != "/container/copy" {
		t.Errorf("copy target changed: %q", spec.Copies[0].Target)
	}
}

func TestValidateCreateSpecRelativePathRejected(t *testing.T) {
	// Use a real temp directory as source for target-rejection tests so that
	// source validation passes on all platforms (macOS /tmp is a symlink).
	validSource := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(validSource, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	tests := []struct {
		name string
		spec *agboxv1.CreateSpec
		want string
	}{
		{
			name: "mount_relative_source",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Mounts: []*agboxv1.MountSpec{{Source: "relative/path", Target: "/container/t"}},
			},
			want: "mount source must be absolute",
		},
		{
			name: "mount_relative_target",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Mounts: []*agboxv1.MountSpec{{Source: validSource, Target: "relative/target"}},
			},
			want: "mount target must be absolute",
		},
		{
			name: "copy_relative_source",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Copies: []*agboxv1.CopySpec{{Source: "relative/copy", Target: "/container/t"}},
			},
			want: "copy source must be absolute",
		},
		{
			name: "copy_relative_target",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Copies: []*agboxv1.CopySpec{{Source: validSource, Target: "relative/copy-target"}},
			},
			want: "copy target must be absolute",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateSpec(tt.spec)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidateCreateSpecRelativeSourceExistsRejected(t *testing.T) {
	// Even if a relative path exists on the filesystem, it must be rejected.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "exists")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	tests := []struct {
		name string
		spec *agboxv1.CreateSpec
		want string
	}{
		{
			name: "mount_relative_source_exists",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Mounts: []*agboxv1.MountSpec{{Source: "exists", Target: "/container/t"}},
			},
			want: "mount source must be absolute",
		},
		{
			name: "copy_relative_source_exists",
			spec: &agboxv1.CreateSpec{
				Image:  "test:latest",
				Copies: []*agboxv1.CopySpec{{Source: "exists", Target: "/container/t"}},
			},
			want: "copy source must be absolute",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateSpec(tt.spec)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}
