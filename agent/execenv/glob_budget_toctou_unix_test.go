//go:build linux || darwin

package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

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
