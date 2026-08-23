package execenv

import (
	"context"
	"fmt"
	"io/fs"
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

// globMatches runs one expanded glob pattern against fsys, which must already
// observe ctx (see cancelFS).
//
// WithNoFollow keeps `**` traversal from descending through directory
// symlinks. Symlinks still match; they are just never walked into, because a
// directory symlink can re-enter the tree it lives in — /proc/<pid>/root
// points back at / — and a `**` walk that follows one has no reason to stop.
// The sandboxed walk already refuses symlink traversal outright, so this also
// brings the two paths into agreement.
func globMatches(ctx context.Context, fsys fs.FS, pattern string) ([]string, error) {
	matches, err := doublestar.Glob(fsys, pattern, doublestar.WithNoFollow())
	// doublestar reports I/O errors only under WithFailOnIOErrors, which we
	// don't want (an unreadable directory shouldn't fail the whole glob). So a
	// cancelled walk surfaces here as a truncated result with a nil error;
	// answer with the cancellation instead of a plausible-looking short list.
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
