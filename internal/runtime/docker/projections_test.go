package docker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWorkspaceTreeAllowsSymlinksInsideWorkspaceRoot(t *testing.T) {
	projectRoot := t.TempDir()
	targetPath := filepath.Join(projectRoot, "docs", "guide.md")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("guide"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(filepath.Join("docs", "guide.md"), filepath.Join(projectRoot, "guide-link")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	if err := ValidateWorkspaceTree(projectRoot); err != nil {
		t.Fatalf("ValidateWorkspaceTree failed: %v", err)
	}
}

func TestValidateWorkspaceTreeRejectsSymlinksEscapingWorkspaceRoot(t *testing.T) {
	projectRoot := t.TempDir()
	externalRoot := t.TempDir()
	externalTarget := filepath.Join(externalRoot, "secret.txt")
	if err := os.WriteFile(externalTarget, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(externalTarget, filepath.Join(projectRoot, "secret-link")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	err := ValidateWorkspaceTree(projectRoot)
	if !errors.Is(err, ErrArtifactPathEscapesRoot) {
		t.Fatalf("expected ErrArtifactPathEscapesRoot, got %v", err)
	}
}
