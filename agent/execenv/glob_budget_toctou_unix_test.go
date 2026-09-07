//go:build linux || darwin

package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// growDirWithFiles adds n more files to dir, named apart from any fixture's
// own files so growth never collides with them. It exists so the TOCTOU
// probes below can turn a directory that was under the entry budget when
// ignore discovery listed it into one that is over budget by the time the
// grep walk under test lists it a second time — the concurrent-growth shape
// the per-directory guard exists for, which no fixture built up front can
// produce (ignore discovery's skip set is always a subset of the walk's, so
// it always reaches an oversized directory first on a static tree).
func growDirWithFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("grown%04d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery
// forces the concurrent-growth case that no static fixture can produce.
// grepNative runs two independent passes over the same tree under the same
// budget: loadIgnoreSet's fs.WalkDir first, then grepWalk's own. Ignore
// discovery's skip set (dot-prefixed directories only) is always a subset of
// grepWalk's (which also skips gitignored ones), so on any fixture built up
// front ignore discovery always reaches an oversized directory first and
// grepWalk's own callback guard never gets a turn — that guard exists only
// for a directory that grows past maxGlobDirEntries in the gap between the
// two passes. This stubs grepWalk itself to open that exact gap: the stub
// grows the walk root only once grepWalk is invoked, which is after
// loadIgnoreSet has already listed the same root at its original, in-budget
// size, and only then delegates to the real fs.WalkDir. The now-oversized
// root's ReadDir raises the entries refusal from inside grepWalk's own
// listing, so a pass here can only mean grepNative's own callback guard — not
// ignore discovery's — carried it out instead of swallowing it as an
// unreadable entry.
func TestGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery(t *testing.T) {
	const fileCount = 3
	root := flatEntriesFixture(t, fileCount)

	const budget = 8
	stubMaxGlobDirEntries(t, budget)

	walk := grepWalk
	grepWalk = func(fsys fs.FS, name string, fn fs.WalkDirFunc) error {
		growDirWithFiles(t, root, 20)
		return walk(fsys, name, fn)
	}
	t.Cleanup(func() { grepWalk = walk })

	_, err := NewLocalExecutionEnvironment(root).grepNative(t.Context(), "needle", root, "", false, 100, "")
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("grepNative over a directory that grows past the entry budget of %d between ignore discovery's pass and grepWalk's own = %v, want a *globBudgetError from grepWalk's own callback guard", budget, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "grep" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "grep")
	}
}

// TestSandboxedGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery
// is the sandboxed counterpart: sandboxFS.grepNative runs the same two-pass
// shape as the off-sandbox arm — loadIgnoreSet walks the base once, then
// secureBrowseWalkDir walks it again under the same budget — so the same
// concurrent-growth gap exists and is just as unreachable from a static
// fixture. This stubs secureBrowseWalkDir the way the off-sandbox probe above
// stubs grepWalk: it grows the sandboxed worktree only once
// secureBrowseWalkDir is invoked, after loadIgnoreSet has already listed it
// at its original, in-budget size, then delegates to the real fs.WalkDir.
// Growing the tree through the os.WriteFile calls below reaches the
// filesystem directly, the same way the test's own fixture setup does; it is
// not an operation the sandboxed environment under test performs.
func TestSandboxedGrepWalkCarriesTheEntriesRefusalWhenADirectoryGrowsAfterIgnoreDiscovery(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)

	const fileCount = 3
	for i := range fileCount {
		p := filepath.Join(worktree, fmt.Sprintf("leaf%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const budget = 8
	stubMaxGlobDirEntries(t, budget)

	walk := secureBrowseWalkDir
	secureBrowseWalkDir = func(fsys fs.FS, name string, fn fs.WalkDirFunc) error {
		growDirWithFiles(t, worktree, 20)
		return walk(fsys, name, fn)
	}
	t.Cleanup(func() { secureBrowseWalkDir = walk })

	_, err := env.Grep(t.Context(), "needle", worktree, "", false, 100, "")
	budgetErr, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("sandboxed Grep over a directory that grows past the entry budget of %d between ignore discovery's pass and the walk's own = %v, want a *globBudgetError from grepNative's own callback guard", budget, err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != "grep" {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, "grep")
	}
}

// TestSandboxedGrepWalkDoesNotChargeSkippedDirectories is the sandboxed
// counterpart to TestGrepWalkDoesNotChargeSkippedDirectories: sandboxFS's own
// walk charges budget.listing(true) once per directory it actually descends
// into, after the masked/dot/gitignore fs.SkipDir checks earlier in the same
// callback, not before them. The fixture and budget here are the same
// arithmetic as the off-sandbox test, because loadIgnoreSet is the identical
// shared code on both arms: it does not yet know a directory is gitignored
// while it is still collecting the .gitignore rules that would exclude it, so
// it charges the base plus every real and gitignored directory (1 + 3 + 10 =
// 14 listings here) but skips only the dot-prefixed ones for free. The
// sandboxed walk then adds 4 more of its own — the base plus the 3 real
// directories it actually descends into — for a total of 18, comfortably
// under the budget of 25 and comfortably above ignore discovery's 14-listing
// cost alone, so headroom cannot hide a regression here. Moving the charge
// above the skip checks would instead start charging the 20 dot and 10
// gitignored directories the walk was about to skip anyway, which on their
// own are more than enough to cross the budget of 25 partway through (the
// walk gives up as soon as the running total does, well short of a
// legitimate 18): that is the only way this test can fail.
func TestSandboxedGrepWalkDoesNotChargeSkippedDirectories(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)

	const realCount = 3
	for i := range realCount {
		if err := os.MkdirAll(filepath.Join(worktree, fmt.Sprintf("dir%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "dir00", "leaf.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const dotCount = 20
	for i := range dotCount {
		if err := os.MkdirAll(filepath.Join(worktree, fmt.Sprintf(".excluded%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const gitignoredCount = 10
	gitignoreLines := make([]string, 0, gitignoredCount)
	for i := range gitignoredCount {
		dir := fmt.Sprintf("ignored%02d", i)
		if err := os.MkdirAll(filepath.Join(worktree, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		gitignoreLines = append(gitignoreLines, dir+"/")
	}
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte(strings.Join(gitignoreLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const budget = 25
	stubMaxGlobDirListings(t, budget)

	out, err := env.Grep(t.Context(), "needle", worktree, "", false, 100, "")
	if budgetErr, refused := errors.AsType[*globBudgetError](err); refused {
		t.Fatalf("sandboxed grepNative over a tree with %d dot-excluded and %d gitignored directories against only %d listed directories, with a listing budget of %d well above ignore discovery's own cost, refused: %v; the walk is charging directories it is about to skip toward the budget instead of only the ones it actually descends into", dotCount, gitignoredCount, realCount, budget, budgetErr)
	}
	if err != nil {
		t.Fatalf("sandboxed Grep: %v", err)
	}
	if !strings.Contains(out, "needle") {
		t.Fatalf("sandboxed Grep(%q) = %q, want a match for the needle in dir00/leaf.txt", "needle", out)
	}
}
