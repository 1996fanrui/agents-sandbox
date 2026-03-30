package docker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

var ErrProjectionTargetUnreadable = errors.New("projection target is unreadable")

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
