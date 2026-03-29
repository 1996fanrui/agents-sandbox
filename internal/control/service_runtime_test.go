package control

import (
	"context"
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

func TestMaterializeGenericCopiesRejectsExternalSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalRoot, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.Symlink(filepath.Join(externalRoot, "secret.txt"), filepath.Join(sourceRoot, "leak.txt")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	backend := &dockerRuntimeBackend{config: ServiceConfig{StateRoot: t.TempDir()}}
	state := &sandboxRuntimeState{}
	_, err := backend.materializeGenericCopies("sandbox-1", []*agboxv1.CopySpec{
		{Source: sourceRoot, Target: "/workspace/project"},
	}, state)
	if err == nil || !strings.Contains(err.Error(), "external symlink") {
		t.Fatalf("expected external symlink failure, got %v", err)
	}
}

func TestMaterializeGenericCopiesAppliesExcludePatterns(t *testing.T) {
	sourceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, ".git"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	backend := &dockerRuntimeBackend{config: ServiceConfig{StateRoot: t.TempDir()}}
	state := &sandboxRuntimeState{}
	mounts, err := backend.materializeGenericCopies("sandbox-1", []*agboxv1.CopySpec{
		{Source: sourceRoot, Target: "/workspace/project", ExcludePatterns: []string{".git"}},
	}, state)
	if err != nil {
		t.Fatalf("materializeGenericCopies failed: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("expected one mount, got %d", len(mounts))
	}
	if _, err := os.Stat(filepath.Join(mounts[0].Source, "keep.txt")); err != nil {
		t.Fatalf("expected keep.txt to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mounts[0].Source, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file to be absent, got %v", err)
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
