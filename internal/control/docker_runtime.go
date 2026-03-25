package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
)

type runtimeBackend interface {
	CreateSandbox(context.Context, *sandboxRecord) (runtimeCreateResult, error)
	ResumeSandbox(context.Context, *sandboxRecord) error
	StopSandbox(context.Context, *sandboxRecord) error
	DeleteSandbox(context.Context, *sandboxRecord) error
	RunExec(context.Context, *sandboxRecord, *agboxv1.ExecStatus) (runtimeExecResult, error)
}

type runtimeCreateResult struct {
	ResolvedTooling []*agboxv1.ResolvedProjectionHandle
	DependencyNames []string
	RuntimeState    *sandboxRuntimeState
}

type runtimeExecResult struct {
	ExitCode int32
	Stdout   string
	Stderr   string
}

type sandboxRuntimeState struct {
	NetworkName             string
	PrimaryContainerName    string
	DependencyContainerName []string
	WorkspaceHostPath       string
	WorkspaceOwned          bool
	ShadowRoot              string
}

type fakeRuntimeBackend struct{}

func (fakeRuntimeBackend) CreateSandbox(_ context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	dependencyNames := make([]string, 0, len(record.dependencies))
	for _, dependency := range record.dependencies {
		dependencyNames = append(dependencyNames, dependency.GetDependencyName())
	}
	return runtimeCreateResult{
		ResolvedTooling: resolveTooling(record.createSpec.GetToolingProjections()),
		DependencyNames: dependencyNames,
		RuntimeState: &sandboxRuntimeState{
			NetworkName:             "fake-network-" + record.handle.GetSandboxId(),
			PrimaryContainerName:    "fake-primary-" + record.handle.GetSandboxId(),
			DependencyContainerName: slices.Clone(dependencyNames),
		},
	}, nil
}

func (fakeRuntimeBackend) ResumeSandbox(context.Context, *sandboxRecord) error { return nil }
func (fakeRuntimeBackend) StopSandbox(context.Context, *sandboxRecord) error   { return nil }
func (fakeRuntimeBackend) DeleteSandbox(context.Context, *sandboxRecord) error { return nil }

func (fakeRuntimeBackend) RunExec(_ context.Context, _ *sandboxRecord, _ *agboxv1.ExecStatus) (runtimeExecResult, error) {
	return runtimeExecResult{ExitCode: 0}, nil
}

type dockerRuntimeBackend struct {
	config ServiceConfig
}

func newDockerRuntimeBackend(config ServiceConfig) runtimeBackend {
	return &dockerRuntimeBackend{config: config}
}

func (backend *dockerRuntimeBackend) CreateSandbox(ctx context.Context, record *sandboxRecord) (runtimeCreateResult, error) {
	state := &sandboxRuntimeState{
		NetworkName:          dockerNetworkName(record.handle.GetSandboxId()),
		PrimaryContainerName: dockerPrimaryContainerName(record.handle.GetSandboxId()),
	}
	cleanupRequired := false
	defer func() {
		if !cleanupRequired {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = backend.deleteRuntimeArtifacts(cleanupCtx, state)
	}()

	workspaceHostPath, workspaceOwned, shadowRoot, err := backend.materializeWorkspace(record.handle.GetSandboxId(), record.createSpec.GetWorkspace())
	if err != nil {
		return runtimeCreateResult{}, err
	}
	state.WorkspaceHostPath = workspaceHostPath
	state.WorkspaceOwned = workspaceOwned
	state.ShadowRoot = shadowRoot

	resolvedTooling, mounts, err := backend.materializeTooling(record.handle.GetSandboxId(), record.createSpec.GetToolingProjections(), state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	builtinRequests, err := backend.buildBuiltinResourceRequests(record.createSpec.GetBuiltinResources())
	if err != nil {
		return runtimeCreateResult{}, err
	}
	resolvedBuiltin, builtinMounts, err := backend.materializeTooling(record.handle.GetSandboxId(), builtinRequests, state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	resolvedTooling = append(resolvedTooling, resolvedBuiltin...)
	mounts = append(mounts, builtinMounts...)
	genericMounts, err := backend.materializeGenericMounts(record.createSpec.GetMounts())
	if err != nil {
		return runtimeCreateResult{}, err
	}
	mounts = append(mounts, genericMounts...)
	copyMounts, err := backend.materializeGenericCopies(record.handle.GetSandboxId(), record.createSpec.GetCopies(), state)
	if err != nil {
		return runtimeCreateResult{}, err
	}
	mounts = append(mounts, copyMounts...)
	if workspaceHostPath != "" {
		mounts = append(mounts, dockerMount{
			Source:   workspaceHostPath,
			Target:   "/workspace",
			ReadOnly: false,
		})
	}
	if err := ensureUniqueMountTargets(mounts); err != nil {
		return runtimeCreateResult{}, err
	}

	cleanupRequired = true
	if err := ensureDockerImage(ctx, record.createSpec.GetImage()); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := dockerNetworkCreate(ctx, state.NetworkName, runtimedocker.SandboxLabels(record.handle.GetSandboxId(), record.sandboxOwner, "default")); err != nil {
		return runtimeCreateResult{}, err
	}

	dependencyNames := make([]string, 0, len(record.dependencies))
	state.DependencyContainerName = make([]string, 0, len(record.dependencies))
	for _, dependency := range record.dependencies {
		if dependency.GetDependencyName() == "" || dependency.GetImage() == "" {
			return runtimeCreateResult{}, fmt.Errorf("dependency name and image are required")
		}
		dependencyNames = append(dependencyNames, dependency.GetDependencyName())
		containerName := dockerDependencyContainerName(record.handle.GetSandboxId(), dependency.GetDependencyName())
		state.DependencyContainerName = append(state.DependencyContainerName, containerName)
		if err := ensureDockerImage(ctx, dependency.GetImage()); err != nil {
			return runtimeCreateResult{}, err
		}
		if err := dockerContainerCreate(ctx, dockerContainerSpec{
			Name:         containerName,
			Image:        dependency.GetImage(),
			NetworkName:  state.NetworkName,
			NetworkAlias: firstNonEmpty(dependency.GetNetworkAlias(), dependency.GetDependencyName()),
			Labels:       runtimedocker.DependencyLabels(record.handle.GetSandboxId(), record.sandboxOwner, dependency.GetDependencyName()),
			Environment:  keyValuesToMap(dependency.GetEnvironment()),
		}); err != nil {
			return runtimeCreateResult{}, err
		}
		if err := dockerContainerStart(ctx, containerName); err != nil {
			return runtimeCreateResult{}, err
		}
		if err := dockerWaitContainerRunning(ctx, containerName, 10*time.Second); err != nil {
			return runtimeCreateResult{}, err
		}
	}

	if err := dockerContainerCreate(ctx, dockerContainerSpec{
		Name:        state.PrimaryContainerName,
		Image:       record.createSpec.GetImage(),
		NetworkName: state.NetworkName,
		Labels:      runtimedocker.SandboxLabels(record.handle.GetSandboxId(), record.sandboxOwner, "default"),
		Mounts:      mounts,
		Environment: primaryContainerEnvironment(mounts),
		Workdir:     "/workspace",
		Command: []string{
			"sh",
			"-lc",
			"trap 'exit 0' TERM INT; while sleep 3600; do :; done",
		},
	}); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := dockerContainerStart(ctx, state.PrimaryContainerName); err != nil {
		return runtimeCreateResult{}, err
	}
	if err := dockerWaitContainerRunning(ctx, state.PrimaryContainerName, 10*time.Second); err != nil {
		return runtimeCreateResult{}, err
	}

	cleanupRequired = false
	return runtimeCreateResult{
		ResolvedTooling: resolvedTooling,
		DependencyNames: dependencyNames,
		RuntimeState:    state,
	}, nil
}

func (backend *dockerRuntimeBackend) ResumeSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return errors.New("sandbox runtime state is missing")
	}
	if err := dockerContainerMustExist(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return err
	}
	for _, containerName := range record.runtimeState.DependencyContainerName {
		if err := dockerContainerMustExist(ctx, containerName); err != nil {
			return err
		}
	}
	for _, containerName := range record.runtimeState.DependencyContainerName {
		if err := dockerContainerEnsureRunning(ctx, containerName); err != nil {
			return err
		}
	}
	return dockerContainerEnsureRunning(ctx, record.runtimeState.PrimaryContainerName)
}

func (backend *dockerRuntimeBackend) StopSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return errors.New("sandbox runtime state is missing")
	}
	if err := dockerContainerStop(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return err
	}
	for _, containerName := range record.runtimeState.DependencyContainerName {
		if err := dockerContainerStop(ctx, containerName); err != nil {
			return err
		}
	}
	return nil
}

func (backend *dockerRuntimeBackend) DeleteSandbox(ctx context.Context, record *sandboxRecord) error {
	if record.runtimeState == nil {
		return nil
	}
	return backend.deleteRuntimeArtifacts(ctx, record.runtimeState)
}

func (backend *dockerRuntimeBackend) deleteRuntimeArtifacts(ctx context.Context, state *sandboxRuntimeState) error {
	var joined []error
	if state == nil {
		return nil
	}
	if state.PrimaryContainerName != "" {
		joined = append(joined, dockerContainerRemove(ctx, state.PrimaryContainerName))
	}
	for _, containerName := range state.DependencyContainerName {
		joined = append(joined, dockerContainerRemove(ctx, containerName))
	}
	if state.NetworkName != "" {
		joined = append(joined, dockerNetworkRemove(ctx, state.NetworkName))
	}
	if state.WorkspaceOwned && state.WorkspaceHostPath != "" {
		joined = append(joined, os.RemoveAll(state.WorkspaceHostPath))
	}
	if state.ShadowRoot != "" {
		joined = append(joined, os.RemoveAll(state.ShadowRoot))
	}
	return errors.Join(joined...)
}

func (backend *dockerRuntimeBackend) RunExec(ctx context.Context, record *sandboxRecord, execRecord *agboxv1.ExecStatus) (runtimeExecResult, error) {
	if record.runtimeState == nil {
		return runtimeExecResult{}, errors.New("sandbox runtime state is missing")
	}
	if err := dockerContainerMustExist(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeExecResult{}, err
	}
	if err := dockerContainerEnsureRunning(ctx, record.runtimeState.PrimaryContainerName); err != nil {
		return runtimeExecResult{}, err
	}
	output, exitCode, err := dockerExec(ctx, dockerExecSpec{
		ContainerName: record.runtimeState.PrimaryContainerName,
		Command:       execRecord.GetCommand(),
		Workdir:       execRecord.GetCwd(),
		Environment:   keyValuesToMap(execRecord.GetEnvOverrides()),
	})
	return runtimeExecResult{
		ExitCode: exitCode,
		Stdout:   output.Stdout,
		Stderr:   output.Stderr,
	}, err
}

func (backend *dockerRuntimeBackend) materializeWorkspace(
	sandboxID string,
	workspace *agboxv1.WorkspaceSpec,
) (hostPath string, owned bool, shadowRoot string, err error) {
	if workspace == nil || workspace.GetPath() == "" {
		return "", false, "", nil
	}
	sourceRoot, err := filepath.Abs(workspace.GetPath())
	if err != nil {
		return "", false, "", err
	}
	switch workspace.GetMode() {
	case agboxv1.WorkspaceMaterializationMode_WORKSPACE_MATERIALIZATION_MODE_BIND:
		return sourceRoot, false, "", nil
	case agboxv1.WorkspaceMaterializationMode_WORKSPACE_MATERIALIZATION_MODE_DURABLE_COPY, agboxv1.WorkspaceMaterializationMode_WORKSPACE_MATERIALIZATION_MODE_UNSPECIFIED:
		if backend.config.StateRoot == "" {
			return "", false, "", errors.New("runtime.state_root is required for durable_copy workspaces")
		}
		if err := runtimedocker.ValidateWorkspaceTree(sourceRoot); err != nil {
			return "", false, "", err
		}
		targetRoot := filepath.Join(backend.config.StateRoot, "sandboxes", sandboxID, "workspace")
		if err := os.RemoveAll(targetRoot); err != nil {
			return "", false, "", err
		}
		if err := copyTree(sourceRoot, targetRoot); err != nil {
			return "", false, "", err
		}
		return targetRoot, true, "", nil
	default:
		return "", false, "", fmt.Errorf("unsupported workspace materialization mode %s", workspace.GetMode())
	}
}

func (backend *dockerRuntimeBackend) materializeGenericMounts(
	requests []*agboxv1.MountSpec,
) ([]dockerMount, error) {
	mounts := make([]dockerMount, 0, len(requests))
	for _, request := range requests {
		if request.GetSource() == "" {
			return nil, errors.New("mount source is required")
		}
		if request.GetTarget() == "" {
			return nil, errors.New("mount target is required")
		}
		if !filepath.IsAbs(request.GetTarget()) {
			return nil, fmt.Errorf("mount target must be absolute: %s", request.GetTarget())
		}
		sourcePath, err := filepath.Abs(request.GetSource())
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("mount source must not be a symlink: %s", sourcePath)
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("mount source must be a file or directory: %s", sourcePath)
		}
		mounts = append(mounts, dockerMount{
			Source:   sourcePath,
			Target:   request.GetTarget(),
			ReadOnly: !request.GetWritable(),
		})
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) materializeGenericCopies(
	sandboxID string,
	requests []*agboxv1.CopySpec,
	state *sandboxRuntimeState,
) ([]dockerMount, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if backend.config.StateRoot == "" {
		return nil, errors.New("runtime.state_root is required for generic copy inputs")
	}
	if state.ShadowRoot == "" {
		state.ShadowRoot = filepath.Join(backend.config.StateRoot, "sandboxes", sandboxID, "shadow")
	}
	mounts := make([]dockerMount, 0, len(requests))
	for index, request := range requests {
		if request.GetSource() == "" {
			return nil, errors.New("copy source is required")
		}
		if request.GetTarget() == "" {
			return nil, errors.New("copy target is required")
		}
		if !filepath.IsAbs(request.GetTarget()) {
			return nil, fmt.Errorf("copy target must be absolute: %s", request.GetTarget())
		}
		sourcePath, err := filepath.Abs(request.GetSource())
		if err != nil {
			return nil, err
		}
		sourceInfo, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("copy source must not be a symlink: %s", sourcePath)
		}
		if !sourceInfo.Mode().IsRegular() && !sourceInfo.IsDir() {
			return nil, fmt.Errorf("copy source must be a file or directory: %s", sourcePath)
		}
		copyRoot := filepath.Join(state.ShadowRoot, "copies", fmt.Sprintf("%02d-%s", index, sanitizeRuntimeName(request.GetTarget())))
		if err := os.RemoveAll(copyRoot); err != nil {
			return nil, err
		}
		if err := copyTreeWithPatterns(sourcePath, copyRoot, request.GetExcludePatterns()); err != nil {
			return nil, err
		}
		mounts = append(mounts, dockerMount{
			Source:   copyRoot,
			Target:   request.GetTarget(),
			ReadOnly: false,
		})
	}
	return mounts, nil
}

func (backend *dockerRuntimeBackend) buildBuiltinResourceRequests(resources []string) ([]*agboxv1.ToolingProjectionRequest, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	requests := make([]*agboxv1.ToolingProjectionRequest, 0, len(resources))
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if resource == "" {
			return nil, errors.New("builtin resource id must not be empty")
		}
		if _, exists := seen[resource]; exists {
			return nil, fmt.Errorf("duplicate builtin resource %q", resource)
		}
		if _, ok := profile.CapabilityByID(resource); !ok {
			return nil, fmt.Errorf("unknown builtin resource %q", resource)
		}
		seen[resource] = struct{}{}
		requests = append(requests, &agboxv1.ToolingProjectionRequest{CapabilityId: resource})
	}
	return requests, nil
}

func (backend *dockerRuntimeBackend) materializeTooling(
	sandboxID string,
	requests []*agboxv1.ToolingProjectionRequest,
	state *sandboxRuntimeState,
) ([]*agboxv1.ResolvedProjectionHandle, []dockerMount, error) {
	if len(requests) == 0 {
		return nil, nil, nil
	}
	resolved := make([]*agboxv1.ResolvedProjectionHandle, 0, len(requests))
	mounts := make([]dockerMount, 0, len(requests))
	for _, request := range requests {
		capability, ok := profile.CapabilityByID(request.GetCapabilityId())
		if !ok {
			return nil, nil, fmt.Errorf("unknown tooling capability %q", request.GetCapabilityId())
		}
		sourcePath, err := resolveCapabilitySource(capability)
		if err != nil {
			return nil, nil, err
		}
		if request.GetSourcePath() != "" {
			requestSource, err := filepath.Abs(request.GetSourcePath())
			if err != nil {
				return nil, nil, err
			}
			if requestSource != sourcePath {
				return nil, nil, fmt.Errorf("custom source path is not supported for capability %q", capability.ID)
			}
		}
		targetPath := firstNonEmpty(request.GetTargetPath(), capability.ContainerTarget)
		writable, err := resolveCapabilityWritable(capability, request)
		if err != nil {
			return nil, nil, err
		}
		switch capability.Mode {
		case profile.CapabilityModeSocket:
			if err := requireSocketPath(sourcePath); err != nil {
				return nil, nil, err
			}
			mounts = append(mounts, dockerMount{Source: sourcePath, Target: targetPath, ReadOnly: false})
			resolved = append(resolved, &agboxv1.ResolvedProjectionHandle{
				CapabilityId: capability.ID,
				SourcePath:   sourcePath,
				TargetPath:   targetPath,
				MountMode:    agboxv1.ProjectionMountMode_PROJECTION_MOUNT_MODE_BIND,
				Writable:     false,
				WriteBack:    false,
			})
		default:
			info, err := os.Stat(sourcePath)
			if err != nil {
				return nil, nil, err
			}
			mode := agboxv1.ProjectionMountMode_PROJECTION_MOUNT_MODE_BIND
			writeBack := writable
			actualSource := sourcePath
			if info.IsDir() {
				resolution, err := runtimedocker.ResolveProjectionMode(sourcePath, []string{sourcePath}, writable)
				if err != nil {
					return nil, nil, err
				}
				if resolution.Mode == runtimedocker.ProjectionModeShadowCopy {
					if backend.config.StateRoot == "" {
						return nil, nil, errors.New("runtime.state_root is required for shadow_copy projections")
					}
					state.ShadowRoot = filepath.Join(backend.config.StateRoot, "sandboxes", sandboxID, "shadow")
					actualSource = filepath.Join(state.ShadowRoot, sanitizeRuntimeName(capability.ID))
					if err := os.RemoveAll(actualSource); err != nil {
						return nil, nil, err
					}
					if err := copyTree(sourcePath, actualSource); err != nil {
						return nil, nil, err
					}
					mode = agboxv1.ProjectionMountMode_PROJECTION_MOUNT_MODE_SHADOW_COPY
					writeBack = false
				}
			}
			mounts = append(mounts, dockerMount{Source: actualSource, Target: targetPath, ReadOnly: !writable})
			resolved = append(resolved, &agboxv1.ResolvedProjectionHandle{
				CapabilityId: capability.ID,
				SourcePath:   actualSource,
				TargetPath:   targetPath,
				MountMode:    mode,
				Writable:     writable,
				WriteBack:    writeBack,
			})
		}
	}
	return resolved, mounts, nil
}

func resolveCapabilitySource(capability profile.ToolingCapability) (string, error) {
	if capability.DefaultHostPath == "SSH_AUTH_SOCK" {
		socketPath := os.Getenv("SSH_AUTH_SOCK")
		if socketPath == "" {
			return "", errors.New("SSH_AUTH_SOCK is required for ssh-agent tooling projection")
		}
		return filepath.Abs(socketPath)
	}
	return expandHomePath(capability.DefaultHostPath)
}

func resolveCapabilityWritable(capability profile.ToolingCapability, request *agboxv1.ToolingProjectionRequest) (bool, error) {
	switch capability.Mode {
	case profile.CapabilityModeReadOnly:
		if request.GetWritable() {
			return false, fmt.Errorf("capability %q is read-only", capability.ID)
		}
		return false, nil
	case profile.CapabilityModeSocket:
		if request.GetWritable() {
			return false, fmt.Errorf("capability %q does not support writable mounts", capability.ID)
		}
		return false, nil
	default:
		if request.GetSourcePath() == "" && request.GetTargetPath() == "" && !request.GetWritable() {
			return true, nil
		}
		return true, nil
	}
}

func requireSocketPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a Unix socket", path)
	}
	return nil
}

func expandHomePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func keyValuesToMap(items []*agboxv1.KeyValue) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		result[item.GetKey()] = item.GetValue()
	}
	return result
}

func hasMountedToolingTarget(mounts []dockerMount, target string) bool {
	for _, mount := range mounts {
		if mount.Target == target {
			return true
		}
	}
	return false
}

func dockerNetworkName(sandboxID string) string {
	return "agbox-net-" + sanitizeRuntimeName(sandboxID)
}

func dockerPrimaryContainerName(sandboxID string) string {
	return "agbox-primary-" + sanitizeRuntimeName(sandboxID)
}

func dockerDependencyContainerName(sandboxID string, dependencyName string) string {
	return "agbox-dep-" + sanitizeRuntimeName(sandboxID) + "-" + sanitizeRuntimeName(dependencyName)
}

func sanitizeRuntimeName(value string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", ".", "-", "_", "-")
	return replacer.Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func copyTree(sourceRoot string, targetRoot string) error {
	return copyTreeWithPatterns(sourceRoot, targetRoot, nil)
}

func copyTreeWithPatterns(sourceRoot string, targetRoot string, excludePatterns []string) error {
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return copyFile(sourceRoot, targetRoot, sourceInfo.Mode())
	}
	rootAbs, err := filepath.Abs(sourceRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(currentSource string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, err := filepath.Rel(sourceRoot, currentSource)
		if err != nil {
			return err
		}
		currentTarget := targetRoot
		if relativePath != "." {
			currentTarget = filepath.Join(targetRoot, relativePath)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if relativePath != "." && matchesExcludePattern(relativePath, excludePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(currentTarget, info.Mode())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := rewriteCopiedSymlink(rootAbs, targetRoot, currentSource, currentTarget)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(currentTarget), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, currentTarget)
		}
		return copyFile(currentSource, currentTarget, info.Mode())
	})
}

func rewriteCopiedSymlink(sourceRoot string, targetRoot string, currentSource string, currentTarget string) (string, error) {
	target, err := os.Readlink(currentSource)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := runtimedocker.ResolveLinkTarget(currentSource)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(sourceRoot, resolvedTarget) {
		return "", fmt.Errorf("copy source contains external symlink: %s", currentSource)
	}
	if filepath.IsAbs(target) {
		relativeTarget, err := filepath.Rel(sourceRoot, resolvedTarget)
		if err != nil {
			return "", err
		}
		return filepath.Rel(filepath.Dir(currentTarget), filepath.Join(targetRoot, relativeTarget))
	}
	return target, nil
}

func matchesExcludePattern(relativePath string, patterns []string) bool {
	base := filepath.Base(relativePath)
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if matched, err := filepath.Match(pattern, relativePath); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(pattern, base); err == nil && matched {
			return true
		}
	}
	return false
}

func copyFile(sourcePath string, targetPath string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer targetFile.Close()
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return err
	}
	return nil
}

type dockerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type dockerContainerSpec struct {
	Name         string
	Image        string
	NetworkName  string
	NetworkAlias string
	Labels       map[string]string
	Environment  map[string]string
	Mounts       []dockerMount
	Workdir      string
	Command      []string
}

type dockerExecSpec struct {
	ContainerName string
	Command       []string
	Workdir       string
	Environment   map[string]string
}

type dockerInspectState struct {
	Running  bool   `json:"Running"`
	Status   string `json:"Status"`
	ExitCode int    `json:"ExitCode"`
}

func ensureUniqueMountTargets(mounts []dockerMount) error {
	targets := make(map[string]string, len(mounts))
	for _, mount := range mounts {
		if mount.Target == "" {
			return errors.New("mount target is required")
		}
		if !filepath.IsAbs(mount.Target) {
			return fmt.Errorf("mount target must be absolute: %s", mount.Target)
		}
		if existingSource, exists := targets[mount.Target]; exists {
			return fmt.Errorf("conflicting mount target %s for %s and %s", mount.Target, existingSource, mount.Source)
		}
		targets[mount.Target] = mount.Source
	}
	return nil
}

func ensureDockerImage(ctx context.Context, image string) error {
	if image == "" {
		return errors.New("sandbox image must be configured")
	}
	if _, err := runDocker(ctx, "image", "inspect", image); err == nil {
		return nil
	}
	_, err := runDocker(ctx, "pull", image)
	return err
}

func dockerNetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	args := []string{"network", "create"}
	args = appendDockerLabels(args, labels)
	args = append(args, name)
	_, err := runDocker(ctx, args...)
	return err
}

func dockerNetworkRemove(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := runDocker(ctx, "network", "rm", name)
	if err != nil && strings.Contains(err.Error(), "No such network") {
		return nil
	}
	return err
}

func dockerContainerCreate(ctx context.Context, spec dockerContainerSpec) error {
	args := []string{"create", "--init", "--name", spec.Name}
	args = appendDockerLabels(args, spec.Labels)
	if spec.NetworkName != "" {
		args = append(args, "--network", spec.NetworkName)
	}
	if spec.NetworkAlias != "" {
		args = append(args, "--network-alias", spec.NetworkAlias)
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	for key, value := range spec.Environment {
		args = append(args, "--env", key+"="+value)
	}
	for _, mount := range spec.Mounts {
		mountArg := fmt.Sprintf("type=bind,src=%s,dst=%s", mount.Source, mount.Target)
		if mount.ReadOnly {
			mountArg += ",readonly"
		}
		args = append(args, "--mount", mountArg)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	_, err := runDocker(ctx, args...)
	return err
}

func primaryContainerEnvironment(mounts []dockerMount) map[string]string {
	environment := map[string]string{
		"HOST_UID": strconv.Itoa(os.Getuid()),
		"HOST_GID": strconv.Itoa(os.Getgid()),
	}
	if hasMountedToolingTarget(mounts, "/ssh-agent") {
		environment["SSH_AUTH_SOCK"] = "/ssh-agent"
	}
	return environment
}

func dockerContainerStart(ctx context.Context, name string) error {
	_, err := runDocker(ctx, "start", name)
	return err
}

func dockerContainerEnsureRunning(ctx context.Context, name string) error {
	state, err := dockerContainerState(ctx, name)
	if err != nil {
		return err
	}
	if state.Running {
		return nil
	}
	if err := dockerContainerStart(ctx, name); err != nil {
		return err
	}
	return dockerWaitContainerRunning(ctx, name, 10*time.Second)
}

func dockerWaitContainerRunning(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := dockerContainerState(ctx, name)
		if err != nil {
			return err
		}
		if state.Running {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("container %s did not become running", name)
}

func dockerContainerStop(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	state, err := dockerContainerState(ctx, name)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") {
			return nil
		}
		return err
	}
	if !state.Running {
		return nil
	}
	_, err = runDocker(ctx, "stop", "--time", "5", name)
	return err
}

func dockerContainerRemove(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := runDocker(ctx, "rm", "--force", "--volumes", name)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

func dockerContainerMustExist(ctx context.Context, name string) error {
	_, err := dockerContainerState(ctx, name)
	return err
}

func dockerContainerState(ctx context.Context, name string) (dockerInspectState, error) {
	output, err := runDocker(ctx, "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		return dockerInspectState{}, err
	}
	var state dockerInspectState
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &state); err != nil {
		return dockerInspectState{}, err
	}
	return state, nil
}

type dockerExecOutput struct {
	Stdout string
	Stderr string
}

func dockerExec(ctx context.Context, spec dockerExecSpec) (dockerExecOutput, int32, error) {
	if len(spec.Command) == 0 {
		return dockerExecOutput{}, 0, errors.New("exec command must not be empty")
	}
	args := []string{"exec"}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	for key, value := range spec.Environment {
		args = append(args, "--env", key+"="+value)
	}
	args = append(args, spec.ContainerName)
	args = append(args, spec.Command...)
	output, exitCode, err := runDockerWithExitCode(ctx, args...)
	return output, exitCode, err
}

func appendDockerLabels(args []string, labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	return args
}

func runDocker(ctx context.Context, args ...string) (string, error) {
	output, _, err := runDockerWithExitCode(ctx, args...)
	return output.Stdout, err
}

func runDockerWithExitCode(ctx context.Context, args ...string) (dockerExecOutput, int32, error) {
	command := exec.CommandContext(ctx, "docker", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	output := dockerExecOutput{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	if err == nil {
		return output, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		message := strings.TrimSpace(strings.Join([]string{output.Stderr, output.Stdout}, "\n"))
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return output, int32(status.ExitStatus()), fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), message)
		}
		return output, int32(exitErr.ExitCode()), fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), message)
	}
	if ctx.Err() != nil {
		return output, -1, ctx.Err()
	}
	return output, -1, fmt.Errorf("docker %s failed: %w", strings.Join(args, " "), err)
}
