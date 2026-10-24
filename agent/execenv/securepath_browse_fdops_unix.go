//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// This file holds the fd-anchored browse operations (glob and the native grep
// walk). Their shared, platform-independent matching/formatting core stays in
// securepath_browse.go; securepath_other.go supplies fail-closed stand-ins for
// the operations on platforms without a supported enforcement primitive.

var (
	secureBrowseWalkDir  = fs.WalkDir
	secureBrowseReadFile = grepReadFileCapped
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
	// budget bounds the entries a single ReadDir call may materialize, shared
	// with the rest of the glob or grep call's directory-listing budget.
	budget *GlobBudget
	// ctx is threaded down to readDirChunked the same way boundedDirFS.ctx is
	// on the off-sandbox path; see that field for why.
	ctx context.Context
}

func (f *secureDirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	// Nonblocking + regular-file admission, the same contract readFile enforces:
	// a read-only open of a FIFO entry (which WalkDir reaches as a non-directory
	// and grep then fs.ReadFile's) would otherwise block the walk at open until a
	// writer appears, and a never-ending special file would read without bound.
	// A directory is still admitted — fs.FS Open must serve one.
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	// A regular file whose resolved path sits inside a pseudo root is a
	// stream (it stats regular; its read can block) and must keep the flag, so
	// the capped read's deadline can reach it. The spelled hint is a
	// best-effort path check; admitReadFD also asks the descriptor's own mount,
	// because the anchored root may have been reached through an alias.
	file, err := admitReadFD(fd, name, true, isPseudoFsPath(filepath.Join(f.basePath, name)))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return &grepCtxFile{File: file, ctx: f.ctx}, nil
}

// ReadDir lists name through readDirChunked, the same helper boundedDirFS
// uses on the off-sandbox path, so a huge directory here cannot OOM the walk
// either.
func (f *secureDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	return readDirChunked(f.ctx, df, name, f.budget)
}

func (f *secureDirFS) Stat(name string) (fs.FileInfo, error) {
	// O_NONBLOCK: glob stats every candidate, so a FIFO entry must not block this
	// open. Stat reports one, so no type admission — only the nonblocking open.
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
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
// separate security boundary. budget is shared across every brace-expanded
// pattern the call walks, exactly like the off path's GlobWithBudget.
//
// Dotfiles/dirs and gitignored paths are excluded by default (matching the
// off path's Glob), unless includeIgnored is set.
func (s *sandboxFS) glob(ctx context.Context, tool, base, pattern string, includeIgnored bool, budget *GlobBudget) ([]string, int, error) {
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		return nil, 0, err
	}
	baseFd, canonical, err := s.openReadBaseFd(tool, base)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Close(baseFd) }()

	fsys := cancelFS{ctx: ctx, fsys: &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s, budget: budget, ctx: ctx}}
	var ignores *ignoreSet
	if !includeIgnored {
		// Never list or read into a masked subtree while collecting
		// .gitignore rules — secureDirFS enforces symlink-refusal and root
		// confinement but not masking, so the skip must be supplied here.
		var err error
		ignores, err = loadIgnoreSet(ctx, fsys, func(relPath string) bool {
			return s.underMasked(filepath.Join(canonical, relPath))
		}, budget, ignoreScopeForPatterns(patterns))
		if err != nil {
			return nil, 0, err
		}
	}
	seen := make(map[string]struct{})
	var abs []string
	excluded := 0
	for _, pattern := range patterns {
		matches, err := globMatches(ctx, fsys, pattern, budget)
		if err != nil {
			return nil, 0, err
		}
		for _, m := range matches {
			if !includeIgnored {
				drop, err := globMatchExcluded(ctx, fsys, ignores, m)
				if err != nil {
					return nil, 0, err
				}
				if drop {
					excluded++
					continue
				}
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
	if err := sortPathsByMtimeDesc(ctx, abs); err != nil {
		return nil, 0, err
	}
	return abs, excluded, nil
}

// grepNative runs the native (ripgrep-absent) grep beneath a policy-checked base,
// walking a secureDirFS so symlinks are never followed and refusing to descend
// into masked subtrees. cancelFS makes the walk and file opens observe ctx. The
// per-file matching/formatting is shared with the off path via grepAccum, so
// output semantics are identical. It never searches a file skip names (see
// LocalExecutionEnvironment.GrepSkipping). A base that names a file is
// searched alone (see grepNamedFile).
// outputRoot, when supplied by memory search, changes only result path
// presentation, not the walk's base or selection paths.
func (s *sandboxFS) grepNative(ctx context.Context, pattern, base, globFilter string, caseInsensitive bool, maxResults int, outputMode string, ctxLines int, skip func(rel string) bool, outputRoot ...string) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	a, err := newGrepAccum(pattern, caseInsensitive, maxResults, outputMode, ctxLines)
	if err != nil {
		return "", err
	}
	// The base opens without O_DIRECTORY, so a named file is admitted by the
	// same read policy as a directory; its type then picks the search.
	canonical := filepath.Clean(base)
	if len(outputRoot) > 0 {
		a.pathPrefix, err = filepath.Rel(outputRoot[0], canonical)
		if err != nil {
			return "", err
		}
	}
	baseFd, err := s.openRead("grep", canonical, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return "", err
	}
	var st unix.Stat_t
	if err := unix.Fstat(baseFd, &st); err != nil {
		_ = unix.Close(baseFd)
		return "", err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return grepNamedFile(ctx, baseFd, canonical, a, globFilters, skip)
	}
	defer func() { _ = unix.Close(baseFd) }()

	budget := newGlobBudget("grep")
	fsys := cancelFS{ctx: ctx, fsys: &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s, budget: budget, ctx: ctx}}
	// Never list or read into a masked subtree while collecting .gitignore
	// rules — see the matching comment in glob above.
	ignores, err := loadIgnoreSet(ctx, fsys, func(relPath string) bool {
		abs := filepath.Join(canonical, relPath)
		return s.underMasked(abs) || walkSkipsPseudoDir(canonical, abs)
	}, budget, wholeBaseIgnoreScope())
	if err != nil {
		return "", err
	}
	excludedByIgnore := 0
	oversized := 0
	err = secureBrowseWalkDir(fsys, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if walkErr != nil {
			// loadIgnoreSet's walk above already visits every directory this
			// one would — its skip set (dot-prefixed and masked directories)
			// is a strict subset of this walk's (which also skips gitignored
			// ones) — so it always reaches an oversized directory first on
			// any static tree and this guard cannot be reached that way. It
			// is reachable only when a directory grows past the entry bound
			// in the gap between the two passes, which
			// TestSandboxedGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery
			// forces by stubbing secureBrowseWalkDir itself to grow the tree
			// after ignore discovery has already listed it.
			if _, refused := errors.AsType[*globBudgetError](walkErr); refused {
				return walkErr
			}
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
				if walkSkipsPseudoDir(canonical, abs) {
					return fs.SkipDir
				}
			}
			// fs.WalkDir never follows a directory symlink, so this walk
			// cannot cycle back into itself the way the glob pattern walk's
			// ancestor check has to guard against — the same reason ignore
			// discovery's own budget charge above is always cycleSafe.
			// Charged only here, once the masked/dot/gitignore skips above
			// have already passed, so a directory the walk is about to skip
			// anyway costs nothing.
			if berr := budget.listing(true); berr != nil {
				return berr
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
		if selected, selErr := grepFileSelected(d.Name(), rel, globFilters, skip); !selected {
			return selErr
		}
		data, rerr := secureBrowseReadFile(fsys, rel)
		if rerr != nil {
			if cancelErr := ctx.Err(); cancelErr != nil {
				return cancelErr
			}
			if errors.Is(rerr, errGrepFileTooLarge) {
				oversized++
			}
			return nil //nolint:nilerr // best-effort grep: skip unreadable files
		}
		if a.feed(rel, data) {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return a.finishWalk(excludedByIgnore, oversized), nil
}

// grepNamedFile searches the one file a grep's path names, given its
// nonblocking fd (which it takes ownership of), under the rules the walk
// applies to each file it reaches: dotfiles, files grepFileSelected leaves
// out, non-regular files, and binary files (grepAccum.feed) are skipped. Its
// output path is ".", the unsandboxed walk's name for a named file.
func grepNamedFile(ctx context.Context, fd int, path string, a *grepAccum, globFilters []string, skip func(rel string) bool) (string, error) {
	f, err := admitReadFD(fd, path, false, isPseudoFsPath(path))
	if err != nil {
		return "", nil //nolint:nilerr // best-effort grep: a non-regular file is skipped, as in the walk
	}
	defer func() { _ = f.Close() }()
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") {
		return "", nil
	}
	if selected, err := grepFileSelected(name, ".", globFilters, skip); !selected {
		return "", err
	}
	data, err := grepCappedRead(&grepCtxFile{File: f, ctx: ctx})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return "", cancelErr
	}
	if errors.Is(err, errGrepFileTooLarge) {
		// A file named as the target is a deliberate request: search the
		// over-cap regular behind it in bounded windows. admitReadFD admits
		// only regular files here, so the target cannot be a device stream.
		if _, serr := a.feedLargeFile(ctx, f); serr != nil {
			// A cancellation that landed mid-scan is the search's error, not
			// an empty success; anything else is the unreadable-file skip.
			if cancelErr := ctx.Err(); cancelErr != nil {
				return "", cancelErr
			}
			return "", nil //nolint:nilerr // best-effort grep: a stalled or failed stream is skipped, like an unreadable file
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return "", cancelErr
		}
		return a.finish(), nil
	}
	if err != nil {
		return "", nil //nolint:nilerr // best-effort grep: an unreadable file is skipped, as in the walk
	}
	a.feed(".", data)
	return a.finish(), nil
}
