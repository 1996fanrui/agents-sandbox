package docker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	ErrArtifactPathEscapesRoot    = errors.New("artifact path escapes configured root")
	ErrArtifactPathUsesSymlink    = errors.New("artifact path uses symlink boundary")
	ErrArtifactPathUsesHardlink   = errors.New("artifact path uses hardlink boundary")
	ErrArtifactTemplateFieldEmpty = errors.New("artifact template field is empty")
)

func PrepareExecOutputPath(root string, template string, fields map[string]string) (string, error) {
	relativePath, err := expandArtifactTemplate(template, fields)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relativePath) {
		return "", ErrArtifactPathEscapesRoot
	}
	cleanRelative := filepath.Clean(relativePath)
	if cleanRelative == "." || cleanRelative == "" || strings.HasPrefix(cleanRelative, "..") {
		return "", ErrArtifactPathEscapesRoot
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return "", err
	}
	targetPath := filepath.Join(rootAbs, cleanRelative)
	parentPath := filepath.Dir(targetPath)
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		return "", err
	}
	parentRealPath, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(rootAbs, parentRealPath) {
		return "", ErrArtifactPathEscapesRoot
	}
	targetInfo, err := os.Lstat(targetPath)
	if err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return "", ErrArtifactPathUsesSymlink
		}
		if usesHardlink(targetInfo) {
			return "", ErrArtifactPathUsesHardlink
		}
		return targetPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return targetPath, nil
}

func expandArtifactTemplate(template string, fields map[string]string) (string, error) {
	resolved := template
	for key, value := range fields {
		if value == "" {
			return "", fmt.Errorf("%w: %s", ErrArtifactTemplateFieldEmpty, key)
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

func usesHardlink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
