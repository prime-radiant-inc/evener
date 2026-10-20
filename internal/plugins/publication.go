package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
			if info.IsDir() {
				return fmt.Errorf("plugin content contains Git metadata at %s", relForDiagnostic(dir, path))
			}
			return fmt.Errorf("plugin content contains Git metadata at %s", relForDiagnostic(dir, path))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			lexical := target
			if !filepath.IsAbs(lexical) {
				lexical = filepath.Join(filepath.Dir(path), lexical)
			}
			if hasGitComponent(lexical) {
				return fmt.Errorf("plugin content link %s reaches Git metadata", relForDiagnostic(dir, path))
			}
			if resolved, err := filepath.EvalSymlinks(path); err == nil && hasGitComponent(resolved) {
				return fmt.Errorf("plugin content link %s reaches Git metadata", relForDiagnostic(dir, path))
			}
		}
		return nil
	})
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
			return os.Rename(src, candidate)
		} else if err != nil {
			return err
		}
		candidate = fmt.Sprintf("%s-%d", dst, i)
	}
}
