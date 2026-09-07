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
// so this constructs a secureDirFS directly instead — the same shape
// TestLoadIgnoreSetSkipsMaskedSubtree drives loadIgnoreSet through — over a
// budget the test owns, and reads peakDirEntries off it once the call
// returns. A bare errors.As/result check would also pass a listing that
// materializes the whole directory and only then reports the refusal, which
// is exactly the shape the OOM this bound exists for takes; peakDirEntries
// is what tells the two apart.
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
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: sfs, budget: callBudget}

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
