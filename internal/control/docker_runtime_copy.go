package control

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
)

func copyTree(sourceRoot string, targetRoot string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, nil, false)
}

func copyTreeAllowExternalSymlinks(sourceRoot string, targetRoot string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, nil, true)
}

func copyTreeWithPatterns(sourceRoot string, targetRoot string, excludePatterns []string) error {
	return copyTreeWithOptions(sourceRoot, targetRoot, excludePatterns, false)
}

func copyTreeWithOptions(sourceRoot string, targetRoot string, excludePatterns []string, allowExternalSymlinks bool) error {
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
			target, copyResolved, resolvedTarget, err := rewriteCopiedSymlink(rootAbs, targetRoot, currentSource, currentTarget, allowExternalSymlinks)
			if err != nil {
				return err
			}
			if copyResolved {
				resolvedInfo, err := os.Stat(resolvedTarget)
				if err != nil {
					return err
				}
				if resolvedInfo.IsDir() {
					return copyTreeWithOptions(resolvedTarget, currentTarget, nil, allowExternalSymlinks)
				}
				return copyFile(resolvedTarget, currentTarget, resolvedInfo.Mode())
			}
			if err := os.MkdirAll(filepath.Dir(currentTarget), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, currentTarget)
		}
		return copyFile(currentSource, currentTarget, info.Mode())
	})
}

func rewriteCopiedSymlink(
	sourceRoot string,
	targetRoot string,
	currentSource string,
	currentTarget string,
	allowExternalSymlinks bool,
) (string, bool, string, error) {
	target, err := os.Readlink(currentSource)
	if err != nil {
		return "", false, "", err
	}
	resolvedTarget, err := runtimedocker.ResolveLinkTarget(currentSource)
	if err != nil {
		return "", false, "", err
	}
	if !pathWithinRoot(sourceRoot, resolvedTarget) {
		if !allowExternalSymlinks {
			return "", false, "", fmt.Errorf("copy source contains external symlink: %s", currentSource)
		}
		return "", true, resolvedTarget, nil
	}
	if filepath.IsAbs(target) {
		relativeTarget, err := filepath.Rel(sourceRoot, resolvedTarget)
		if err != nil {
			return "", false, "", err
		}
		rewrittenTarget, err := filepath.Rel(filepath.Dir(currentTarget), filepath.Join(targetRoot, relativeTarget))
		return rewrittenTarget, false, "", err
	}
	return target, false, "", nil
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
