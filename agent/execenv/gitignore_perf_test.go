package execenv

import (
	"path/filepath"
	"testing"
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
		matches, err := env.Glob("agent/*.go", "")
		if err != nil {
			b.Fatalf("Glob: %v", err)
		}
		if len(matches) == 0 {
			b.Fatal("expected matches under agent/*.go")
		}
	}
}
