package plugins

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// publishingDirName is the private store area where a managed source is
// prepared. It sits under the store root, inside Evener's config mask, so a
// fetch's Git directory is never inside a content root and never granted to a
// session.
const publishingDirName = "publishing"

// isGitName reports whether a path component names Git metadata, in any case.
func isGitName(name string) bool { return strings.EqualFold(name, ".git") }

// publishingDir is the private parent of one plugin's in-flight staging.
func (m *Manager) publishingDir(marketplace, plugin string) string {
	return filepath.Join(m.Root, publishingDirName, marketplace, plugin)
}

// bundledPublishingDir is the private staging parent for bundled payloads. It
// is outside the granted bundled content root so an unaudited payload is never
// visible to a session.
func (m *Manager) bundledPublishingDir() string {
	return filepath.Join(m.Root, publishingDirName, bundledDirName)
}

// auditPluginContent walks a prepared plugin tree and refuses any Git metadata
// or link into it. It does not follow symlinks.
func auditPluginContent(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != dir && isGitName(info.Name()) {
			return fmt.Errorf("plugin content contains Git metadata at %s", relForDiagnostic(dir, path))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			reachesGit, err := symlinkReachesGit(dir, path)
			if err != nil {
				return err
			}
			if reachesGit {
				return fmt.Errorf("plugin content link %s reaches Git metadata", relForDiagnostic(dir, path))
			}
		}
		return nil
	})
}

func symlinkReachesGit(root, path string) (bool, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if targetTouchesGit(root, target) {
		return true, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && targetTouchesGit(resolvedOrSelf(root), resolved), nil
}

func resolvedOrSelf(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// targetTouchesGit reports whether target reaches a .git entry relative to the
// content root. Components shared with sourceRoot are namespace, not metadata:
// a marketplace or plugin may legitimately be named .git, and an ordinary link
// inside it must not be refused because of that ancestor.
func targetTouchesGit(sourceRoot, target string) bool {
	root := filepath.Clean(sourceRoot)
	target = filepath.Clean(target)
	if rel, err := filepath.Rel(root, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return hasGitComponent(rel)
	}
	rootParts := pathComponents(root)
	targetParts := pathComponents(target)
	common := 0
	for common < len(rootParts) && common < len(targetParts) && rootParts[common] == targetParts[common] {
		common++
	}
	if common >= len(targetParts) {
		return false
	}
	return hasGitComponent(strings.Join(targetParts[common:], "/"))
}

func pathComponents(path string) []string {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(filepath.Clean(path), volume)
	return append([]string{volume}, strings.Split(strings.Trim(rest, string(filepath.Separator)), string(filepath.Separator))...)
}

// auditBundledPayload refuses an embedded payload carrying Git metadata before
// it can be renamed into the content root.
func auditBundledPayload(dir string) error {
	return auditPluginContent(dir)
}

func relForDiagnostic(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(path)
	}
	return rel
}

// managedGitArchiveDirName is private storage for metadata moved out of a
// legacy managed content root. It is never collected by GC and is not used at
// runtime.
const managedGitArchiveDirName = "managed-git-archive"

const (
	// publishScratchPrefix names the temporary sibling of a destination used
	// when staging and destination are on different filesystems. The sidecar
	// marker proves this code created it; only a marked, old directory is
	// reclaimed.
	publishScratchPrefix       = ".publish-"
	publishScratchMarkerSuffix = ".publish-owned"
	abandonedPublishScratch    = abandonedStaging
)

// publicationStagingPrefix names the container stagePlugin reserves under
// publishing/<marketplace>/<plugin> for one fetch. A fetch killed before its
// cleanup strands the container, including its Git directory.
const publicationStagingPrefix = ".staging-"

func (m *Manager) managedGitArchiveDir(rootName, rel string) string {
	return filepath.Join(m.Root, managedGitArchiveDirName, rootName, rel)
}

// moveNoReplace renames src to dst, or to a numbered sibling when dst is
// already taken. It never overwrites an archive entry.
func moveNoReplace(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	candidate := dst
	for i := 1; ; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return moveEntry(src, candidate)
		} else if err != nil {
			return err
		}
		candidate = fmt.Sprintf("%s-%d", dst, i)
	}
}

var moveRename = os.Rename

func moveEntry(src, dst string) error {
	err := moveRename(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	if err := copyEntryExact(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	// The archive copy is complete before the content root copy goes away.
	return os.RemoveAll(src)
}

// copyEntryExact copies one directory, file, or symlink for a cross-filesystem
// archive move.
func copyEntryExact(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		return copyTreeExact(src, dst)
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case info.Mode().IsRegular():
		return copyRegularFile(src, dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	default:
		return fmt.Errorf("archivable metadata %s is not a regular file, directory, or symlink", filepath.Base(src))
	}
}

func copyRegularFile(src, dst string, flags int, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, flags, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

// publishStaged renames an audited staging tree into place. When staging and
// the destination are on different filesystems, it copies the tree into a
// temporary sibling of the destination first, so the final rename stays
// atomic. The staged content is already audited, so nothing reachable from the
// destination filesystem before the rename contains Git metadata.
func publishStaged(staging, final string, rename func(string, string) error) error {
	reclaimPublishScratch(filepath.Dir(final))
	err := rename(staging, final)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(final), ".publish-")
	if err != nil {
		return err
	}
	marker := tmp + publishScratchMarkerSuffix
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := copyTreeExact(staging, tmp); err != nil {
		if removeErr := os.RemoveAll(tmp); removeErr == nil {
			_ = os.Remove(marker)
		}
		return err
	}
	if err := rename(tmp, final); err != nil {
		if removeErr := os.RemoveAll(tmp); removeErr == nil {
			_ = os.Remove(marker)
		}
		return err
	}
	_ = os.Remove(marker)
	return nil
}

// reclaimPublishScratch removes old, marked cross-filesystem copies left by a
// termination between copy and rename. Callers hold the publication lock for
// this destination; a concurrent copy is younger than the abandonment age.
func reclaimPublishScratch(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(parent, name)
		if strings.HasSuffix(name, publishScratchMarkerSuffix) {
			dir := strings.TrimSuffix(path, publishScratchMarkerSuffix)
			if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) >= abandonedPublishScratch {
				_ = os.Remove(path)
			}
			continue
		}
		if !strings.HasPrefix(name, publishScratchPrefix) {
			continue
		}
		marker := path + publishScratchMarkerSuffix
		if info, err := os.Stat(marker); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info, err := entry.Info(); err != nil || now.Sub(info.ModTime()) < abandonedPublishScratch {
			continue
		}
		if err := os.RemoveAll(path); err == nil {
			_ = os.Remove(marker)
		}
	}
}

// copyTreeExact copies a prepared tree preserving directory modes, file modes,
// and symlinks. It is used only after the source has passed the content audit.
func copyTreeExact(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyRegularFile(path, target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		default:
			return fmt.Errorf("prepared content %s is not a regular file, directory, or symlink", relForDiagnostic(src, path))
		}
	})
}
