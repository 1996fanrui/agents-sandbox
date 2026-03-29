package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agboxv1 "github.com/1996fanrui/agents-sandbox/api/generated/agboxv1"
	"github.com/1996fanrui/agents-sandbox/internal/profile"
)

func validateCreateSpec(spec *agboxv1.CreateSpec) error {
	targets := make(map[string]string)
	seenServiceNames := make(map[string]struct{}, len(spec.GetRequiredServices())+len(spec.GetOptionalServices()))
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
		if err := validateGenericSourcePath("copy", copy.GetSource()); err != nil {
			return err
		}
		if err := registerTarget("copy", copy.GetTarget()); err != nil {
			return err
		}
	}
	if err := validateServiceSpecs(spec.GetRequiredServices(), true, seenServiceNames); err != nil {
		return err
	}
	if err := validateServiceSpecs(spec.GetOptionalServices(), false, seenServiceNames); err != nil {
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

func validateServiceSpecs(items []*agboxv1.ServiceSpec, required bool, seen map[string]struct{}) error {
	for _, service := range items {
		if service.GetName() == "" {
			return errors.New("service name is required")
		}
		if service.GetImage() == "" {
			return fmt.Errorf("service %q image is required", service.GetName())
		}
		if _, exists := seen[service.GetName()]; exists {
			return fmt.Errorf("duplicate service name %q", service.GetName())
		}
		seen[service.GetName()] = struct{}{}
		if required && service.GetHealthcheck() == nil {
			return fmt.Errorf("required service %q must define healthcheck", service.GetName())
		}
		if !required && len(service.GetPostStartOnPrimary()) > 0 && service.GetHealthcheck() == nil {
			return fmt.Errorf("optional service %q with post_start_on_primary must define healthcheck", service.GetName())
		}
		if err := validateHealthcheck(service.GetName(), service.GetHealthcheck(), required); err != nil {
			return err
		}
	}
	return nil
}

func validateHealthcheck(serviceName string, healthcheck *agboxv1.HealthcheckConfig, required bool) error {
	if healthcheck == nil {
		return nil
	}
	if len(healthcheck.GetTest()) == 0 {
		return fmt.Errorf("service %q healthcheck.test must not be empty", serviceName)
	}
	command := healthcheck.GetTest()[0]
	allowed := map[string]struct{}{
		"CMD":       {},
		"CMD-SHELL": {},
	}
	if !required {
		allowed["NONE"] = struct{}{}
	}
	if _, ok := allowed[command]; !ok {
		return fmt.Errorf("service %q healthcheck.test[0] %q is invalid", serviceName, command)
	}
	if command == "NONE" && len(healthcheck.GetTest()) > 1 {
		return fmt.Errorf("service %q healthcheck.test must not include extra args when NONE is used", serviceName)
	}
	if (command == "CMD" || command == "CMD-SHELL") && len(healthcheck.GetTest()) < 2 {
		return fmt.Errorf("service %q healthcheck.test for %s must include a command", serviceName, command)
	}
	for _, raw := range []string{
		healthcheck.GetInterval(),
		healthcheck.GetTimeout(),
		healthcheck.GetStartPeriod(),
		healthcheck.GetStartInterval(),
	} {
		if raw == "" {
			continue
		}
		if _, err := time.ParseDuration(raw); err != nil {
			return fmt.Errorf("service %q healthcheck duration %q is invalid: %w", serviceName, raw, err)
		}
	}
	return nil
}

func validateGenericSourcePath(kind string, source string) error {
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("%s source path is invalid: %w", kind, err)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("%s source path is invalid: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s source must not be a symlink: %s", kind, sourcePath)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("%s source must be a file or directory: %s", kind, sourcePath)
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
	targetPrefix := filepath.Join(rootAbs, cleanRelative)
	parentPath := filepath.Dir(targetPrefix)
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return "", err
	}
	parentRealPath, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(rootAbs, parentRealPath) {
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
