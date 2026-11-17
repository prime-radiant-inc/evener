package execenv

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// globCandidate is one path a call's patterns matched, with how many of the
// expanded patterns selected it. The count is what keeps the exclusion tally
// identical to a per-pattern loop's: a path two patterns both match and the
// default exclusions drop was counted twice there, once per pattern.
type globCandidate struct {
	path     string
	patterns int
}

// globUnionGroup is one traversal shared by several expanded patterns whose
// own walks would list the same directories.
type globUnionGroup struct {
	// cover is a doublestar pattern whose walk visits every path any of the
	// group's patterns can match, and no subtree none of them reaches.
	cover    string
	patterns []string
}

// globUnionPrefix reports the walk root pattern can share with another
// pattern, or ok false when it has to keep its own traversal. Being shareable
// is not a property of the pattern alone — a group needs at least two members
// — but it is what decides whether the pattern may join one.
//
// A pattern qualifies only when its own walk already lists exactly the subtree
// below its literal prefix, which is what the shared cover lists. Two things
// make that true and are checked directly:
//   - the pattern is its own clean form, so no "./", "../", "//" or trailing
//     separator re-spells a path doublestar normalizes away (a trailing slash
//     also makes doublestar report a directory with the slash attached, which
//     no match string carries);
//   - the first component after the prefix is **, so the walk descends into
//     every child rather than only into children whose names match a component
//     like "b*" or "[ab]". A selective first component is exactly the case a
//     shared <prefix>/** cover would list more than the pattern's own walk, and
//     a shared walk is never allowed to cost more than the walks it replaces.
//
// Everything after that first ** only filters paths the walk already visited,
// which is what doublestar.Match answers for them. A pattern whose last
// component is ** keeps its own walk: it also matches the prefix directory
// itself, which a cover reports as a different path than the pattern's walk
// reports, and special-casing that per pattern buys nothing.
func globUnionPrefix(pattern string) (string, bool) {
	if strings.ContainsAny(pattern, `{}\`) || strings.Contains(pattern, "//") {
		// Escaped metacharacters and leftover single-term brace groups change
		// how the pattern is split and matched, and an empty component is not a
		// spelling doublestar's walk and Match agree on. Those shapes keep
		// their own walk rather than being reasoned about here.
		return "", false
	}
	if pattern == "" || pattern != path.Clean(pattern) {
		return "", false
	}
	prefix, rest := doublestar.SplitPattern(pattern)
	if prefix == "" || prefix == "/" || prefix != path.Clean(prefix) {
		// A prefix that is not its own clean form names no directory
		// doublestar walks either; keep the pattern's own walk so its answer
		// stays exactly what it was.
		return "", false
	}
	if !strings.HasPrefix(rest, "**/") {
		return "", false
	}
	if globLastComponent(pattern) == "**" {
		return "", false
	}
	return prefix, true
}

// globLastComponent returns pattern's final slash-separated component.
func globLastComponent(pattern string) string {
	if _, last, found := strings.CutLast(pattern, "/"); found {
		return last
	}
	return pattern
}

// globUnionGroups partitions an expanded pattern set into groups that can share
// one traversal and patterns that cannot. Grouping is by walk root alone: two
// patterns sharing a root list the same subtree, so one cover lists it for
// both, while patterns with different roots are not merged even when one root
// contains the other, since that would need a rule about which root's cover
// reaches which pattern's matches.
func globUnionGroups(patterns []string) (groups []globUnionGroup, singles []string) {
	byPrefix := make(map[string][]string, len(patterns))
	order := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		prefix, ok := globUnionPrefix(pattern)
		if !ok {
			singles = append(singles, pattern)
			continue
		}
		if _, seen := byPrefix[prefix]; !seen {
			order = append(order, prefix)
		}
		byPrefix[prefix] = append(byPrefix[prefix], pattern)
	}
	for _, prefix := range order {
		group := byPrefix[prefix]
		if len(group) < 2 {
			// Nothing to share: one pattern's cover would be its own walk.
			singles = append(singles, group...)
			continue
		}
		cover := prefix + "/**"
		if prefix == "." {
			cover = "**"
		}
		groups = append(groups, globUnionGroup{cover: cover, patterns: group})
	}
	return groups, singles
}

// globCandidatesForPatterns matches an expanded pattern set against fsys,
// listing each group's subtree once instead of once per pattern. The budget is
// the call's, shared by every traversal here exactly as it was shared by the
// per-pattern loop this replaces, so the call stays bounded by one call's
// worth of listing and match work however many alternatives the pattern
// expands into.
func globCandidatesForPatterns(ctx context.Context, fsys fs.FS, patterns []string, budget *GlobBudget) ([]globCandidate, error) {
	groups, singles := globUnionGroups(patterns)
	if len(groups) == 0 {
		// No two patterns share a walk, so every answer comes from the same
		// per-pattern traversal a single-pattern call uses.
		return globSinglePatternCandidates(ctx, fsys, singles, budget)
	}
	var out []globCandidate
	for _, group := range groups {
		candidates, err := globUnionWalk(ctx, fsys, group, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, candidates...)
	}
	tail, err := globSinglePatternCandidates(ctx, fsys, singles, budget)
	if err != nil {
		return nil, err
	}
	return append(out, tail...), nil
}

// globSinglePatternCandidates runs one pattern's own traversal for each
// pattern, each contributing only itself to the pattern count.
func globSinglePatternCandidates(ctx context.Context, fsys fs.FS, patterns []string, budget *GlobBudget) ([]globCandidate, error) {
	var out []globCandidate
	for _, pattern := range patterns {
		matches, err := globMatches(ctx, fsys, pattern, budget)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			out = append(out, globCandidate{path: m, patterns: 1})
		}
	}
	return out, nil
}

// globUnionWalk lists the group's cover subtree once and selects every pattern
// the paths it visits match. Selection is doublestar's own Match, so a path is
// selected by exactly the pattern text doublestar's walk would have used.
//
// The root path "." is never offered to Match: doublestar's walk reports it
// only for a pattern whose last component is ** or that ends in a separator,
// and both of those shapes keep their own traversal (see globUnionPrefix), so
// dropping it here cannot lose a match a per-pattern walk would have reported.
//
// A match is charged to the budget once per candidate path, before the
// caller's exclusion and dedupe, which is what the per-pattern loop charged per
// path per pattern; a path two patterns select is now one candidate instead of
// two, so the cap can only admit more of the tree, never less.
func globUnionWalk(ctx context.Context, fsys fs.FS, group globUnionGroup, budget *GlobBudget) ([]globCandidate, error) {
	if budget.full() {
		// A cancelled walk must not come back as a plausible-looking short
		// list: a cap that is already tripped when a cancellation lands must
		// still answer with the cancellation, not with the truncated result
		// the cap alone would produce.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, nil
	}
	budget.resetLive()
	walk := &globWalkFS{FS: fsys, ctx: ctx, budget: budget}
	var out []globCandidate
	err := doublestar.GlobWalk(walk, group.cover, func(p string, _ fs.DirEntry) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == "." {
			return nil
		}
		selected := 0
		for _, pattern := range group.patterns {
			matched, merr := doublestar.Match(pattern, p)
			if merr != nil {
				return merr
			}
			if matched {
				selected++
			}
		}
		if selected == 0 {
			return nil
		}
		if !budget.match() {
			return errGlobMatchesFull
		}
		out = append(out, globCandidate{path: p, patterns: selected})
		return nil
	}, doublestar.WithFailOnIOErrors())
	// A cancelled walk must not come back as a plausible-looking short list, so
	// answer with the cancellation however the walk happened to unwind.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	// The match cap ending the walk is not a failure: it is the mechanism by
	// which the cap bounds the work rather than only the result, so the
	// candidates collected before it tripped are the answer, not an error.
	if errors.Is(err, errGlobMatchesFull) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// globExpandedMatches turns an expanded pattern set into the absolute paths a
// glob call reports, with everything the default dotfile/gitignore exclusion
// drops left out and counted. base is the directory the matches are relative
// to; masked, when not nil, drops an absolute path for a reason outside the
// default exclusions (the sandbox's policy) without counting it as excluded.
//
// Candidates are collected first and filtered here, so the exclusion and
// dedupe decisions are per path rather than per traversal. A path several
// patterns select is reported once, and contributes one exclusion to the count
// for each pattern that selected it, which is what the per-pattern loop
// counted before dedupe.
func globExpandedMatches(ctx context.Context, fsys fs.FS, base string, patterns []string, includeIgnored bool, ignores *ignoreSet, budget *GlobBudget, masked func(abs string) bool) ([]string, int, error) {
	candidates, err := globCandidatesForPatterns(ctx, fsys, patterns, budget)
	if err != nil {
		return nil, 0, err
	}
	seen := make(map[string]struct{}, len(candidates))
	abs := make([]string, 0, len(candidates))
	excluded := 0
	for _, candidate := range candidates {
		if !includeIgnored {
			drop, derr := globMatchExcluded(ctx, fsys, ignores, candidate.path)
			if derr != nil {
				return nil, 0, derr
			}
			if drop {
				excluded += candidate.patterns
				continue
			}
		}
		p := filepath.Join(base, candidate.path)
		if masked != nil && masked(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		abs = append(abs, p)
	}
	return abs, excluded, nil
}
