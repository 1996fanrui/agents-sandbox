package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/control/reslimits"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateCreateSpec(spec *agboxv1.CreateSpec, caps hostCapabilities) error {
	if spec.GetIdleTtl() != nil && spec.GetIdleTtl().AsDuration() < 0 {
		return errors.New("idle_ttl must not be negative")
	}
	targets := make(map[string]string)
	seenNames := make(map[string]struct{}, len(spec.GetCompanionContainers()))
	registerTarget := func(kind string, target string) error {
		if target == "" {
			return fmt.Errorf("%s target is required", kind)
		}
		if !filepath.IsAbs(target) {
			return fmt.Errorf("%s target must be absolute: %s", kind, target)
		}
		if existingKind, exists := targets[target]; exists {
			return fmt.Errorf("conflicting target %s between %s and %s", target, existingKind, kind)
		}
		targets[target] = kind
		return nil
	}
	for _, mount := range spec.GetMounts() {
		if mount.GetSource() == "" {
			return errors.New("mount source is required")
		}
		if err := validateTildePath(mount.GetSource()); err != nil {
			return err
		}
		if err := validateTildePath(mount.GetTarget()); err != nil {
			return err
		}
		// Reject relative non-tilde source paths before expansion.
		if !strings.HasPrefix(mount.GetSource(), "~") && !filepath.IsAbs(mount.GetSource()) {
			return fmt.Errorf("mount source must be absolute: %s", mount.GetSource())
		}
		expandedSource, err := expandHomePath(mount.GetSource())
		if err != nil {
			return fmt.Errorf("mount source: %w", err)
		}
		mount.Source = expandedSource
		mount.Target = expandContainerHomePath(mount.GetTarget())
		if err := validateGenericSourcePath("mount", mount.GetSource()); err != nil {
			return err
		}
		if err := registerTarget("mount", mount.GetTarget()); err != nil {
			return err
		}
	}
	for _, copy := range spec.GetCopies() {
		if copy.GetSource() == "" {
			return errors.New("copy source is required")
		}
		if err := validateTildePath(copy.GetSource()); err != nil {
			return err
		}
		if err := validateTildePath(copy.GetTarget()); err != nil {
			return err
		}
		// Reject relative non-tilde source paths before expansion.
		if !strings.HasPrefix(copy.GetSource(), "~") && !filepath.IsAbs(copy.GetSource()) {
			return fmt.Errorf("copy source must be absolute: %s", copy.GetSource())
		}
		expandedSource, err := expandHomePath(copy.GetSource())
		if err != nil {
			return fmt.Errorf("copy source: %w", err)
		}
		copy.Source = expandedSource
		copy.Target = expandContainerHomePath(copy.GetTarget())
		if err := validateGenericSourcePath("copy", copy.GetSource()); err != nil {
			return err
		}
		if filepath.Clean(copy.GetSource()) == "/" {
			return errors.New("copy source must not be the root directory")
		}
		if err := registerTarget("copy", copy.GetTarget()); err != nil {
			return err
		}
	}
	seenHostPorts := make(map[string]struct{})
	for _, port := range spec.GetPorts() {
		if port.GetContainerPort() == 0 || port.GetContainerPort() > 65535 {
			return fmt.Errorf("port container_port must be between 1 and 65535, got %d", port.GetContainerPort())
		}
		if port.GetHostPort() == 0 || port.GetHostPort() > 65535 {
			return fmt.Errorf("port host_port must be between 1 and 65535, got %d", port.GetHostPort())
		}
		protoStr := portProtocolToString(port.GetProtocol())
		if protoStr == "" {
			return fmt.Errorf("port protocol must be TCP, UDP, or SCTP, got %d", int32(port.GetProtocol()))
		}
		key := fmt.Sprintf("%d/%s", port.GetHostPort(), protoStr)
		if _, exists := seenHostPorts[key]; exists {
			return fmt.Errorf("duplicate host_port %d with protocol %s", port.GetHostPort(), protoStr)
		}
		seenHostPorts[key] = struct{}{}
	}
	if err := validateCompanionContainerSpecs(spec.GetCompanionContainers(), seenNames); err != nil {
		return err
	}
	// Reject empty-string tokens in the primary command. An empty array is
	// intentionally accepted here because proto3 cannot distinguish "omit" from
	// "explicit []" on repeated fields; the YAML/SDK entry layers own that
	// check (see design doc section "presence semantics and validation layer
	// ownership").
	for i, token := range spec.GetCommand() {
		if token == "" {
			return fmt.Errorf("command[%d]: empty string entry is not allowed", i)
		}
	}
	if err := validateResourceLimits(spec, caps); err != nil {
		return err
	}
	seenBuiltin := make(map[string]struct{}, len(spec.GetBuiltinTools()))
	for _, builtin := range spec.GetBuiltinTools() {
		if builtin == "" {
			return errors.New("builtin resource id must not be empty")
		}
		if _, exists := seenBuiltin[builtin]; exists {
			return fmt.Errorf("duplicate builtin resource %q", builtin)
		}
		if _, ok := profile.CapabilityByID(builtin); !ok {
			return fmt.Errorf("unknown builtin resource %q", builtin)
		}
		seenBuiltin[builtin] = struct{}{}
	}
	return nil
}

// validateResourceLimits parses every resource limit string on the spec and
// gates cpu/memory limits on host capabilities. It returns gRPC status errors
// directly so the caller can surface them as-is to clients.
func validateResourceLimits(spec *agboxv1.CreateSpec, caps hostCapabilities) error {
	cpu, err := reslimits.ParseCPU(spec.GetCpuLimit())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "cpu_limit: %v", err)
	}
	mem, err := reslimits.ParseMemoryOrDisk(spec.GetMemoryLimit(), "memory_limit")
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "memory_limit: %v", err)
	}
	if _, err := reslimits.ParseMemoryOrDisk(spec.GetDiskLimit(), "disk_limit"); err != nil {
		return status.Errorf(codes.InvalidArgument, "disk_limit: %v", err)
	}
	for _, cc := range spec.GetCompanionContainers() {
		if cc.GetDiskLimit() == "" {
			continue
		}
		field := fmt.Sprintf("companion_containers[%s].disk_limit", cc.GetName())
		if _, err := reslimits.ParseMemoryOrDisk(cc.GetDiskLimit(), field); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: %v", field, err)
		}
	}
	if cpu > 0 || mem > 0 {
		if caps.CgroupDriver != "systemd" || !caps.CgroupV2Available {
			return status.Errorf(codes.FailedPrecondition,
				"cpu_limit / memory_limit require cgroup v2 + systemd cgroup driver; got driver=%q v2=%v",
				caps.CgroupDriver, caps.CgroupV2Available)
		}
	}
	return nil
}

func validateCompanionContainerSpecs(items []*agboxv1.CompanionContainerSpec, seen map[string]struct{}) error {
	for _, cc := range items {
		if cc.GetName() == "" {
			return errors.New("companion container name is required")
		}
		if cc.GetImage() == "" {
			return fmt.Errorf("companion container %q image is required", cc.GetName())
		}
		if _, exists := seen[cc.GetName()]; exists {
			return fmt.Errorf("duplicate companion container name %q", cc.GetName())
		}
		seen[cc.GetName()] = struct{}{}
		if len(cc.GetPostStartOnPrimary()) > 0 && cc.GetHealthcheck() == nil {
			return fmt.Errorf("companion container %q with post_start_on_primary must define healthcheck", cc.GetName())
		}
		if err := validateHealthcheck(cc.GetName(), cc.GetHealthcheck()); err != nil {
			return err
		}
		// Same empty-string-token rule as the primary command; empty array
		// semantics remain a YAML/SDK responsibility because proto3 cannot
		// distinguish omit from [] here.
		for i, token := range cc.GetCommand() {
			if token == "" {
				return fmt.Errorf("companion_containers[%s].command[%d]: empty string entry is not allowed", cc.GetName(), i)
			}
		}
	}
	return nil
}

func validateHealthcheck(name string, healthcheck *agboxv1.HealthcheckConfig) error {
	if healthcheck == nil {
		return nil
	}
	if len(healthcheck.GetTest()) == 0 {
		return fmt.Errorf("companion container %q healthcheck.test must not be empty", name)
	}
	command := healthcheck.GetTest()[0]
	allowed := map[string]struct{}{
		"CMD":       {},
		"CMD-SHELL": {},
		"NONE":      {},
	}
	if _, ok := allowed[command]; !ok {
		return fmt.Errorf("companion container %q healthcheck.test[0] %q is invalid", name, command)
	}
	if command == "NONE" {
		if len(healthcheck.GetTest()) > 1 {
			return fmt.Errorf("companion container %q healthcheck.test must not include extra args when NONE is used", name)
		}
	}
	if (command == "CMD" || command == "CMD-SHELL") && len(healthcheck.GetTest()) < 2 {
		return fmt.Errorf("companion container %q healthcheck.test for %s must include a command", name, command)
	}
	// Duration fields use google.protobuf.Duration which is inherently valid when set.
	return nil
}

func validateGenericSourcePath(kind string, source string) error {
	if !filepath.IsAbs(source) {
		return fmt.Errorf("%s source must be absolute: %s", kind, source)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("%s source path is invalid: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s source must not be a symlink: %s", kind, source)
	}
	isSocket := info.Mode()&os.ModeSocket != 0
	if !info.Mode().IsRegular() && !info.IsDir() && !isSocket {
		return fmt.Errorf("%s source must be a file, directory, or unix socket: %s", kind, source)
	}
	return nil
}

func prepareExecOutputPaths(root string, template string, fields map[string]string) (execArtifactPaths, error) {
	prefix, err := prepareExecOutputPrefix(root, template, fields)
	if err != nil {
		return execArtifactPaths{}, err
	}
	return execArtifactPaths{
		StdoutPath: prefix + ".stdout.log",
		StderrPath: prefix + ".stderr.log",
	}, nil
}

func prepareExecOutputPrefix(root string, template string, fields map[string]string) (string, error) {
	relativePath, err := expandArtifactTemplate(template, fields)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relativePath) {
		return "", errArtifactPathEscapesRoot
	}
	cleanRelative := filepath.Clean(relativePath)
	if cleanRelative == "." || cleanRelative == "" || strings.HasPrefix(cleanRelative, "..") {
		return "", errArtifactPathEscapesRoot
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return "", err
	}
	// Resolve symlinks on the root so that the containment check compares
	// real paths on both sides (e.g. macOS /var -> /private/var).
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	targetPrefix := filepath.Join(rootReal, cleanRelative)
	parentPath := filepath.Dir(targetPrefix)
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return "", err
	}
	parentRealPath, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(rootReal, parentRealPath) {
		return "", errArtifactPathEscapesRoot
	}
	return targetPrefix, nil
}

func expandArtifactTemplate(template string, fields map[string]string) (string, error) {
	resolved := template
	for key, value := range fields {
		if value == "" {
			return "", fmt.Errorf("%w: %s", errArtifactTemplateFieldEmpty, key)
		}
		resolved = strings.ReplaceAll(resolved, "{"+key+"}", value)
	}
	if strings.Contains(resolved, "{") || strings.Contains(resolved, "}") {
		return "", fmt.Errorf("artifact template contains unresolved field: %s", resolved)
	}
	return resolved, nil
}

func pathWithinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..")
}
