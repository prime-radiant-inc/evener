package execenv

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"primeradiant.com/evener/agent/internal/globpattern"
)

func expandSearchPattern(pattern string) ([]string, error) {
	expanded, err := globpattern.Expand(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	return expanded, nil
}

// cancelFS wraps an fs.FS so a walk over it observes ctx. doublestar has no
// context-aware entry point and reaches the filesystem only through Open,
// ReadDir and Stat, so failing those three the moment ctx is done is what
// makes an in-flight glob abortable — without it a `**` walk over a large
// tree runs to completion no matter who asks it to stop.
type cancelFS struct {
	ctx  context.Context
	fsys fs.FS
}

func (c cancelFS) Open(name string) (fs.File, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.fsys.Open(name)
}

func (c cancelFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return fs.ReadDir(c.fsys, name)
}

func (c cancelFS) Stat(name string) (fs.FileInfo, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return fs.Stat(c.fsys, name)
}

// maxUnidentifiedGlobDirs bounds a walk over a filesystem whose directories
// carry no file identity, where globWalkFS's cycle check cannot work. The
// bound counts directories listed rather than capping depth because a symlink
// cycle costs unbounded *work*, not merely unbounded depth: one
// /proc/<pid>/root hop re-enters the entire tree, so a shallow depth cap would
// still admit a combinatorial re-walk while a deep one would truncate real
// results. os-backed filesystems never reach this — the cycle check bounds
// them — so exceeding it means the walk has lost its footing, and it is
// reported as an error rather than as a short list.
const maxUnidentifiedGlobDirs = 100_000

// globWalkFS is the view of the filesystem a single doublestar walk runs over.
// It supplies the two properties doublestar itself cannot:
//
// Termination. It refuses to list a directory that is the same file as one of
// its own ancestors on this walk. That is the shape /proc/<pid>/root has —
// following the link re-enters the tree it lives in — and it is why a `**`
// glob rooted at / never returned (#369). Everything under such a directory is
// already reachable at a shorter path, so refusing costs no matches. A
// directory symlink pointing anywhere other than at its own ancestors
// (node_modules/lib -> ../packages/lib, or /etc -> /private/etc on macOS) is
// walked normally and its files match under both names.
//
// Abort. doublestar propagates a filesystem error only under
// WithFailOnIOErrors; without it a cancelled walk keeps grinding through every
// sibling its parent frames already listed. So the walk runs with that option
// and this wrapper reports every failure other than the cancellation as
// fs.ErrNotExist — the "this entry simply isn't there" answer doublestar
// already skips over — which leaves cancellation as the only error that can
// end a walk. An unreadable directory still does not fail the glob.
type globWalkFS struct {
	fs.FS
	ctx context.Context
	// listed holds the identity of every directory already listed, keyed by
	// the path it was listed under, so the ancestor check costs a map lookup
	// per level instead of a stat per level.
	listed map[string]fs.FileInfo
	// unidentified counts listed directories whose FileInfo carries no file
	// identity — the only case maxUnidentifiedGlobDirs bounds.
	unidentified int
}

func (w *globWalkFS) Stat(name string) (fs.FileInfo, error) {
	info, err := fs.Stat(w.FS, name)
	if err != nil {
		return nil, w.hide("stat", name)
	}
	return info, nil
}

func (w *globWalkFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := w.admit(name); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(w.FS, name)
	if err != nil {
		return nil, w.hide("readdir", name)
	}
	return entries, nil
}

// admit reports whether the walk may list name, recording its identity so the
// directories below it can be checked against it.
func (w *globWalkFS) admit(name string) error {
	info, err := fs.Stat(w.FS, name)
	if err != nil {
		return w.hide("stat", name)
	}
	if !hasFileIdentity(info) {
		w.unidentified++
		if w.unidentified > maxUnidentifiedGlobDirs {
			return fmt.Errorf("glob walk made %d directory listings on a filesystem that reports no file identity, so a symlink cycle cannot be detected: refusing to keep walking", w.unidentified)
		}
		return nil
	}
	// The walk root is skipped: it has no ancestors to cycle back to, and
	// path.Dir(".") is "." — scanning from there would find the root's own
	// entry and refuse the second listing doublestar makes of every directory.
	if name != "." {
		for dir := path.Dir(name); ; dir = path.Dir(dir) {
			if ancestor, ok := w.listed[dir]; ok && os.SameFile(ancestor, info) {
				return w.hide("readdir", name)
			}
			if dir == "." {
				break
			}
		}
	}
	w.listed[name] = info
	return nil
}

// hide answers with the walk's cancellation when there is one — the walk runs
// under WithFailOnIOErrors so that ends it immediately — and otherwise with
// "not there", which doublestar skips past. See the type comment.
func (w *globWalkFS) hide(op, name string) error {
	if cerr := w.ctx.Err(); cerr != nil {
		return cerr
	}
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
}

// hasFileIdentity reports whether info carries the (device, inode) identity
// os.SameFile compares. os.SameFile answers false for any FileInfo that did
// not come from the os package — an fstest.MapFS entry, say — so comparing an
// info against itself is how to tell "a different file" from "cannot tell".
func hasFileIdentity(info fs.FileInfo) bool {
	return os.SameFile(info, info)
}

// globMatches runs one expanded glob pattern against fsys, which must already
// observe ctx (see cancelFS), through a fresh globWalkFS — fresh per pattern,
// because the directories one pattern listed say nothing about whether another
// pattern's walk is re-entering itself.
//
// GlobWalk rather than Glob: it ends the walk the moment the callback returns
// an error, so a cancellation that lands between two filesystem calls stops
// the walk at the next match instead of being noticed only after the tree runs
// out.
func globMatches(ctx context.Context, fsys fs.FS, pattern string) ([]string, error) {
	walk := &globWalkFS{FS: fsys, ctx: ctx, listed: map[string]fs.FileInfo{}}
	var matches []string
	err := doublestar.GlobWalk(walk, pattern, func(p string, _ fs.DirEntry) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		matches = append(matches, p)
		return nil
	}, doublestar.WithFailOnIOErrors())
	// A cancelled walk must not come back as a plausible-looking short list,
	// so answer with the cancellation however the walk happened to unwind.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func expandGrepFilter(filter string) ([]string, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, nil
	}
	return expandSearchPattern(filter)
}

func matchesAnyGrepFilter(name string, filters []string) (bool, error) {
	for _, filter := range filters {
		matched, err := filepath.Match(filter, name)
		if err != nil {
			// Preserve the existing grep behavior for malformed [] patterns:
			// brace syntax is validated by Expand, while filepath.Match errors
			// simply mean that this filter cannot match this filename.
			continue
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
