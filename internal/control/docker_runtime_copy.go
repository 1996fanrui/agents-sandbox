package control

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
)

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

// buildCopyTar writes a tar archive of sourceRoot to w, applying exclude patterns
// and preserving symlinks. The archive paths are relative so that CopyToContainer
// extracts them under the target directory. The writer is always closed.
func buildCopyTar(w io.WriteCloser, sourceRoot string, excludePatterns []string) error {
	tw := tar.NewWriter(w)
	closeAll := func(tarErr error) error {
		twErr := tw.Close()
		wErr := w.Close()
		if tarErr != nil {
			return tarErr
		}
		if twErr != nil {
			return twErr
		}
		return wErr
	}

	rootAbs, err := filepath.Abs(sourceRoot)
	if err != nil {
		return closeAll(err)
	}

	sourceInfo, err := os.Lstat(sourceRoot)
	if err != nil {
		return closeAll(err)
	}
	if !sourceInfo.IsDir() {
		// Single file: write it as the base name.
		err := writeTarFile(tw, sourceRoot, sourceInfo.Name(), sourceInfo)
		return closeAll(err)
	}

	err = filepath.WalkDir(sourceRoot, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(sourceRoot, currentPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		if matchesExcludePattern(relPath, excludePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return writeTarSymlink(tw, currentPath, relPath, rootAbs)
		}
		if entry.IsDir() {
			header, headerErr := tar.FileInfoHeader(info, "")
			if headerErr != nil {
				return headerErr
			}
			header.Name = relPath + "/"
			return tw.WriteHeader(header)
		}
		return writeTarFile(tw, currentPath, relPath, info)
	})
	return closeAll(err)
}

func writeTarFile(tw *tar.Writer, sourcePath string, archiveName string, info fs.FileInfo) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = archiveName
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func writeTarSymlink(tw *tar.Writer, sourcePath string, archiveName string, sourceRootAbs string) error {
	linkTarget, err := os.Readlink(sourcePath)
	if err != nil {
		return err
	}
	// Reject external symlinks (those resolving outside the source tree).
	resolvedTarget, err := runtimedocker.ResolveLinkTarget(sourcePath)
	if err != nil {
		return err
	}
	if !pathWithinRoot(sourceRootAbs, resolvedTarget) {
		return fmt.Errorf("copy source contains external symlink: %s", sourcePath)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return err
	}
	header.Name = archiveName
	header.Linkname = linkTarget
	return tw.WriteHeader(header)
}
