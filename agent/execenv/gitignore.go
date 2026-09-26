package execenv

import (
	"bytes"
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
// matcher because its matcher would match nothing.
func readIgnoreFile(ctx context.Context, fsys fs.FS, path string, budget *GlobBudget) ([]string, bool, error) {
	f, err := fsys.Open(path)
	if err != nil {
		// A cancelled open is not an unreadable .gitignore: swallowing it
		// here would let the discovery walk keep traversing after
		// cancellation and report whatever partial rule set it assembled.
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, cerr
		}
		return nil, false, nil //nolint:nilerr // best-effort: skip a missing or unreadable .gitignore
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxGlobIgnoreFileBytes)+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, cerr
		}
		return nil, false, nil //nolint:nilerr // best-effort: skip an unreadable .gitignore
	}
	if berr := budget.tooManyRuleBytes(path, len(data)); berr != nil {
		return nil, false, berr
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
		return nil, false, berr
	}
	if cost.rules == 0 {
		return nil, true, nil
	}
	return ignoreRuleLines(data), true, nil
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

// compiledIgnoreRules counts, from a .gitignore's raw bytes, the lines
// go-gitignore.CompileIgnoreLines tries to turn into a matcher and reports what
// those lines expand into: their source, the source and stars inside a counted
// repetition, their glob stars, and their Unicode property escapes. Each of
// those compiles to far more instructions than the source that wrote it — a
// repetition because `regexp` expands it, a star because the library rewrites
// each one to a group, a property escape because it carries a whole rune table
// — so the charge is driven by the constructs the line actually holds rather
// than by its length. Counting happens before any line is materialized.
//
// The count is an upper bound, not an exact match, on the matchers the library
// retains: a line whose generated regexp fails to compile (an unterminated
// character class, say) is still counted here but yields no pattern there.
// Over-counting is the safe direction — it can refuse a call early but never
// under-charge one — and it can never turn a zero-rule file into a retained
// matcher, since such a file has no such line at all. The alternative,
// exact-matching the library, would mean reaching into its unexported pattern
// list from production code.
func compiledIgnoreRules(data []byte) ignoreFileCost {
	var c ignoreFileCost
	eachIgnoreLine(data, func(line []byte) {
		c.rules++
		expansion := classifyIgnoreLine(line)
		c.expansionUnits = satAdd(c.expansionUnits, expansion.units)
	})
	return c
}

// ignoreLineExpansion is what one rule line compiles into, in units of one
// compiled instruction. It is computed by a single forward parse so that a
// nested quantifier multiplies the units inside its element and no byte is ever
// re-scanned.
type ignoreLineExpansion struct {
	units int64 // compiled instruction units the line expands into
}

// globStarUnits is the instruction cost of one glob star. go-gitignore rewrites
// each `*` to a capture, a character class and a split before compiling, which
// measured about five instructions.
const globStarUnits = 5

// globUnicodeUnits is the instruction-unit charge for one Unicode property
// escape. The recorded cost is globIgnoreUnicodeClassBytes for the property's
// rune table, expressed in units so that it rides the same atom and is
// multiplied by any counted or nested repetition around it, exactly as the
// compiled table is.
const globUnicodeUnits = (globIgnoreUnicodeClassBytes + globIgnoreUnitBytes - 1) / globIgnoreUnitBytes

// classifyIgnoreLine parses line once, left to right, and reports the compiled
// instruction units it expands into. An escaped byte is literal, so `\*` is not
// a star and `\{` is not a quantifier; so is anything inside a character class,
// so `[*]` is not a star, and `\p`/`\P` is a Unicode property.
//
// This estimate is best-effort, not a proof of the budget it feeds. It accounts
// for every expansion vector found so far — a literal, a glob star, a counted
// repetition and its nesting, an optional branch, a capturing group, and a
// Unicode property escape, including inside a character class and under a
// repetition — and it cannot bound a construct whose compiled size it misses.
// The charge is produced by a second parser that has to agree byte-for-byte
// with go-gitignore's rewrite plus Go's regexp/syntax, and there is no fixed
// point short of reimplementing that parser, so a later construct can still slip
// past. The follow-up is to charge the real compiled program size instead; see
// issue #1971.
//
// A quantifier multiplies the units of the element it follows, and because a
// group's units are accumulated before the group's own quantifier is seen, a
// nested quantifier multiplies everything inside it: `(a{900}){900}` records
// 810,000 units from one source byte. A malformed line — an unmatched `)` or a
// group left open at the end — is treated as literal bytes, and nothing scans
// backwards, so the parse is linear in the line length.
func classifyIgnoreLine(line []byte) ignoreLineExpansion {
	var e ignoreLineExpansion
	type frame struct{ units int64 }
	stack := []frame{{}}
	flush := func(units int64) {
		top := len(stack) - 1
		stack[top].units = satAdd(stack[top].units, units)
	}
	// pending holds the units of the atom just completed, waiting for a
	// quantifier; -1 means no atom is waiting.
	pending := int64(-1)
	finish := func() {
		if pending >= 0 {
			flush(pending)
			pending = -1
		}
	}
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			finish()
			units := int64(1)
			if i+1 < len(line) {
				if line[i+1] == 'p' || line[i+1] == 'P' {
					units += globUnicodeUnits
					// A property escape is `\pL` or `\p{Greek}`, one atom whose
					// quantifier must attach to the whole escape rather than to
					// the letter after `\p`.
					i = ignorePropertyEnd(line, i)
				} else {
					i++ // the escaped byte is literal
				}
			}
			pending = units
		case '[':
			finish()
			end, _ := ignoreClassEnd(line, i)
			pending = int64(end-i+1) + int64(countUnicodeClasses(line[i:end+1]))*globUnicodeUnits
			i = end
		case '(':
			finish()
			// A capturing group compiles to its own instruction, and a
			// quantifier after the group repeats that instruction too, so the
			// capture is the frame's starting cost. Go emits a bracket pair per
			// capture, so it is charged as two units.
			stack = append(stack, frame{units: 2})
		case ')':
			finish()
			if len(stack) > 1 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				pending = top.units
			} else {
				pending = 1 // unmatched, so a literal byte
			}
		case '*':
			finish()
			pending = globStarUnits
		case '{':
			if factor, end, ok := countedRepetitionFactor(line, i); ok && pending >= 0 {
				pending = satMul(pending, factor)
				i = end
				continue
			}
			finish()
			pending = 1 // literal brace byte
		default:
			finish()
			pending = 1
		}
	}
	finish()
	// A group left open at the end still holds units that compile.
	for len(stack) > 1 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dest := len(stack) - 1
		stack[dest].units = satAdd(stack[dest].units, top.units)
	}
	e.units = stack[0].units
	return e
}

// ignoreClassEnd returns the index of the `]` that closes the character class
// opening at line[i], or the last index of the line when it never closes, and
// whether it closed. An escaped byte inside the class cannot close it, and a
// `]` in the first position (or right after a `^`) is literal, as `regexp`
// treats it: the class `[])]` holds `]` and `)` and closes at the last `]`.
func ignoreClassEnd(line []byte, i int) (end int, closed bool) {
	j := i + 1
	if j < len(line) && line[j] == '^' {
		j++
	}
	if j < len(line) && line[j] == ']' {
		j++ // the first `]` is a member of the class
	}
	for ; j < len(line); j++ {
		switch line[j] {
		case '\\':
			j++
		case ']':
			return j, true
		}
	}
	return len(line) - 1, false
}

// ignorePropertyEnd returns the index of the last byte of the `\p`/`\P` escape
// that starts at line[i], so a quantifier after it attaches to the whole escape.
// The name is either one letter (`\pL`) or a braced name (`\p{Greek}`).
func ignorePropertyEnd(line []byte, i int) int {
	j := i + 2 // past `\` and `p`/`P`
	if j < len(line) && line[j] == '{' {
		for k := j + 1; k < len(line); k++ {
			if line[k] == '}' {
				return k
			}
		}
		return len(line) - 1
	}
	if j < len(line) {
		return j
	}
	return i + 1
}

// countUnicodeClasses counts the `\p`/`\P` property escapes inside a character
// class. A class holding one carries the same rune table as the bare escape, so
// it costs the same.
func countUnicodeClasses(class []byte) int {
	n := 0
	for k := 0; k < len(class); k++ {
		if class[k] != '\\' {
			continue
		}
		if k+1 < len(class) && (class[k+1] == 'p' || class[k+1] == 'P') {
			n++
		}
		k++
	}
	return n
}

// countedRepetitionFactor reports the multiplicative factor of a quantifier
// opening at line[i], the index of its closing `}`, and whether it is a
// quantifier at all. `regexp` compiles `{n}` to n copies, `{n,m}` to m copies
// plus m-n optional copies, and `{n,}` to n copies plus a star, so the factor is
// max + (max-min); a brace with no valid bounds — `{cache}`, `{js,map}`, `foo{`
// — and one that expands to nothing, `{0}` or `{0,0}`, is a literal instead.
// A bound with a leading zero, like `{01}` or `{00,1000}`, is literal too, as
// `regexp` treats it: the only valid zero is a bare `0`.
func countedRepetitionFactor(line []byte, i int) (factor int64, end int, ok bool) {
	j := i + 1
	start := j
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == start {
		return 0, 0, false
	}
	if !validRepetitionBound(line[start:j]) {
		return 0, 0, false
	}
	lo := repetitionBound(line[start:j])
	hiBound := lo
	if j < len(line) && line[j] == ',' {
		j++
		hiStart := j
		for j < len(line) && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		if j == hiStart {
			// `regexp` treats `{n,` as a quantifier only when the terminator is
			// present; otherwise the `{` is literal. Checking here also stops
			// the classifier from consuming whatever byte follows the comma.
			if j >= len(line) || line[j] != '}' {
				return 0, 0, false
			}
			// `{n,}` compiles to n copies followed by a star loop of constant
			// size, not to n plus 1000 more copies: that is `{n,m}`'s behavior.
			// Charging the loop as a constant keeps a valid `{1,}` rule from
			// exhausting the budget.
			return int64(lo) + globStarUnits, j, true
		}
		if !validRepetitionBound(line[hiStart:j]) {
			return 0, 0, false
		}
		hiBound = repetitionBound(line[hiStart:j])
	}
	if j >= len(line) || line[j] != '}' {
		return 0, 0, false
	}
	if hiBound == 0 {
		return 0, 0, false
	}
	if hiBound < lo {
		hiBound = lo
	}
	return int64(hiBound) + int64(hiBound-lo), j, true
}

// validRepetitionBound reports whether digits are a repetition bound `regexp`
// accepts: the sole digit `0`, or a decimal with no leading zero. `{00}`,
// `{01}` and `{00,1000}` are not repetitions to it, so they stay literal.
func validRepetitionBound(digits []byte) bool {
	if len(digits) == 0 {
		return false
	}
	return len(digits) == 1 || digits[0] != '0'
}

// repetitionBound parses a decimal repetition bound. A malformed or overlong
// bound saturates high, which is the conservative direction: it is charged as
// an expansion rather than dismissed as a literal.
func repetitionBound(digits []byte) int {
	n := 0
	for _, d := range digits {
		n = n*10 + int(d-'0')
		if n > 1<<20 {
			return 1 << 20
		}
	}
	return n
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
// masking. The off-path caller (no masking concept) passes a no-op skip.
//
// budget is the same one the caller's glob walk spends listings from, so
// ignore discovery and pattern matching together are bounded as one call's
// worth of work rather than each getting its own unbounded pass over the
// tree.
func loadIgnoreSet(ctx context.Context, fsys fs.FS, skip func(relPath string) bool, budget *GlobBudget, scope []ignoreScope) (*ignoreSet, error) {
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
			lines, read, berr := readIgnoreFile(ctx, fsys, p, budget)
			if berr != nil {
				return set, berr
			}
			// Only a path that was actually read is remembered: a missing or
			// unreadable file keeps nothing, so it is neither cached nor
			// charged, and a later scope merely tries it again cheaply.
			if read {
				loadedRules[p] = true
			}
			if lines == nil {
				continue
			}
			matcher := gitignore.CompileIgnoreLines(lines...)
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
			lines, read, berr := readIgnoreFile(ctx, fsys, p, budget)
			if berr != nil {
				budgetErr = berr
				return berr
			}
			if read {
				loadedRules[p] = true
			}
			if lines == nil {
				return nil
			}
			dir := path.Dir(p)
			matcher := gitignore.CompileIgnoreLines(lines...)
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
