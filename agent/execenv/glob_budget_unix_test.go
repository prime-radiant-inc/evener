//go:build linux || darwin

package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestSandboxedGlobStopsOnADirectoryWithTooManyEntries is
// TestGlobStopsOnADirectoryWithTooManyEntries's sandboxed counterpart:
// secureDirFS.ReadDir also collects a directory in one df.ReadDir(-1) call
// before the budget or match cap get a say, so the same OOM shape exists on
// the sandboxed arm. sandboxFS.glob builds its secureDirFS internally, with no
// seam over its directory reads the way stubGlobBaseFS gives the plain path,
// so this constructs a secureDirFS directly instead, over a budget the test
// owns, and reads peakDirEntries off it once the call returns. A bare
// errors.As/result check would also pass a listing that materializes the
// whole directory and only then reports the refusal, which is exactly the
// shape the OOM this bound exists for takes; peakDirEntries is what tells the
// two apart.
func TestSandboxedGlobStopsOnADirectoryWithTooManyEntries(t *testing.T) {
	env, home, _ := sandboxedEnv(t, sandbox.ModeReadOnly)

	// A fresh subdirectory of its own, rather than home itself, so the
	// sandboxedEnv-created "project" worktree isn't an extra entry the
	// listing has to account for.
	bucket := filepath.Join(home, "bucket")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	const fileCount = 30
	for i := range fileCount {
		p := filepath.Join(bucket, fmt.Sprintf("leaf%03d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const budget = 10
	stubMaxGlobDirEntries(t, budget)
	stubGlobDirChunk(t, 5)

	sfs := env.sandbox()
	if sfs == nil {
		t.Fatal("expected a sandboxed environment")
	}
	// sandbox() hands back a held layer; the ReadDir call below runs on it, so
	// the release goes at the end of the operation, not here.
	defer sfs.release()
	baseFd, canonical, err := sfs.openReadBaseFd("glob", bucket)
	if err != nil {
		t.Fatalf("openReadBaseFd: %v", err)
	}
	defer func() { _ = unix.Close(baseFd) }()

	callBudget := newGlobBudget("glob")
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: sfs, budget: callBudget, ctx: t.Context()}

	entries, err := fsys.ReadDir(".")
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("secureDirFS.ReadDir over a %d-entry directory with an entry budget of %d = (%d entries, %v), want a *globBudgetError; nothing bounds how many entries one listing may materialize", fileCount, budget, len(entries), err)
	}
	if budgetErr.kind != budgetEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetEntries", budgetErr.kind)
	}
	if budgetErr.op != callBudget.op {
		t.Fatalf("globBudgetError.op = %q, want %q", budgetErr.op, callBudget.op)
	}
	if callBudget.peakDirEntries < budget || callBudget.peakDirEntries >= fileCount {
		t.Fatalf("globBudget.peakDirEntries = %d, want at least the entry budget of %d but strictly less than the directory's %d entries (a listing that materializes everything before refusing must not pass this)", callBudget.peakDirEntries, budget, fileCount)
	}
}

// TestSandboxedGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree is the
// sandboxed counterpart to
// TestGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree above.
// sandboxFS.glob has no live-entry accounting of its own — it gets the
// call-wide ceiling only because secureDirFS.ReadDir, like boundedDirFS on the
// plain path, lists through readDirChunked and charges holdEntries the same
// way. That inheritance is structural rather than exercised directly anywhere
// else on this arm, so a change that broke it — secureDirFS falling back to
// its whole-directory read, or a live-entry accounting bug that only surfaces
// through the fd-anchored resolver's own directory handles — would have
// nothing here to catch it before a real sandboxed session hit the same OOM
// the plain path's fix closes. This drives the identical nested-tree shape
// through env.GlobWithBudget, which dispatches to sandboxFS.glob once the
// environment is sandboxed, exercising the inheritance through the real entry
// point rather than by constructing secureDirFS directly the way the
// entries-cap sibling above does.
func TestSandboxedGlobStopsWhenTooManyEntriesAreHeldLiveAcrossADeepTree(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)

	const depth = 10   // d00..d09
	const perLevel = 5 // every directory in the chain holds this many entries
	const perDirBudget = 20
	const liveBudget = 30

	cur := worktree
	for i := range depth {
		cur = filepath.Join(cur, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(cur, 0o755); err != nil {
			t.Fatal(err)
		}
		// Every non-leaf directory holds perLevel-1 padding files plus the
		// subdirectory that continues the chain; the leaf holds perLevel
		// padding files and no subdirectory, so every level's own listing is
		// the same size.
		padding := perLevel - 1
		if i == depth-1 {
			padding = perLevel
		}
		for p := range padding {
			if err := os.WriteFile(filepath.Join(cur, fmt.Sprintf("pad%02d.txt", p)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	stubMaxGlobDirEntries(t, perDirBudget)
	stubMaxGlobLiveEntries(t, liveBudget)

	budget := NewGlobBudget()
	matches, _, err := env.GlobWithBudget(t.Context(), "**/*.txt", worktree, true, budget)
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("sandboxed glob over a %d-level tree with a live-entry budget of %d = (%d matches, %v), want a *globBudgetError; secureDirFS is not charging the call-wide live-entry ceiling the way boundedDirFS does on the plain path", depth, liveBudget, len(matches), err)
	}
	if budgetErr.kind != budgetLiveEntries {
		t.Fatalf("globBudgetError.kind = %v, want budgetLiveEntries", budgetErr.kind)
	}
}
