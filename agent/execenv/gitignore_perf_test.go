package execenv

import (
	"path/filepath"
	"testing"

	gitignore "github.com/sabhiram/go-gitignore"
)

// BenchmarkGlobRepoTree measures Glob("agent/*.go")'s wall time against this
// repo's own worktree — the case I4 raised: loadIgnoreSet's own walk (run
// before every glob/grep to collect .gitignore rules) must not pay to
// descend into dot-directories (.git, .worktrees, .claude scratch dirs, ...),
// mirroring the dot-dir skip loadIgnoreSet's caller already applies when
// filtering matches. This worktree checkout has no node_modules or sibling
// .worktrees of its own, so the absolute numbers here are modest; a
// standalone timing comparison against the full repo root (which does carry
// a ~1GB .worktrees tree) showed the fix taking a >2s walk down to ~300ms —
// see the fix-wave report for those numbers.
func BenchmarkGlobRepoTree(b *testing.B) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatalf("abs: %v", err)
	}
	env := NewLocalExecutionEnvironment(repoRoot)
	for b.Loop() {
		matches, err := env.Glob(b.Context(), "agent/*.go", "")
		if err != nil {
			b.Fatalf("Glob: %v", err)
		}
		if len(matches) == 0 {
			b.Fatal("expected matches under agent/*.go")
		}
	}
}

// BenchmarkGlobBraceExpansion measures one brace-expanded glob against this
// repo's own tree: agent/**/*.{go,md} expands to two recursive patterns under
// one prefix, which is the shape the shared walk collapses into a single
// traversal. It reports the directory listings the call charged its budget,
// next to the wall time, because the listing count is what the change moves —
// 860 before, 344 after, over the 12,124 files and 846 directories of this
// repo — and unlike the wall time it does not move with host load.
func BenchmarkGlobBraceExpansion(b *testing.B) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatalf("abs: %v", err)
	}
	env := NewLocalExecutionEnvironment(repoRoot)
	const pattern = "agent/**/*.{go,md}"
	reportListings := func(b *testing.B, budget *GlobBudget, matches int) {
		b.Helper()
		if matches == 0 {
			b.Fatalf("expected matches under %s", pattern)
		}
		if at := budget.TruncatedAt(); at != 0 {
			b.Fatalf("call was truncated at budget %d, so its listing count is a floor rather than the walk's cost", at)
		}
		b.ReportMetric(float64(budget.listings), "listings/op")
		// Both arms must report the same value here: the shared walk changes
		// what is traversed, never what is found.
		b.ReportMetric(float64(matches), "matches/op")
	}

	b.Run("per-pattern", func(b *testing.B) {
		// The loop the shared walk replaces: one walk per expanded alternative,
		// each matching the filesystem alone and merging with a seen set.
		patterns, err := expandSearchPattern(pattern)
		if err != nil {
			b.Fatalf("expandSearchPattern: %v", err)
		}
		var last *GlobBudget
		var count int
		for b.Loop() {
			budget := NewGlobBudget()
			fsys := cancelFS{ctx: b.Context(), fsys: globBaseFS(b.Context(), repoRoot, budget)}
			ignores, err := loadIgnoreSet(b.Context(), fsys, nil, budget, ignoreScopeForPatterns(patterns))
			if err != nil {
				b.Fatalf("loadIgnoreSet: %v", err)
			}
			seen := make(map[string]struct{})
			for _, p := range patterns {
				found, err := globMatches(b.Context(), fsys, p, budget)
				if err != nil {
					b.Fatalf("globMatches(%q): %v", p, err)
				}
				for _, m := range found {
					drop, err := globMatchExcluded(b.Context(), fsys, ignores, m)
					if err != nil {
						b.Fatalf("globMatchExcluded: %v", err)
					}
					if drop {
						continue
					}
					seen[filepath.Join(repoRoot, m)] = struct{}{}
				}
			}
			last, count = budget, len(seen)
		}
		b.StopTimer()
		reportListings(b, last, count)
	})

	b.Run("shared-walk", func(b *testing.B) {
		var last *GlobBudget
		var count int
		for b.Loop() {
			budget := NewGlobBudget()
			matches, _, err := env.GlobWithBudget(b.Context(), pattern, "", false, budget)
			if err != nil {
				b.Fatalf("GlobWithBudget: %v", err)
			}
			last, count = budget, len(matches)
		}
		b.StopTimer()
		reportListings(b, last, count)
	})
}

// BenchmarkIgnoreMatcherReuse measures what the matcher cache buys, through the
// ENTRY POINT a tool call actually uses: env.GlobWithBudget. That distinction is
// load-bearing — an earlier version of this benchmark called
// loadIgnoreSetCached with a cache handed to it, which measured the helper and
// stayed green while no production call site passed a cache at all. It reports
// the matcher compiles per call, which is the load-independent evidence: this
// repo's six rules files cost six compiles on a cold call and none on a repeat
// over an unchanged tree.
//
// The two arms differ only in whether the environment keeps its cache: the
// "unwarmed" arm gets a fresh environment each iteration, which is what a
// session that has not searched this tree yet looks like, and the "reused" arm
// keeps one environment, so only its first call compiles.
func BenchmarkIgnoreMatcherReuse(b *testing.B) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatalf("abs: %v", err)
	}
	const pattern = "**/*.{go,md}"
	compiles := new(int)
	orig := compileIgnoreLines
	compileIgnoreLines = func(lines ...string) *gitignore.GitIgnore {
		*compiles++
		return orig(lines...)
	}
	defer func() { compileIgnoreLines = orig }()

	b.Run("cold-environment", func(b *testing.B) {
		for b.Loop() {
			*compiles = 0
			// A fresh environment per iteration: nothing is cached yet, which is
			// the cost a session paid on every call before this change.
			env := NewLocalExecutionEnvironment(repoRoot)
			if _, _, err := env.GlobWithBudget(b.Context(), pattern, "", false, NewGlobBudget()); err != nil {
				b.Fatalf("GlobWithBudget: %v", err)
			}
			b.ReportMetric(float64(*compiles), "compiles/op")
			b.ReportMetric(0, "listings/op")
		}
	})
	b.Run("reused-environment", func(b *testing.B) {
		env := NewLocalExecutionEnvironment(repoRoot)
		// Warm the cache before timing: this arm is a session whose rules files
		// are already compiled, so its per-op compile count is exactly zero.
		if _, _, err := env.GlobWithBudget(b.Context(), pattern, "", false, NewGlobBudget()); err != nil {
			b.Fatalf("warming GlobWithBudget: %v", err)
		}
		for b.Loop() {
			*compiles = 0
			if _, _, err := env.GlobWithBudget(b.Context(), pattern, "", false, NewGlobBudget()); err != nil {
				b.Fatalf("GlobWithBudget: %v", err)
			}
			b.ReportMetric(float64(*compiles), "compiles/op")
			b.ReportMetric(0, "listings/op")
		}
	})
}
