package docker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

var ErrProjectionTargetUnreadable = errors.New("projection target is unreadable")

type ProjectionMode string

const (
	ProjectionModeBind       ProjectionMode = "bind"
	ProjectionModeShadowCopy ProjectionMode = "shadow_copy"
)

type ProjectionResolution struct {
	Mode      ProjectionMode
	WriteBack bool
}

func ValidateWorkspaceTree(projectRoot string) error {
	rootAbs, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	return filepath.WalkDir(rootAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		targetPath, err := ResolveLinkTarget(path)
		if err != nil {
			return err
		}
		if !pathWithinRoot(rootAbs, targetPath) {
			return ErrArtifactPathEscapesRoot
		}
		_, err = os.Stat(targetPath)
		if err != nil {
			return err
		}
		return nil
	})
}

func ResolveProjectionMode(projectionRoot string, declaredRoots []string, writable bool) (ProjectionResolution, error) {
	rootAbs, err := filepath.Abs(projectionRoot)
	if err != nil {
		return ProjectionResolution{}, err
	}
	allowedRoots := make([]string, 0, len(declaredRoots))
	for _, declaredRoot := range declaredRoots {
		absoluteRoot, err := filepath.Abs(declaredRoot)
		if err != nil {
			return ProjectionResolution{}, err
		}
		allowedRoots = append(allowedRoots, absoluteRoot)
	}
	resolution := ProjectionResolution{
		Mode:      ProjectionModeBind,
		WriteBack: writable,
	}
	err = filepath.WalkDir(rootAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		targetPath, err := ResolveLinkTarget(path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(targetPath); err != nil {
			// Unresolvable symlink (broken target or permission denied): skip for
			// projection mode decision. The symlink passes through as-is in a bind
			// mount (stale cache entries, cross-environment paths, root-owned paths).
			return nil
		}
		if !withinAnyRoot(targetPath, allowedRoots) {
			resolution.Mode = ProjectionModeShadowCopy
			resolution.WriteBack = false
		}
		return nil
	})
	if err != nil {
		return ProjectionResolution{}, err
	}
	return resolution, nil
}

func ResolveLinkTarget(path string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(target) {
		return filepath.Abs(target)
	}
	return filepath.Abs(filepath.Join(filepath.Dir(path), target))
}

func withinAnyRoot(candidate string, allowedRoots []string) bool {
	for _, allowedRoot := range allowedRoots {
		if pathWithinRoot(allowedRoot, candidate) {
			return true
		}
	}
	return false
}
