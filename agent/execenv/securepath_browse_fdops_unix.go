//go:build linux || darwin

package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/sys/unix"
)

// This file holds the fd-anchored browse operations (glob and the native grep
// walk). Their shared, platform-independent matching/formatting core stays in
// securepath_browse.go; securepath_other.go supplies fail-closed stand-ins for
// the operations on platforms without a supported enforcement primitive.

var (
	secureBrowseWalkDir  = fs.WalkDir
	secureBrowseReadFile = fs.ReadFile
)

// secureDirFS is an fs.FS rooted at a base directory fd that refuses symlink
// traversal and cannot escape the base: every Open/ReadDir/Stat resolves beneath
// baseFd with the same symlink-refusing primitive the file tools use. It backs
// glob and the native grep walk so neither can be steered out of policy by a
// symlinked entry mid-walk. basePath is the canonical base, used to skip masked
// subtrees.
type secureDirFS struct {
	baseFd   int
	basePath string
	fs       *sandboxFS
}

func (f *secureDirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (f *secureDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	return df.ReadDir(-1)
}

func (f *secureDirFS) Stat(name string) (fs.FileInfo, error) {
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: toFsErr(err)}
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	return df.Stat()
}

// toFsErr maps a symlink-refusal or root-escape from the resolver to
// fs.ErrNotExist so glob/WalkDir treat a symlinked/escaping entry as absent
// (invisible) rather than surfacing a raw errno.
func toFsErr(err error) error {
	switch {
	case errors.Is(err, errSymlinkComponent), errors.Is(err, errEscapesRoot),
		errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return fs.ErrNotExist
	default:
		return err
	}
}

// glob resolves matches for pattern beneath a policy-checked base directory,
// refusing symlink traversal (so a pattern cannot escape through a symlink) and
// dropping any match under a masked path. Results are absolute and sorted like the
// off path (newest mtime first, ties by path). The returned int is the number of
// candidate matches dropped by the dotfile/gitignore exclusion (see
// GlobExcluder) — it never includes matches dropped by masking, which is a
// separate security boundary.
//
// Dotfiles/dirs and gitignored paths are excluded by default (matching the
// off path's Glob), unless includeIgnored is set.
func (s *sandboxFS) glob(tool, base, pattern string, includeIgnored bool) ([]string, int, error) {
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		return nil, 0, err
	}
	baseFd, canonical, err := s.openReadBaseFd(tool, base)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Close(baseFd) }()

	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s}
	var ignores *ignoreSet
	if !includeIgnored {
		// Never list or read into a masked subtree while collecting
		// .gitignore rules — secureDirFS enforces symlink-refusal and root
		// confinement but not masking, so the skip must be supplied here.
		ignores = loadIgnoreSet(fsys, func(relPath string) bool {
			return s.underMasked(filepath.Join(canonical, relPath))
		})
	}
	seen := make(map[string]struct{})
	var abs []string
	excluded := 0
	for _, pattern := range patterns {
		matches, err := doublestar.Glob(fsys, pattern)
		if err != nil {
			return nil, 0, err
		}
		for _, m := range matches {
			if !includeIgnored && (isDotPath(m) || ignores.matches(m, globMatchIsDir(fsys, m))) {
				excluded++
				continue
			}
			p := filepath.Join(canonical, m)
			if s.underMasked(p) {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			abs = append(abs, p)
		}
	}
	sortPathsByMtimeDesc(abs)
	return abs, excluded, nil
}

// grepNative runs the native (ripgrep-absent) grep beneath a policy-checked base,
// walking a secureDirFS so symlinks are never followed and refusing to descend
// into masked subtrees. The per-file matching/formatting is shared with the off
// path via grepAccum, so output semantics are identical.
func (s *sandboxFS) grepNative(pattern, base, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	baseFd, canonical, err := s.openReadBaseFd("grep", base)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(baseFd) }()

	ctxLines := 0
	if len(contextLines) > 0 && contextLines[0] > 0 {
		ctxLines = contextLines[0]
	}
	a, err := newGrepAccum(pattern, caseInsensitive, maxResults, outputMode, ctxLines)
	if err != nil {
		return "", err
	}
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s}
	// Never list or read into a masked subtree while collecting .gitignore
	// rules — see the matching comment in glob above.
	ignores := loadIgnoreSet(fsys, func(relPath string) bool {
		return s.underMasked(filepath.Join(canonical, relPath))
	})
	excludedByIgnore := 0
	err = secureBrowseWalkDir(fsys, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip unreadable / symlink-refused entries and keep walking
		}
		abs := filepath.Join(canonical, rel)
		if s.underMasked(abs) {
			if d.IsDir() {
				return fs.SkipDir // never descend into a masked subtree
			}
			return nil
		}
		if d.IsDir() {
			if rel != "." {
				if strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				if ignores.matches(rel, true) {
					excludedByIgnore++
					return fs.SkipDir
				}
			}
			return nil
		}
		// Skip hidden and gitignored files
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if ignores.matches(rel, false) {
			excludedByIgnore++
			return nil
		}
		if len(globFilters) > 0 {
			matched, matchErr := matchesAnyGrepFilter(d.Name(), globFilters)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		data, rerr := secureBrowseReadFile(fsys, rel)
		if rerr != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable files
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		if a.feed(rel, data) {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	result := a.finish()
	// Silent-empty is the enemy (D2): distinguish "genuinely no matches" from
	// "no matches among the files searched, but N were skipped by the
	// default dotfile/gitignore exclusion" — grep has no include_ignored
	// knob, so this is informational rather than a suggestion to retry.
	if result == "" && excludedByIgnore > 0 {
		return fmt.Sprintf("0 matches; %d dotfile/gitignored path(s) were excluded from the search", excludedByIgnore), nil
	}
	return result, nil
}
