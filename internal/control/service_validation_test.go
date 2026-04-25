package control

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestValidateGenericSourcePath_AcceptsUnixSocket(t *testing.T) {
	// Use /tmp for the socket to avoid exceeding the 104-byte Unix socket
	// path limit on macOS, where t.TempDir() paths are much longer.
	sockPath := filepath.Join("/tmp", "agbox-test-validate.sock")
	os.Remove(sockPath) // clean up from prior runs
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create unix socket: %v", err)
	}
	defer ln.Close()

	if err := validateGenericSourcePath("mount", sockPath); err != nil {
		t.Errorf("expected unix socket to be accepted, got error: %v", err)
	}
}

func TestValidateGenericSourcePath_AcceptsFileAndDirectory(t *testing.T) {
	dir := t.TempDir()

	// Directory should be accepted.
	if err := validateGenericSourcePath("mount", dir); err != nil {
		t.Errorf("expected directory to be accepted, got error: %v", err)
	}

	// Regular file should be accepted.
	filePath := filepath.Join(dir, "regular.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	if err := validateGenericSourcePath("mount", filePath); err != nil {
		t.Errorf("expected regular file to be accepted, got error: %v", err)
	}
}

func TestValidateGenericSourcePath_RejectsRelativePath(t *testing.T) {
	err := validateGenericSourcePath("mount", "relative/path")
	if err == nil {
		t.Error("expected error for relative path, got nil")
	}
}

func TestValidateGenericSourcePath_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	err := validateGenericSourcePath("mount", link)
	if err == nil {
		t.Error("expected error for symlink, got nil")
	}
}

// validationConflictSource creates two real paths inside t.TempDir so each
// CreateSpec entry passes validateGenericSourcePath. Returns paths suitable
// for use as MountSpec / CopySpec source fields.
func validationConflictSource(t *testing.T, basename string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, basename)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

// TestValidateCreateSpec_MountTargetConflict covers AT-7JER: two mounts with
// the same target are rejected and the error mentions the conflicting target.
func TestValidateCreateSpec_MountTargetConflict(t *testing.T) {
	src1 := validationConflictSource(t, "src1")
	src2 := validationConflictSource(t, "src2")
	spec := &agboxv1.CreateSpec{
		Mounts: []*agboxv1.MountSpec{
			{Source: src1, Target: "/data"},
			{Source: src2, Target: "/data"},
		},
	}
	err := validateCreateSpec(spec)
	if err == nil {
		t.Fatal("expected mount target conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "/data") {
		t.Fatalf("error must include conflicting target /data, got %q", err.Error())
	}
}

// TestValidateCreateSpec_MountCopyTargetConflict covers AT-7JER: a mount and
// a copy sharing a target are rejected (mounts and copies share the same
// target namespace).
func TestValidateCreateSpec_MountCopyTargetConflict(t *testing.T) {
	mountSrc := validationConflictSource(t, "msrc")
	copySrc := validationConflictSource(t, "csrc")
	spec := &agboxv1.CreateSpec{
		Mounts: []*agboxv1.MountSpec{
			{Source: mountSrc, Target: "/x"},
		},
		Copies: []*agboxv1.CopySpec{
			{Source: copySrc, Target: "/x"},
		},
	}
	err := validateCreateSpec(spec)
	if err == nil {
		t.Fatal("expected mount/copy target conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "/x") {
		t.Fatalf("error must include conflicting target /x, got %q", err.Error())
	}
}

// TestValidateCreateSpec_CopyTargetConflict covers AT-7JER: two copies with
// the same target are rejected.
func TestValidateCreateSpec_CopyTargetConflict(t *testing.T) {
	src1 := validationConflictSource(t, "csrc1")
	src2 := validationConflictSource(t, "csrc2")
	spec := &agboxv1.CreateSpec{
		Copies: []*agboxv1.CopySpec{
			{Source: src1, Target: "/x"},
			{Source: src2, Target: "/x"},
		},
	}
	err := validateCreateSpec(spec)
	if err == nil {
		t.Fatal("expected copy target conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "/x") {
		t.Fatalf("error must include conflicting target /x, got %q", err.Error())
	}
}

// TestValidateCreateSpec_PortHostProtocolConflict covers AT-7JER: two port
// mappings sharing (host_port, protocol) are rejected and the error mentions
// the conflicting host_port literal.
func TestValidateCreateSpec_PortHostProtocolConflict(t *testing.T) {
	spec := &agboxv1.CreateSpec{
		Ports: []*agboxv1.PortMapping{
			{ContainerPort: 8080, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
			{ContainerPort: 9090, HostPort: 8080, Protocol: agboxv1.PortProtocol_PORT_PROTOCOL_TCP},
		},
	}
	err := validateCreateSpec(spec)
	if err == nil {
		t.Fatal("expected duplicate host_port/protocol error, got nil")
	}
	if !strings.Contains(err.Error(), "8080") {
		t.Fatalf("error must include conflicting host_port 8080, got %q", err.Error())
	}
}

// TestValidateCreateSpec_CompanionNameConflict covers AT-7JER: two companion
// containers sharing a name are rejected and the error mentions the
// conflicting name literal.
func TestValidateCreateSpec_CompanionNameConflict(t *testing.T) {
	spec := &agboxv1.CreateSpec{
		CompanionContainers: []*agboxv1.CompanionContainerSpec{
			{Name: "redis", Image: "redis:7"},
			{Name: "redis", Image: "redis:7-alpine"},
		},
	}
	err := validateCreateSpec(spec)
	if err == nil {
		t.Fatal("expected duplicate companion name error, got nil")
	}
	if !strings.Contains(err.Error(), "redis") {
		t.Fatalf("error must include conflicting companion name redis, got %q", err.Error())
	}
}
