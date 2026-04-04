package control

import (
	"net"
	"os"
	"path/filepath"
	"testing"
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
