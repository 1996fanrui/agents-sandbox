package control

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
)

func TestExecStatusCarriesExitCode(t *testing.T) {
	runtime := &capturingRuntimeBackend{
		execResult: runtimeExecResult{
			ExitCode: 0,
		},
	}
	client := newBufconnClient(t, ServiceConfig{
		TransitionDelay: 5 * time.Millisecond,
		PollInterval:    2 * time.Millisecond,
		runtimeBackend:  runtime,
	})

	createResp, err := client.CreateSandbox(context.Background(), createSandboxRequest("session-exec-output", "ghcr.io/agents-sandbox/coding-runtime:test"))
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	waitForSandboxState(t, client, createResp.GetSandbox().GetSandboxId(), agboxv1.SandboxState_SANDBOX_STATE_READY)

	execResp, err := client.CreateExec(context.Background(), &agboxv1.CreateExecRequest{
		SandboxId: createResp.GetSandbox().GetSandboxId(),
		Command:   []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("CreateExec failed: %v", err)
	}
	waitForExecState(t, client, execResp.GetExecId(), agboxv1.ExecState_EXEC_STATE_FINISHED)

	execStatus, err := client.GetExec(context.Background(), &agboxv1.GetExecRequest{ExecId: execResp.GetExecId()})
	if err != nil {
		t.Fatalf("GetExec failed: %v", err)
	}
	if got := execStatus.GetExec().GetLastEventSequence(); got == 0 {
		t.Fatal("expected non-zero exec last_event_sequence")
	}
}

func TestBuildCopyTarRejectsExternalSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(filepath.Join(externalRoot, "secret.txt"), filepath.Join(sourceRoot, "leak.txt")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	err := buildCopyTar(pw, sourceRoot, nil)
	if err == nil || !strings.Contains(err.Error(), "external symlink") {
		t.Fatalf("expected external symlink failure, got %v", err)
	}
}

func TestBuildCopyTarAppliesExcludePatterns(t *testing.T) {
	sourceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, ".git"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	pr, pw := io.Pipe()
	go func() {
		_ = buildCopyTar(pw, sourceRoot, []string{".git"})
	}()
	tr := tar.NewReader(pr)
	names := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		names[header.Name] = true
	}
	if !names["keep.txt"] {
		t.Fatal("expected keep.txt in tar archive")
	}
	if names[".git"] {
		t.Fatal("expected .git to be excluded from tar archive")
	}
}

func TestBuildCopyTarPreservesSymlinks(t *testing.T) {
	sourceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "real.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(sourceRoot, "link.txt")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	pr, pw := io.Pipe()
	go func() {
		_ = buildCopyTar(pw, sourceRoot, nil)
	}()
	tr := tar.NewReader(pr)
	var found bool
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		if header.Name == "link.txt" {
			found = true
			if header.Typeflag != tar.TypeSymlink {
				t.Fatalf("expected symlink type, got %d", header.Typeflag)
			}
			if header.Linkname != "real.txt" {
				t.Fatalf("expected linkname real.txt, got %s", header.Linkname)
			}
		}
	}
	if !found {
		t.Fatal("expected link.txt in tar archive")
	}
}

// extractTarNames reads all entries from a tar stream and returns a set of archive names.
func extractTarNames(t *testing.T, r io.Reader) map[string]bool {
	t.Helper()
	tr := tar.NewReader(r)
	names := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		names[header.Name] = true
	}
	return names
}

// mkFile creates a file with the given content under parent.
func mkFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

// TestBuildCopyTarGitignoreSkipsExternalSymlinks builds a complex directory tree:
//
//	sourceRoot/
//	  .gitignore              # contains: .generated/
//	  keep.txt                # regular file -> INCLUDED
//	  internal-link.txt       # symlink to keep.txt (internal) -> INCLUDED
//	  top-external.txt        # external symlink, NOT gitignored -> ERROR
//	  .generated/             # directory gitignored, contains external symlink
//	    build.txt             # regular file -> INCLUDED (gitignore only filters external symlinks)
//	    report.txt            # internal symlink -> INCLUDED
//	    external-ref.txt      # external symlink -> whole dir SKIPPED
//	  docs/                   # NOT gitignored, no external symlinks
//	    readme.md             # regular file -> INCLUDED
//	  vendor/                 # gitignored (vendor/), but NO external symlinks -> INCLUDED normally
//	    lib.go                # regular file -> INCLUDED
func TestBuildCopyTarGitignoreSkipsExternalSymlinks(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	mkFile(t, filepath.Join(externalRoot, "external.txt"), "external-content")

	// .gitignore: ignore .generated/ and vendor/
	mkFile(t, filepath.Join(sourceRoot, ".gitignore"), ".generated/\nvendor/\n")

	// Regular files
	mkFile(t, filepath.Join(sourceRoot, "keep.txt"), "keep")
	mkFile(t, filepath.Join(sourceRoot, "docs", "readme.md"), "readme")
	mkFile(t, filepath.Join(sourceRoot, "vendor", "lib.go"), "package lib")

	// .generated/ directory with mixed content
	mkFile(t, filepath.Join(sourceRoot, ".generated", "build.txt"), "build-output")
	// Internal symlink inside .generated/
	if err := os.Symlink(
		filepath.Join(sourceRoot, ".generated", "build.txt"),
		filepath.Join(sourceRoot, ".generated", "report.txt"),
	); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}
	// External symlink inside .generated/ -> triggers directory-level skip
	if err := os.Symlink(
		filepath.Join(externalRoot, "external.txt"),
		filepath.Join(sourceRoot, ".generated", "external-ref.txt"),
	); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	// Internal symlink at top level
	if err := os.Symlink("keep.txt", filepath.Join(sourceRoot, "internal-link.txt")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	// --- Case 1: .generated/ (gitignored + has external symlink) -> entire dir skipped ---
	pr, pw := io.Pipe()
	go func() {
		_ = buildCopyTar(pw, sourceRoot, nil)
	}()
	names := extractTarNames(t, pr)

	// Regular files and internal symlinks should be present
	for _, expected := range []string{"keep.txt", "internal-link.txt", "docs/", "docs/readme.md", "vendor/", "vendor/lib.go"} {
		if !names[expected] {
			t.Errorf("expected %q in tar archive, got entries: %v", expected, names)
		}
	}
	// .generated/ directory and all its contents should be skipped
	for _, excluded := range []string{".generated/", ".generated/build.txt", ".generated/report.txt", ".generated/external-ref.txt"} {
		if names[excluded] {
			t.Errorf("expected %q to be skipped (gitignored dir with external symlink), but found in tar", excluded)
		}
	}

	// --- Case 2: top-level external symlink NOT in gitignore -> still errors ---
	if err := os.Symlink(
		filepath.Join(externalRoot, "external.txt"),
		filepath.Join(sourceRoot, "top-external.txt"),
	); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}
	pr2, pw2 := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr2) }()
	err := buildCopyTar(pw2, sourceRoot, nil)
	if err == nil || !strings.Contains(err.Error(), "external symlink") {
		t.Fatalf("expected external symlink error for top-external.txt, got: %v", err)
	}
}

// TestBuildCopyTarGitignoreFileLevel verifies that a gitignore rule matching
// a specific file (not a directory) skips only that external symlink file.
func TestBuildCopyTarGitignoreFileLevel(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	mkFile(t, filepath.Join(externalRoot, "secret.txt"), "secret")

	// .gitignore ignores a specific file pattern
	mkFile(t, filepath.Join(sourceRoot, ".gitignore"), "ignored-link.txt\n")
	mkFile(t, filepath.Join(sourceRoot, "normal.txt"), "normal")

	// External symlink matching gitignore -> skipped
	if err := os.Symlink(
		filepath.Join(externalRoot, "secret.txt"),
		filepath.Join(sourceRoot, "ignored-link.txt"),
	); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	pr, pw := io.Pipe()
	go func() {
		_ = buildCopyTar(pw, sourceRoot, nil)
	}()
	names := extractTarNames(t, pr)

	if !names["normal.txt"] {
		t.Fatal("expected normal.txt in tar")
	}
	if names["ignored-link.txt"] {
		t.Fatal("expected ignored-link.txt to be skipped (gitignored external symlink)")
	}
}

// TestBuildCopyTarNoGitignore verifies the original behavior is preserved
// when no .gitignore exists.
func TestBuildCopyTarNoGitignore(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	mkFile(t, filepath.Join(externalRoot, "target.txt"), "target")

	mkFile(t, filepath.Join(sourceRoot, "ok.txt"), "ok")
	if err := os.Symlink(
		filepath.Join(externalRoot, "target.txt"),
		filepath.Join(sourceRoot, "bad-link.txt"),
	); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	pr, pw := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	err := buildCopyTar(pw, sourceRoot, nil)
	if err == nil || !strings.Contains(err.Error(), "external symlink") {
		t.Fatalf("expected external symlink error without .gitignore, got: %v", err)
	}
}

func TestPrepareExecOutputPaths(t *testing.T) {
	root := t.TempDir()
	paths, err := prepareExecOutputPaths(root, "{sandbox_id}/{exec_id}", map[string]string{
		"sandbox_id": "sb-1",
		"exec_id":    "ex-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(paths.StdoutPath, ".stdout.log") {
		t.Fatalf("expected stdout path to end with .stdout.log, got %q", paths.StdoutPath)
	}
	if !strings.HasSuffix(paths.StderrPath, ".stderr.log") {
		t.Fatalf("expected stderr path to end with .stderr.log, got %q", paths.StderrPath)
	}
	// Verify parent directory was created but files were not pre-created.
	if _, err := os.Stat(filepath.Dir(paths.StdoutPath)); err != nil {
		t.Fatalf("parent directory should exist: %v", err)
	}
	if _, err := os.Stat(paths.StdoutPath); !os.IsNotExist(err) {
		t.Fatalf("stdout file should not be created by daemon")
	}
}
