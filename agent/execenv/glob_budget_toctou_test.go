package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
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
