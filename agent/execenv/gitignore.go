package execenv

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	gitignore "github.com/sabhiram/go-gitignore"
)

// ignoreMatcherCache memoizes the compiled matcher of each .gitignore file
// across the search calls one environment makes, so a session that globs or
// greps repeatedly compiles a rules file once instead of once per call.
//
// It changes only how long a matcher lives, never what it matches. An entry
// records the (size, modification time) the file had when its bytes were read,
// and a lookup hits only when the file still has exactly that pair, so an
// edited, appended, truncated or replaced .gitignore is recompiled on the next
// call. The staleness window left is the one every stat-keyed cache leaves: a
// rewrite that restores the same byte count at the same modification
// timestamp. Timestamps carry nanoseconds on every platform this builds for, so
// an ordinary edit moves the key.
//
// It holds one entry per rules path rather than one per version, so its size
// grows with the number of .gitignore files a session has searched through and
// never with how often they were edited. That number is not bounded either — a
// long-lived session greps many trees and keeps every one's matcher — so each
// entry records the charge a call's rules budget pays for that file, and the map
// is cleared when a store would take the summed charge past
// maxGlobIgnoreTotalBytes. A dropped entry is recompiled on its next search, so
// a tree whose rules fit the bound recompiles once after a clear and hits again
// from then on. That is a ceiling on what a session may pin, not a policy.
//
// Entries are keyed by the rules file's ABSOLUTE path, not by its path relative
// to whichever base the call searched, because one cache serves an environment
// that searches many bases: a glob rooted at /repo/a and a later one rooted at
// /repo/b both name ".gitignore", and those are different files. Keying on the
// relative path would let the second call read the first directory's matcher
// whenever the two files happened to share a size and modification time, which
// git-archive tarballs, Nix store paths and coarse-timestamped copies all make
// easy to arrange.
//
// It deliberately caches only the compile of a file discovery has already read,
// never the ignoreSet as a whole: which rules files exist is discovery's own
// walk's answer, and a cached answer could not notice one added since.
type ignoreMatcherCache struct {
	mu      sync.Mutex
	entries map[string]ignoreMatcherEntry
	// stored is the summed charge of every entry, maintained under mu so the
	// retention bound never has to walk the map.
	stored int
}

// ignoreMatcherEntry is one rules file's last compiled version.
type ignoreMatcherEntry struct {
	size    int64
	mod     time.Time
	matcher *gitignore.GitIgnore
	// cost is what the read that produced matcher charged its call, so a hit
	// can charge the next call exactly the same.
	cost ignoreFileCost
}

// newIgnoreMatcherCache returns an empty cache, ready for use.
func newIgnoreMatcherCache() *ignoreMatcherCache {
	return &ignoreMatcherCache{entries: make(map[string]ignoreMatcherEntry)}
}

// ignoreMatcherCacheFor returns the environment's matcher cache, creating it on
// first use. A struct-literal environment (a test double, or a value built
// before this field existed) has no cache, and the searches it runs must still
// compile rules rather than crash, so the field is filled in lazily. The mutex
// guarding that is the environment's own sandbox lock, which every file-tool
// operation already takes; a nil environment answers nil and the caller reads
// rules uncached.
func (e *LocalExecutionEnvironment) ignoreMatcherCacheFor() *ignoreMatcherCache {
	if e == nil {
		return nil
	}
	e.sbMu.Lock()
	defer e.sbMu.Unlock()
	if e.ignoreMatchers == nil {
		e.ignoreMatchers = newIgnoreMatcherCache()
	}
	return e.ignoreMatchers
}

// ignoreCacheKey is one rules file's identity in the cache: the absolute
// filesystem root the searching call was rooted at, and the file's path
// relative to that root. Both halves are needed for the reason the type comment
// gives — the cache serves an environment that searches many bases, so the
// relative path alone does not name a file.
//
// A root of "" means the filesystem has no absolute root of its own, which is
// true only of the in-memory filesystems tests build. There the test's own
// path namespace is the namespace, and each test has its own cache.
func ignoreCacheKey(root, relPath string) string {
	if root == "" {
		return relPath
	}
	return filepath.Join(root, relPath)
}

// lookup returns the entry recorded for root+path when the file still has the
// version that entry was built from. A nil cache never hits.
func (c *ignoreMatcherCache) lookup(root, path string, size int64, mod time.Time) (ignoreMatcherEntry, bool) {
	if c == nil {
		return ignoreMatcherEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[ignoreCacheKey(root, path)]
	if !ok || entry.size != size || !entry.mod.Equal(mod) {
		return ignoreMatcherEntry{}, false
	}
	return entry, true
}

// store records entry for root+path, replacing any earlier version. A nil cache
// drops it. A store that would take the retained total past
// maxGlobIgnoreTotalBytes drops every entry first and restarts the total, so a
// session that has searched enough distinct trees starts over rather than keep
// growing. The clear happens before this entry is recorded, not after, so an
// entry larger than the bound on its own still gets cached and the next search
// of that same tree still hits instead of recompiling and clearing again.
func (c *ignoreMatcherCache) store(root, path string, entry ignoreMatcherEntry) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := ignoreCacheKey(root, path)
	if old, ok := c.entries[key]; ok {
		c.stored -= old.cost.charge()
	}
	if cost := entry.cost.charge(); c.stored+cost > maxGlobIgnoreTotalBytes {
		clear(c.entries)
		c.stored = 0
	}
	c.entries[key] = entry
	c.stored += entry.cost.charge()
}

// readIgnoreRules reads one .gitignore under the call's budgets and returns that
// file's compiled matcher — nil for a file that compiles no rule — together with
// whether its rules were available at all. cache, when not nil and when the file
// still has the size and modification time its entry was built from, supplies the
// matcher without reading the file again.
//
// read is false only for a file that is missing or unreadable; a cache hit
// answers true, because the caller asks whether rules were available, not where
// they came from.
//
// A hit charges the call exactly what the read that populated the entry paid,
// source bytes included, so a cached call is bounded by the same accounting as
// an uncached one: repeating an operation cannot buy more rules than the budget
// allows by hitting the cache, and a call that would have been refused is still
// refused. That is deliberate — the budget bounds what a call retains and reads,
// and a call that retains the same matchers again really does hold them again
// for its own duration.
func readIgnoreRules(ctx context.Context, fsys fs.FS, root, path string, budget *GlobBudget, cache *ignoreMatcherCache) (matcher *gitignore.GitIgnore, read bool, err error) {
	size, mod, identified := ignoreFileVersion(ctx, fsys, path, cache)
	if identified {
		if entry, ok := cache.lookup(root, path, size, mod); ok {
			if berr := budget.retainIgnoreFile(entry.cost); berr != nil {
				return nil, false, berr
			}
			return entry.matcher, true, nil
		}
	}
	lines, read, cost, err := readIgnoreFile(ctx, fsys, path, budget)
	if err != nil || !read {
		return nil, read, err
	}
	var compiled *gitignore.GitIgnore
	if lines != nil {
		compiled = compileIgnoreLines(lines...)
	}
	if identified {
		// Stored under the version the file had *before* its bytes were read, so
		// a change landing in between leaves the entry stale and the next call
		// recompiles, rather than letting a later call hit a matcher built from
		// different bytes.
		cache.store(root, path, ignoreMatcherEntry{size: size, mod: mod, matcher: compiled, cost: cost})
	}
	return compiled, true, nil
}

// ignoreFileVersion stats path for the cache key. ok is false when there is no
// cache to key or the file cannot be stat'ed, in which case the caller reads it
// (and charges it) exactly as it did before the cache existed.
func ignoreFileVersion(ctx context.Context, fsys fs.FS, path string, cache *ignoreMatcherCache) (size int64, mod time.Time, ok bool) {
	if cache == nil {
		return 0, time.Time{}, false
	}
	info, err := fs.Stat(fsys, path)
	if err != nil {
		// A cancelled stat is not an unreadable rules file: the read below
		// answers the cancellation, and the discovery walk stops on it.
		return 0, time.Time{}, false
	}
	return info.Size(), info.ModTime(), true
}

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

// readIgnoreFile reads one .gitignore under the call's budgets. It stops one
// byte past the per-file cap rather than reading the whole file, so an
// enormous rules file is refused without first being materialized, and it
// charges what the file costs the call against the call-wide ignore budget:
// its path entry, each compiled rule and the source that rule was written as,
// and the source it read. Every successfully read file is charged, rule-free
// ones included, so the budget bounds the aggregate source a call reads and not
// only the matchers it retains.
//
// It answers three ways: a refusal, which the caller propagates; nil lines
// with no error, which the caller skips; and the rule lines otherwise, ready to
// compile. The second return value reports whether the file was actually read:
// true for a file that was read even if it compiles no rule, false for one that
// is missing or unreadable. A caller caches only the paths it actually read, so
// a failed read is neither remembered nor charged, and one that compiles no
// rule is remembered and charged like any other file, though it contributes no
// matcher because its matcher would match nothing. The third value is that
// charge, so a cache hit can repeat it exactly.
func readIgnoreFile(ctx context.Context, fsys fs.FS, path string, budget *GlobBudget) ([]string, bool, ignoreFileCost, error) {
	f, err := fsys.Open(path)
	if err != nil {
		// A cancelled open is not an unreadable .gitignore: swallowing it
		// here would let the discovery walk keep traversing after
		// cancellation and report whatever partial rule set it assembled.
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, ignoreFileCost{}, cerr
		}
		return nil, false, ignoreFileCost{}, nil //nolint:nilerr // best-effort: skip a missing or unreadable .gitignore
	}
	defer func() { _ = f.Close() }()
	data, err := readIgnoreFileBytes(f, int64(maxGlobIgnoreFileBytes)+1)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, ignoreFileCost{}, cerr
		}
		return nil, false, ignoreFileCost{}, nil //nolint:nilerr // best-effort: skip an unreadable .gitignore
	}
	if berr := budget.tooManyRuleBytes(path, len(data)); berr != nil {
		return nil, false, ignoreFileCost{}, berr
	}
	// Count and charge from the raw bytes before any line is materialized: a
	// split allocates a string header per line, so a near-cap file of newlines
	// would allocate tens of megabytes before a refusal that could have been
	// made from the bytes alone. Only the lines that will actually compile are
	// collected afterwards, and only for a file that stayed within budget, so a
	// blank- or comment-heavy file never has those lines materialized.
	cost := compiledIgnoreRules(data)
	cost.path = len(path)
	cost.source = len(data)
	if berr := budget.retainIgnoreFile(cost); berr != nil {
		return nil, false, ignoreFileCost{}, berr
	}
	if cost.rules == 0 {
		return nil, true, cost, nil
	}
	return ignoreRuleLines(data), true, cost, nil
}

// ignoreRuleLines collects the lines of a .gitignore that
// go-gitignore.CompileIgnoreLines will compile, leaving out the blank and
// comment lines it would drop anyway. It runs only after the budget has been
// charged, so a file that is refused, or that compiles no rule, never has its
// lines materialized. It is a variable so a test can observe that directly.
var ignoreRuleLines = func(data []byte) []string {
	var lines []string
	eachIgnoreLine(data, func(line []byte) {
		lines = append(lines, string(line))
	})
	return lines
}

// compileIgnoreLines builds the matcher for a .gitignore's rule lines. It is a
// variable so a test can count compiles and tell a matcher reused from the
// cache apart from one rebuilt on the spot.
var compileIgnoreLines = gitignore.CompileIgnoreLines

// compiledIgnoreRules counts, from a .gitignore's raw bytes, the lines
// go-gitignore.CompileIgnoreLines tries to turn into a matcher. The count is an
// upper bound on the matchers the library retains: a line whose generated
// regexp fails to compile (an unterminated character class, say) is still
// counted here but yields no pattern there. Over-counting can refuse a call
// early but never under-charge one, and it can never turn a zero-rule file into
// a retained matcher, since such a file has no such line at all.
func compiledIgnoreRules(data []byte) ignoreFileCost {
	var c ignoreFileCost
	eachIgnoreLine(data, func([]byte) { c.rules++ })
	return c
}

// eachIgnoreLine calls visit for every line of a .gitignore that
// go-gitignore.CompileIgnoreLines will try to compile, passing the line with
// its trailing carriage return removed. A line is skipped when it is a comment
// in column zero or, after trimming spaces from both ends, is empty, which
// mirrors the library's getPatternFromLine exactly; an indented comment is not
// skipped, because the library does not treat it as one. Walking the bytes
// rather than splitting is what lets a caller count or collect only the lines
// that compile, without materializing the rest.
func eachIgnoreLine(data []byte, visit func(line []byte)) {
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimRight(line, "\r")
		if len(line) > 0 && line[0] == '#' {
			continue
		}
		if len(bytes.Trim(line, " ")) == 0 {
			continue
		}
		visit(line)
	}
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
// masking. The off-path glob caller (no masking concept) passes a no-op
// skip; the off-path grep caller passes the pseudo-filesystem skip so rule
// discovery never lists the trees its own walk refuses to enter.
//
// budget is the same one the caller's glob walk spends listings from, so
// ignore discovery and pattern matching together are bounded as one call's
// worth of work rather than each getting its own unbounded pass over the
// tree.
func loadIgnoreSet(ctx context.Context, fsys fs.FS, skip func(relPath string) bool, budget *GlobBudget, scope []ignoreScope) (*ignoreSet, error) {
	return loadIgnoreSetCached(ctx, fsys, skip, budget, scope, "", nil)
}

// loadIgnoreSetCached is loadIgnoreSet with a matcher cache: cache, when not
// nil, supplies the compiled matcher of every .gitignore a previous call in the
// same environment already compiled, keyed on that file's path, size and
// modification time. Everything else — which files discovery reads, when it
// refuses, what each rule contributes — is exactly loadIgnoreSet's behavior, so
// the two differ only in how long a compiled matcher lives.
//
// A hit is charged the same ignoreFileCost the reading call paid, so the
// call-wide rules budget still bounds the aggregate a single call retains and
// reads; a repeated operation cannot exceed the budget by going through the
// cache.
func loadIgnoreSetCached(ctx context.Context, fsys fs.FS, skip func(relPath string) bool, budget *GlobBudget, scope []ignoreScope, root string, cache *ignoreMatcherCache) (*ignoreSet, error) {
	set := &ignoreSet{}
	scopes := narrowIgnoreScopes(scope)

	// loadedRules keys every .gitignore this call has read, so no scope reads
	// one twice: a second read would charge the file's path and source again,
	// and a file that contributed rules would compile and retain a second
	// matcher. Each remembered path is charged globIgnoreFileOverheadBytes, so
	// the map itself is bounded by the budget rather than growing with the
	// tree. A path that could not be read is not remembered — it is neither
	// cached nor charged, and re-trying it costs one failed open.
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
			// them: IsDotPath drops every candidate underneath one before a
			// rule from it could apply, so reading it could not change an
			// answer and would spend the rules budget for nothing. The check
			// is on the full path, not the basename: a scope like
			// a/.config/sub sits beneath a dot-directory without naming one
			// itself, and reading under it is just as pointless.
			if dir != "." && IsDotPath(dir) {
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
			matcher, read, berr := readIgnoreRules(ctx, fsys, root, p, budget, cache)
			if berr != nil {
				return set, berr
			}
			// Only a path that was actually read is remembered: a missing or
			// unreadable file keeps nothing, so it is neither cached nor
			// charged, and a later scope merely tries it again cheaply.
			if read {
				loadedRules[p] = true
			}
			if matcher == nil {
				continue
			}
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
		// IsDotPath drops every path under one before a rule from inside
		// could apply, so walking it only spends the listing and rules
		// budget — and a large enough subtree under it can refuse a glob
		// whose answer is already fixed. The walk's own d.Name() check cannot
		// see this: it only skips dot-named entries, never a scope already
		// rooted under one.
		if IsDotPath(sc.prefix) {
			continue
		}
		// Each scope's walk is its own traversal, so it starts holding
		// nothing: a listing the previous scope's walk was still holding when
		// it finished is not held any more, and counting it here would refuse
		// on memory nothing occupies. The cumulative counters stay call-wide.
		budget.resetLive()
		walkErr := fs.WalkDir(fsys, sc.prefix, func(p string, d fs.DirEntry, err error) error {
			// A cancellation can land after a directory was successfully read,
			// so its remaining entries keep arriving with a nil error and no
			// file is ever opened again: without this check the walk would
			// process them all and report whatever partial rule set it
			// assembled. Checking first also keeps a cancelled walk that hit
			// the budget reporting the cancellation the caller asked for.
			if cerr := ctx.Err(); cerr != nil {
				budgetErr = cerr
				return cerr
			}
			if err != nil {
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
			matcher, read, berr := readIgnoreRules(ctx, fsys, root, p, budget, cache)
			if berr != nil {
				budgetErr = berr
				return berr
			}
			if read {
				loadedRules[p] = true
			}
			if matcher == nil {
				return nil
			}
			dir := path.Dir(p)
			set.dirs = append(set.dirs, ignoreDir{rel: dir, matcher: matcher})
			return nil
		})
		// The walk's own return is the backstop for a failure the callback
		// never classified: every callback error sets budgetErr, so a
		// non-nil return with budgetErr still nil is either a cancellation
		// that raced the callback checks or a best-effort traversal failure
		// the callback already swallowed. Cancellations report; anything
		// else stays best-effort.
		if budgetErr == nil && walkErr != nil {
			if cerr := ctx.Err(); cerr != nil {
				budgetErr = cerr
			}
		}
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
	if IsDotPath(m) {
		return true, nil
	}
	isDir, err := globMatchIsDir(ctx, fsys, m)
	if err != nil {
		return false, err
	}
	return ignores.matches(m, isDir), nil
}

// IsDotPath reports whether any path component of relPath (slash-separated)
// starts with "." — the existing convention (matching grepNative's long-
// standing hidden-file skip) for hiding VCS internals, worktree scratch
// dirs (.claude/worktrees/x), and other dotfiles from unscoped search.
func IsDotPath(relPath string) bool {
	for part := range strings.SplitSeq(relPath, "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
