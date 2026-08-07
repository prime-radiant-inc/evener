package execenv

import (
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
// .gitignore file's rules. It is best-effort: unreadable files are skipped
// rather than failing the search, and a base with no .gitignore files
// anywhere (including one that is not inside a git repository at all) yields
// an ignoreSet that matches nothing.
//
// skip, when non-nil, is consulted for every path (relative to fsys' root,
// slash-separated) before it is descended into or read; skip returning true
// prunes the whole subtree for a directory and skips reading a file. The
// sandboxed caller wires this to its masking check so the walk never lists a
// masked directory's contents or reads a .gitignore inside one — a policy
// this walk otherwise has no other way to honor, since fsys itself (a
// symlink-refusing, root-confined secureDirFS) enforces confinement but not
// masking. The off-path caller (no masking concept) passes a no-op skip.
func loadIgnoreSet(fsys fs.FS, skip func(relPath string) bool) *ignoreSet {
	set := &ignoreSet{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
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
	return set
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
// doublestar.Glob) names a directory. doublestar's match strings carry no
// type information of their own, so this stats the entry; a stat failure is
// treated as "not a directory" (best-effort, matching the rest of this file's
// error handling).
func globMatchIsDir(fsys fs.FS, m string) bool {
	info, err := fs.Stat(fsys, m)
	return err == nil && info.IsDir()
}

// isDotPath reports whether any path component of relPath (slash-separated)
// starts with "." — the existing convention (matching grepNative's long-
// standing hidden-file skip) for hiding VCS internals, worktree scratch
// dirs (.claude/worktrees/x), and other dotfiles from unscoped search.
func isDotPath(relPath string) bool {
	for _, part := range strings.Split(relPath, "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
