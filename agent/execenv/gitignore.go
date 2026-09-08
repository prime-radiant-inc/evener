package execenv

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
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
	// dirs is in collection order, which is root-first only for a
	// single-prefix scope: a call scoped to several prefixes appends each
	// prefix's ancestors and subtree in turn, so {a,b}/*.txt yields ".", "b",
	// "b/c", "a". Nothing depends on the order today, because matches ORs
	// every rule that applies and never resets. Anything that later gives a
	// deeper rule precedence over a shallower one has to sort first rather
	// than rely on this slice.
	dirs []ignoreDir
}

type ignoreDir struct {
	rel     string // "." for the base itself, else slash-separated relative dir
	matcher *gitignore.GitIgnore
}

// ignoreScope is one region of the base discovery has to inspect: a literal
// prefix, and how many directory levels below it can hold a .gitignore whose
// rules reach one of the caller's candidates. depth -1 means unbounded.
type ignoreScope struct {
	prefix string
	depth  int
	// walk is false when the pattern's remainder holds no glob metacharacter,
	// so it names literal paths the glob resolves with a stat rather than by
	// listing anything. Discovery then reads the prefix's own .gitignore
	// directly and never lists it, because listing a large literal directory
	// is work the pattern's own walk would never do.
	walk bool
}

// wholeBaseIgnoreScope is the scope a caller with no pattern to narrow by
// needs: the whole base, to any depth. Grep uses it, because its own walk
// covers the whole base too.
func wholeBaseIgnoreScope() []ignoreScope {
	return []ignoreScope{{prefix: ".", depth: -1, walk: true}}
}

// ignoreScopeDepth reports how many directory levels below a pattern's
// literal prefix can hold a rule affecting one of its candidates. Only ** can
// match a directory separator, so without one every remaining separator in
// the pattern is exactly one level and the reach is bounded; with one it is
// unbounded (-1). This is what keeps a non-recursive pattern like "*.go" from
// inspecting a subtree its own walk never lists.
func ignoreScopeDepth(rest string) int {
	if ignorePatternIsRecursive(rest) {
		return -1
	}
	return strings.Count(rest, "/")
}

// ignorePatternIsRecursive reports whether pattern can match across a
// separator, which doublestar allows only for a ** that is a whole path
// component: its matcher special-cases "**", "/**", "**/" and "/**/" and
// nothing else, so "foo**bar" behaves like a single star. Comparing each
// slash-separated component against the literal two-character "**" follows
// that rule exactly, and it is why an escaped "\*\*" and a character class
// like "[**]" are not recursive either: neither component's raw text is "**".
//
// Splitting on every slash cannot hide a real ** component, since a standalone
// one is always bounded by separators or the ends of the pattern. The only
// patterns it can misread are ones with an escaped separator, and those it
// misreads as recursive, which costs a wider scope rather than a missing rule.
func ignorePatternIsRecursive(pattern string) bool {
	for component := range strings.SplitSeq(pattern, "/") {
		if component == "**" {
			return true
		}
	}
	return false
}

// ignorePatternHasMeta reports whether pattern can match more than one exact
// path. Without a metacharacter the glob resolves it by stat'ing the path it
// spells, so nothing is ever listed and discovery must not list either; an
// escaped metacharacter is a literal and does not count.
func ignorePatternHasMeta(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '*', '?', '[', '{':
			return true
		}
	}
	return false
}

// ignoreScopeRelDepth reports how many levels below prefix p sits, prefix
// itself being 0.
func ignoreScopeRelDepth(prefix, p string) int {
	if p == prefix {
		return 0
	}
	rel := p
	if prefix != "." {
		rel = strings.TrimPrefix(p, prefix+"/")
	}
	return strings.Count(rel, "/") + 1
}

// narrowIgnoreScopes drops the scopes another already covers, so no directory
// is walked or read twice: an unbounded scope over the whole base subsumes
// every other, and an unbounded scope subsumes anything at or beneath its own
// prefix. Scopes sharing a prefix collapse to the deepest reach among them.
func narrowIgnoreScopes(scopes []ignoreScope) []ignoreScope {
	deepest := make(map[string]int, len(scopes))
	walks := make(map[string]bool, len(scopes))
	order := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if sc.prefix == "." && sc.depth < 0 && sc.walk {
			return wholeBaseIgnoreScope()
		}
		if d, seen := deepest[sc.prefix]; !seen {
			deepest[sc.prefix] = sc.depth
			order = append(order, sc.prefix)
		} else if d >= 0 && (sc.depth < 0 || sc.depth > d) {
			deepest[sc.prefix] = sc.depth
		}
		// One pattern needing the subtree is enough to need it: a literal
		// sibling pattern over the same prefix asks for strictly less.
		if sc.walk {
			walks[sc.prefix] = true
		}
	}
	kept := make([]ignoreScope, 0, len(order))
	for _, prefix := range order {
		covered := false
		for _, other := range order {
			if other == prefix {
				continue
			}
			if other != "." && !strings.HasPrefix(prefix, other+"/") {
				continue
			}
			// An unbounded ancestor covers anything beneath it. A bounded one
			// covers it only when its own reach extends at least as far: the
			// levels down to this prefix, plus this prefix's own reach, have to
			// fit inside the ancestor's. Otherwise both are kept, since neither
			// walk subsumes the other.
			if !walks[other] {
				continue
			}
			if deepest[other] < 0 || (!walks[prefix] && deepest[other] >= ignoreScopeRelDepth(other, prefix)) ||
				(walks[prefix] && deepest[prefix] >= 0 &&
					ignoreScopeRelDepth(other, prefix)+deepest[prefix] <= deepest[other]) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, ignoreScope{prefix: prefix, depth: deepest[prefix], walk: walks[prefix]})
		}
	}
	return kept
}

// ignoreAncestors returns prefix's strict ancestor directories, root first
// ("." through prefix's parent): the levels a rule affecting a path under
// prefix could live in that the budgeted subtree walk below never visits,
// since that walk starts at prefix itself. "." has none: it is the base, so
// there is nothing above it to read.
func ignoreAncestors(prefix string) []string {
	if prefix == "." {
		return nil
	}
	parts := strings.Split(prefix, "/")
	dirs := make([]string, 0, len(parts))
	dirs = append(dirs, ".")
	for i := 1; i < len(parts); i++ {
		dirs = append(dirs, strings.Join(parts[:i], "/"))
	}
	return dirs
}

// ignoreScopeForPatterns builds a glob call's ignore-discovery scope from its
// expanded patterns: doublestar.SplitPattern's meta-free leading directory for
// each one, so discovery is scoped to that prefix's subtree instead of the
// whole base. That is the subtree, not the pattern's own reach: a directory
// inside the prefix is still walked, and can still refuse, even where the
// pattern would never have matched inside it — "sub/*.txt" beside a large
// "sub/huge/" can fail on sub/huge. Narrowing further would mean deciding
// which descendants a pattern can match before walking them, which is the
// walk's own job.
// A pattern starting with a metacharacter (e.g. "**/*.go") splits to ".",
// scoping discovery to the whole base — correct, since such a pattern's own
// walk covers the whole base too, so the budget still applies to both
// consistently.
func ignoreScopeForPatterns(patterns []string) []ignoreScope {
	scopes := make([]ignoreScope, len(patterns))
	for i, pattern := range patterns {
		prefix, rest := doublestar.SplitPattern(pattern)
		scopes[i] = ignoreScope{prefix: prefix, depth: ignoreScopeDepth(rest), walk: ignorePatternHasMeta(rest)}
	}
	return scopes
}

// readIgnoreFile reads one .gitignore under the call's byte budgets. It stops
// one byte past the per-file cap rather than reading the whole file, so an
// enormous rules file is refused without first being materialized, and it
// charges what it keeps against the call-wide retention budget because the
// compiled matcher lives as long as the call does.
//
// It answers three ways: a refusal, which the caller propagates; nil data
// with no error for a file that is missing or unreadable, which the caller
// skips as it always has; and the bytes otherwise.
func readIgnoreFile(ctx context.Context, fsys fs.FS, path string, budget *GlobBudget) ([]byte, error) {
	f, err := fsys.Open(path)
	if err != nil {
		// A cancelled open is not an unreadable .gitignore: swallowing it
		// here would let the discovery walk keep traversing after
		// cancellation and report whatever partial rule set it assembled.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, nil //nolint:nilerr // best-effort: skip a missing or unreadable .gitignore
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxGlobIgnoreFileBytes)+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, nil //nolint:nilerr // best-effort: skip an unreadable .gitignore
	}
	if berr := budget.tooManyRuleBytes(path, len(data)); berr != nil {
		return nil, berr
	}
	if berr := budget.retainRuleBytes(len(data)); berr != nil {
		return nil, berr
	}
	return data, nil
}

// loadIgnoreSet collects every .gitignore rule that can affect a path under
// one of scope's prefixes ("." meaning the whole base). It is best-effort for
// I/O errors: unreadable files are skipped rather than failing the search,
// and a base with no .gitignore files anywhere (including one that is not
// inside a git repository at all) yields an ignoreSet that matches nothing.
// The errors it does return are budget refusals, of every kind: the call's
// listing-count budget, a single directory's entry cap, and the call-wide
// live-entries ceiling all have to reach the caller, since a partial
// ignoreSet built from a walk that gave up partway through would silently
// under-exclude the rest of a huge tree.
//
// A rule collected in directory id.rel only ever affects a path equal to or
// under id.rel (see ignoreSet.matches), so for each of scope's prefixes this
// reads two disjoint halves: the prefix's strict ancestors, one fs.ReadFile
// per level and no directory listing at all, then the prefix's own subtree
// via fs.WalkDir, budgeted exactly like a walk rooted at the base itself
// would be. A prefix that names a directory absent from fsys makes its
// WalkDir a no-op rather than a failure: that prefix simply contributes no
// rules, the same as a literal glob pattern naming a directory that isn't
// there matches nothing.
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
func loadIgnoreSet(ctx context.Context, fsys fs.FS, skip func(relPath string) bool, budget *GlobBudget, scope []ignoreScope) (*ignoreSet, error) {
	set := &ignoreSet{}
	scopes := narrowIgnoreScopes(scope)

	// loadedRules keys every .gitignore this call has already taken, so
	// partially overlapping scopes cannot read one twice: a duplicate would
	// compile a second matcher, retain it, and charge its bytes again.
	loadedRules := make(map[string]bool)
	for _, sc := range scopes {
		dirs := ignoreAncestors(sc.prefix)
		if !sc.walk {
			// Nothing lists this prefix, so its own rules file has to be read
			// here or not at all — and it does affect the literal paths the
			// pattern names, which sit directly inside it.
			dirs = append(dirs, sc.prefix)
		}
		for _, dir := range dirs {
			// Dot-directories are skipped the same way the subtree walk skips
			// them: isDotPath drops every candidate underneath one before a
			// rule from it could apply, so reading it could not change an
			// answer and would spend the rules budget for nothing. The check
			// is on the full path, not the basename: a scope like
			// a/.config/sub sits beneath a dot-directory without naming one
			// itself, and reading under it is just as pointless.
			if dir != "." && isDotPath(dir) {
				continue
			}
			if dir != "." && skip != nil && skip(dir) {
				continue
			}
			p := ".gitignore"
			if dir != "." {
				p = dir + "/.gitignore"
			}
			if loadedRules[p] {
				continue
			}
			loadedRules[p] = true
			// Masking is per path, so an unmasked directory can still hold a
			// masked .gitignore, and the base itself is never masked while a
			// .gitignore directly inside it can be. secureDirFS enforces
			// symlink refusal and root confinement but not masking, so the
			// file has to clear skip on its own before it is read — checking
			// only the directory would read a file the policy hides. The
			// subtree walk below gets this for free: fs.WalkDir hands it every
			// entry, files included, and its skip check runs before the read.
			if skip != nil && skip(p) {
				continue
			}
			if cerr := ctx.Err(); cerr != nil {
				return set, cerr
			}
			data, berr := readIgnoreFile(ctx, fsys, p, budget)
			if berr != nil {
				return set, berr
			}
			if data == nil {
				continue
			}
			matcher := gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
			set.dirs = append(set.dirs, ignoreDir{rel: dir, matcher: matcher})
		}
	}

	var budgetErr error
	for _, sc := range scopes {
		if !sc.walk {
			// The pattern names literal paths; its prefix was read above and
			// listing it would be work the glob itself never does.
			continue
		}
		// A scope beneath a dot-directory has no candidates to collect for:
		// isDotPath drops every path under one before a rule from inside
		// could apply, so walking it only spends the listing and rules
		// budget — and a large enough subtree under it can refuse a glob
		// whose answer is already fixed. The walk's own d.Name() check cannot
		// see this: it only skips dot-named entries, never a scope already
		// rooted under one.
		if isDotPath(sc.prefix) {
			continue
		}
		// Each scope's walk is its own traversal, so it starts holding
		// nothing: a listing the previous scope's walk was still holding when
		// it finished is not held any more, and counting it here would refuse
		// on memory nothing occupies. The cumulative counters stay call-wide.
		budget.resetLive()
		_ = fs.WalkDir(fsys, sc.prefix, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// A cancellation is not an unreadable entry either: skipping
				// it would let the walk keep traversing after cancellation
				// and report whatever partial rule set it assembled. The
				// ctx check comes first so a cancelled walk that also hit
				// the budget reports the cancellation the caller asked for.
				if cerr := ctx.Err(); cerr != nil {
					budgetErr = cerr
					return cerr
				}
				// A budget refusal is not an unreadable entry: skipping it would
				// let the directory that tripped the bound be read again by
				// whatever walks next, and would leave this set reported as
				// complete when it stopped partway, silently under-excluding the
				// rest of the tree. That holds for the live-entries ceiling as
				// much as for the other two: this walk has to start at the root
				// and visit everything below it, so it holds a deeper chain live
				// than a pattern walk that can skip to a literal prefix, and
				// reaching the ceiling here means the process is already carrying
				// the memory the ceiling exists to prevent. Continuing on and
				// leaving a later walk to refuse would spend that memory first.
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
				// Past the scope's reach nothing below can hold a rule that
				// touches one of its candidates, so descending would inspect a
				// subtree the caller's own walk never lists. Only ** crosses a
				// separator, which is why an unbounded scope has no cut here.
				if sc.depth >= 0 && ignoreScopeRelDepth(sc.prefix, p) > sc.depth {
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
			if loadedRules[p] {
				return nil
			}
			loadedRules[p] = true
			data, berr := readIgnoreFile(ctx, fsys, p, budget)
			if berr != nil {
				budgetErr = berr
				return berr
			}
			if data == nil {
				return nil
			}
			dir := path.Dir(p)
			matcher := gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
			set.dirs = append(set.dirs, ignoreDir{rel: dir, matcher: matcher})
			return nil
		})
		if budgetErr != nil {
			break
		}
	}
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
