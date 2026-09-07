package execenv

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// ignoreSet holds compiled .gitignore matchers discovered under a search base,
// keyed by the directory (relative to the base, "." for the base itself) that
// contains each .gitignore file. It backs glob's and grepNative's exclusion of
// gitignored paths without shelling out to git — there is no vendored or
// hand-rolled gitignore parser elsewhere in this repo, and sandboxed sessions
// have no exec capability at all, so a pure-Go matcher is the only thing that
// works uniformly for both the off and sandboxed search paths.
//
// It is a best-effort approximation of git's own precedence: patterns from a
// deeper .gitignore are consulted after (and so can override) patterns from a
// shallower one, but a match found in a shallower directory can only be
// reversed by a negating ("!") pattern within that SAME .gitignore file, not
// by an unrelated file further down — matching git's documented restriction
// that a file excluded by a parent directory's rule generally cannot be
// re-included from below. Patterns from .gitignore files outside the search
// base (e.g. a repo root .gitignore when base is a subdirectory) are not
// consulted; this is a known simplification for a directory-scoped search.
type ignoreSet struct {
	// dirs is ordered root-first so the "deeper overrides shallower" search
	// above is a simple linear scan.
	dirs []ignoreDir
}

type ignoreDir struct {
	rel     string // "." for the base itself, else slash-separated relative dir
	matcher *gitignore.GitIgnore
}

// loadIgnoreSet walks fsys (rooted at the search base) collecting every
// .gitignore file's rules. It is best-effort for I/O errors: unreadable files
// are skipped rather than failing the search, and a base with no .gitignore
// files anywhere (including one that is not inside a git repository at all)
// yields an ignoreSet that matches nothing. The one error it does return is
// budget refusing to let the walk keep going — that has to reach the caller,
// since a partial ignoreSet built from a walk that gave up partway through
// would silently under-exclude the rest of a huge tree.
//
// skip, when non-nil, is consulted for every path (relative to fsys' root,
// slash-separated) before it is descended into or read; skip returning true
// prunes the whole subtree for a directory and skips reading a file. The
// sandboxed caller wires this to its masking check so the walk never lists a
// masked directory's contents or reads a .gitignore inside one — a policy
// this walk otherwise has no other way to honor, since fsys itself (a
// symlink-refusing, root-confined secureDirFS) enforces confinement but not
// masking. The off-path caller (no masking concept) passes a no-op skip.
//
// budget is the same one the caller's glob walk spends listings from, so
// ignore discovery and pattern matching together are bounded as one call's
// worth of work rather than each getting its own unbounded pass over the
// tree.
func loadIgnoreSet(fsys fs.FS, skip func(relPath string) bool, budget *globBudget) (*ignoreSet, error) {
	set := &ignoreSet{}
	var budgetErr error
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A budget refusal is not an unreadable entry: skipping it would
			// let the directory that tripped the bound be read again by
			// whatever walks next, and would leave this set reported as
			// complete when it stopped partway, silently under-excluding the
			// rest of the tree.
			if _, refused := errors.AsType[*globBudgetError](err); refused {
				budgetErr = err
				return err
			}
			return nil //nolint:nilerr // best-effort: skip unreadable entries
		}
		if p != "." && skip != nil && skip(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// Dot-directories (.git, .worktrees, .claude scratch dirs, ...)
			// never hold .gitignore files worth loading and can be enormous
			// (.git) — skip them the same way Glob's own match-filtering
			// does, so this walk doesn't pay to descend into them either.
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			// cycleSafe is always true here: fs.WalkDir never follows a
			// symlink into a directory, so this walk can never re-enter
			// itself the way the pattern walk's /proc/<pid>/root case does.
			// The flag only picks the refusal's wording, not whether the
			// bound applies — an unbounded walk over a huge tree costs
			// unbounded work whether or not it could have looped.
			if err := budget.listing(true); err != nil {
				budgetErr = err
				return err
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}
		data, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return nil //nolint:nilerr // best-effort: skip unreadable .gitignore
		}
		dir := path.Dir(p)
		matcher := gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
		set.dirs = append(set.dirs, ignoreDir{rel: dir, matcher: matcher})
		return nil
	})
	return set, budgetErr
}

// matches reports whether relPath (slash-separated, relative to the search
// base loadIgnoreSet was built from) is excluded by any discovered
// .gitignore. isDir must be true for directory entries: the underlying
// matcher (github.com/sabhiram/go-gitignore) only matches a directory-only
// pattern like "node_modules/" against a path that itself ends in "/" — it
// does not infer directory-ness the way real git does — so a directory entry
// is matched with a trailing slash appended. A nil set (as returned when the
// caller skips loading, e.g. include_ignored) never matches.
func (s *ignoreSet) matches(relPath string, isDir bool) bool {
	if s == nil {
		return false
	}
	ignored := false
	for _, id := range s.dirs {
		var rel string
		switch {
		case id.rel == ".":
			rel = relPath
		case relPath == id.rel:
			rel = "."
		case strings.HasPrefix(relPath, id.rel+"/"):
			rel = strings.TrimPrefix(relPath, id.rel+"/")
		default:
			continue
		}
		if isDir {
			rel += "/"
		}
		if id.matcher.MatchesPath(rel) {
			ignored = true
		}
	}
	return ignored
}

// globMatchIsDir reports whether m (a path relative to fsys, as returned by
// the glob walk) names a directory. doublestar's match strings carry no type
// information of their own, so this stats the entry; a stat failure is treated
// as "not a directory" (best-effort, matching the rest of this file's error
// handling).
//
// A cancelled walk is the exception. Reading the cancellation as "not a
// directory" would quietly turn off every directory-only .gitignore rule and
// hand the caller a plausible list with a nil error, which is the failure mode
// the glob walk itself no longer has — so the cancellation is reported.
func globMatchIsDir(ctx context.Context, fsys fs.FS, m string) (bool, error) {
	info, err := fs.Stat(fsys, m)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		return false, nil
	}
	return info.IsDir(), nil
}

// globMatchExcluded reports whether the default dotfile/gitignore exclusion
// drops the glob match m. Shared by the off and sandboxed glob so the two
// arms exclude — and report a cancellation — identically.
func globMatchExcluded(ctx context.Context, fsys fs.FS, ignores *ignoreSet, m string) (bool, error) {
	if isDotPath(m) {
		return true, nil
	}
	isDir, err := globMatchIsDir(ctx, fsys, m)
	if err != nil {
		return false, err
	}
	return ignores.matches(m, isDir), nil
}

// isDotPath reports whether any path component of relPath (slash-separated)
// starts with "." — the existing convention (matching grepNative's long-
// standing hidden-file skip) for hiding VCS internals, worktree scratch
// dirs (.claude/worktrees/x), and other dotfiles from unscoped search.
func isDotPath(relPath string) bool {
	for part := range strings.SplitSeq(relPath, "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
