package control

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	runtimedocker "github.com/1996fanrui/agents-sandbox/internal/runtime/docker"
	ignore "github.com/sabhiram/go-gitignore"
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

// loadGitignore loads and compiles the .gitignore file under sourceRoot.
// Returns nil (not an error) when no .gitignore exists.
func loadGitignore(sourceRoot string) *ignore.GitIgnore {
	gitignorePath := filepath.Join(sourceRoot, ".gitignore")
	gi, err := ignore.CompileIgnoreFile(gitignorePath)
	if err != nil {
		return nil
	}
	return gi
}

// dirContainsExternalSymlink reports whether dir (an absolute path) contains
// at least one symlink whose target resolves outside rootAbs.
func dirContainsExternalSymlink(dir string, rootAbs string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, err := runtimedocker.ResolveLinkTarget(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			if !pathWithinRoot(rootAbs, resolved) {
				return true
			}
		}
		if entry.IsDir() {
			if dirContainsExternalSymlink(filepath.Join(dir, entry.Name()), rootAbs) {
				return true
			}
		}
	}
	return false
}

// buildCopyTar writes a tar archive of sourceRoot to w, preserving symlinks.
// The archive paths are relative so that CopyToContainer extracts them under
// the target directory. The writer is always closed.
//
// Filtering priority for each entry during the walk:
//  1. Exclude patterns — if the entry matches an explicit exclude pattern, skip it
//     (SkipDir for directories). Checked first, so excluded content never triggers
//     any downstream logic.
//  2. Internal symlinks — symlinks whose target resolves within the source tree
//     are preserved as-is in the tar archive.
//  3. External symlinks + .gitignore — symlinks resolving outside the source tree
//     are normally rejected with an error. However, if the source root contains a
//     .gitignore and the entry's path matches a gitignore rule, the entry is
//     silently skipped. For directory-level gitignore rules (e.g. ".generated/"),
//     the entire directory is skipped (SkipDir) when it contains at least one
//     external symlink. External symlinks not covered by .gitignore still cause
//     a hard error.
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

	gi := loadGitignore(sourceRoot)

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

		// For directories matched by .gitignore that contain external symlinks,
		// skip the entire directory to avoid partial copies of gitignored content.
		if entry.IsDir() && gi != nil && gi.MatchesPath(relPath+"/") && dirContainsExternalSymlink(currentPath, rootAbs) {
			return filepath.SkipDir
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return writeTarSymlink(tw, currentPath, relPath, rootAbs, gi)
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

func writeTarSymlink(tw *tar.Writer, sourcePath string, archiveName string, sourceRootAbs string, gi *ignore.GitIgnore) error {
	linkTarget, err := os.Readlink(sourcePath)
	if err != nil {
		return err
	}
	// Reject external symlinks (those resolving outside the source tree),
	// unless the path is covered by .gitignore.
	resolvedTarget, err := runtimedocker.ResolveLinkTarget(sourcePath)
	if err != nil {
		return err
	}
	if !pathWithinRoot(sourceRootAbs, resolvedTarget) {
		if gi != nil && gi.MatchesPath(archiveName) {
			return nil
		}
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
