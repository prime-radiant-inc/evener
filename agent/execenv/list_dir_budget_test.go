package execenv

// Regression tests for issue #2375: directory pagination happened after an
// unbounded, uncancellable walk. The list_dir walk must be context-aware,
// bounded by an operation/entry budget shared with glob's GlobBudget
// machinery, and honest about partial totals: it stops once it has collected
// enough entries to answer the requested page (plus one, so it can tell that
// more exist) instead of enumerating the whole requested subtree first, and
// reports the cap it stopped at through TruncatedAt.
//
// Both arms are pinned here: the unconfined walk (local.go, reading raw OS
// paths through the bounded chunked reader) and the confined fd-anchored
// walk (securepath_fdops_unix.go). The legacy ListDirectory remains the oracle
// for completed walks: it is pinned by the existing tests and the fuzz
// oracles, so exact prefix equality against it proves the budgeted walk emits
// the same canonical listing, just stopping earlier.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// stubListDirRead interposes on the directory-listing seam the off-sandbox
// list_dir walk reads through, so a test can drive the exported entry point
// while counting — or cancelling at — each real listing, and restores the
// seam when the test ends. Not parallel: the seam is a package variable.
func stubListDirRead(t *testing.T, read func(ctx context.Context, absPath, dir string, budget *GlobBudget) ([]fs.DirEntry, error)) {
	t.Helper()
	orig := listDirRead
	listDirRead = read
	t.Cleanup(func() { listDirRead = orig })
}

// stubMaxListDirWalkEntries lowers the entry-visit cap of one list_dir walk
// for a test and restores it when the test ends, so a truncation case does
// not need a tree as large as the production cap. Not parallel: package
// variable.
func stubMaxListDirWalkEntries(t *testing.T, n int) {
	t.Helper()
	orig := maxListDirWalkEntries
	maxListDirWalkEntries = n
	t.Cleanup(func() { maxListDirWalkEntries = orig })
}

// writeListDirTree writes a two-file, six-subdirectory tree into root:
// top1.txt and top2.txt at the root, and dir00..dir05 each holding leaf.md
// and leaf.txt — 7 directories and 20 entries in all, so a paged walk's
// stopping points sit far from the completed-walk control run.
func writeListDirTree(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"top1.txt", "top2.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 6 {
		dir := filepath.Join(root, fmt.Sprintf("dir%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"leaf.md", "leaf.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func listDirTreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeListDirTree(t, root)
	return root
}

// writeListDirChain nests n one-directory levels (d01..d0n) under root, each
// holding one f.txt, so a depth clamp has an observable boundary: a walk
// capped at k levels stops inside the chain rather than at its natural end.
func writeListDirChain(t *testing.T, root string, n int) {
	t.Helper()
	dir := root
	for i := 1; i <= n; i++ {
		dir = filepath.Join(dir, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListDirBudgetCompletesASmallDirectoryExactlyLikeTheLegacyWalk pins the
// acceptance case that a normal small directory behaves exactly as today:
// with a budget generous enough to let the walk finish, the budgeted listing
// is entry-for-entry the legacy ListDirectory's — same names, sizes, mod
// times, type flags — and no truncation is reported.
func TestListDirBudgetCompletesASmallDirectoryExactlyLikeTheLegacyWalk(t *testing.T) {
	t.Parallel()
	root := listDirTreeFixture(t)
	env := NewLocalExecutionEnvironment(root)

	legacy, err := env.ListDirectory(root, 4)
	if err != nil {
		t.Fatalf("legacy ListDirectory: %v", err)
	}
	budget := NewDirListBudget(0, 1000, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 4, budget)
	if err != nil {
		t.Fatalf("budgeted walk over a small tree: %v", err)
	}
	if !slices.Equal(got, legacy) {
		t.Fatalf("budgeted walk = %v, want exactly the legacy listing %v", got, legacy)
	}
	if budget.TruncatedAt() != 0 {
		t.Fatalf("a walk that enumerated the whole tree reported truncatedAt=%d, want 0", budget.TruncatedAt())
	}
}

// TestListDirBudgetStopsWalkingOnceThePageIsFull is the issue's limit=1
// regression proof for the unconfined arm: the walk must stop once it has
// collected enough entries to answer the page — one page entry plus the one
// that proves more exist — rather than enumerating the whole subtree. The
// instrumented listing seam counts directory listings: a completed walk
// over this fixture lists all 7 directories, a limit=1 page must stop after 2
// (the root and the first subdirectory) holding the exact legacy prefix.
func TestListDirBudgetStopsWalkingOnceThePageIsFull(t *testing.T) {
	root := listDirTreeFixture(t)
	env := NewLocalExecutionEnvironment(root)
	legacy, err := env.ListDirectory(root, 4)
	if err != nil {
		t.Fatalf("legacy ListDirectory: %v", err)
	}

	var control int
	stubListDirRead(t, func(ctx context.Context, absPath, dir string, budget *GlobBudget) ([]fs.DirEntry, error) {
		control++
		return listDirReadOS(ctx, absPath, dir, budget)
	})
	if _, err := env.ListDirectoryWithBudget(t.Context(), root, 4, NewDirListBudget(0, 1000, 0)); err != nil {
		t.Fatalf("control run: %v", err)
	}
	if control != 7 {
		t.Fatalf("control run made %d listings, want 7 (every directory in the fixture)", control)
	}

	var capped int
	stubListDirRead(t, func(ctx context.Context, absPath, dir string, budget *GlobBudget) ([]fs.DirEntry, error) {
		capped++
		return listDirReadOS(ctx, absPath, dir, budget)
	})
	budget := NewDirListBudget(0, 1, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 4, budget)
	if err != nil {
		t.Fatalf("limit=1 page: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=1 walk collected %d entries, want 2 (the page entry plus the one that proves more exist)", len(got))
	}
	if !slices.Equal(got, legacy[:2]) {
		t.Fatalf("limit=1 walk = %v, want the legacy prefix %v", got, legacy[:2])
	}
	if budget.TruncatedAt() != 2 {
		t.Fatalf("limit=1 walk reported truncatedAt=%d, want 2", budget.TruncatedAt())
	}
	if capped != 2 {
		t.Fatalf("limit=1 walk made %d directory listings, want 2 (the root and the first subdirectory)", capped)
	}
	if capped >= control {
		t.Fatalf("limit=1 walk listed %d directories, want fewer than the completed walk's %d", capped, control)
	}
}

// TestListDirBudgetPagesCoherentlyAcrossCalls pins the partial-total contract:
// every page is a prefix of the one canonical listing — the exhaustive walk
// an unbounded list_dir would have produced — extended only as far as that
// page's need, so consecutive pages hand the model consecutive slices without
// gaps or repeats. The final page completes the walk and reports no
// truncation.
func TestListDirBudgetPagesCoherentlyAcrossCalls(t *testing.T) {
	t.Parallel()
	root := listDirTreeFixture(t)
	env := NewLocalExecutionEnvironment(root)
	legacy, err := env.ListDirectory(root, 4)
	if err != nil {
		t.Fatalf("legacy ListDirectory: %v", err)
	}
	for _, page := range []struct{ offset, limit int }{
		{0, 3}, {3, 3}, {6, 3}, {9, 3}, {12, 3}, {15, 3}, {18, 3},
	} {
		budget := NewDirListBudget(page.offset, page.limit, 0)
		got, err := env.ListDirectoryWithBudget(t.Context(), root, 4, budget)
		if err != nil {
			t.Fatalf("page (offset=%d, limit=%d): %v", page.offset, page.limit, err)
		}
		end := min(page.offset+page.limit+1, len(legacy))
		if !slices.Equal(got, legacy[page.offset:end]) {
			t.Fatalf("page (offset=%d, limit=%d) = %v, want the canonical listing's window %v", page.offset, page.limit, got, legacy[page.offset:end])
		}
		wantTruncatedAt := 0
		if end < len(legacy) {
			wantTruncatedAt = end
		}
		if budget.TruncatedAt() != wantTruncatedAt {
			t.Fatalf("page (offset=%d, limit=%d) reported truncatedAt=%d, want %d", page.offset, page.limit, budget.TruncatedAt(), wantTruncatedAt)
		}
		if wantTruncatedAt == 0 && budget.Visited() != len(legacy) {
			t.Fatalf("completed page (offset=%d, limit=%d) visited %d entries, want the subtree's exhaustive %d", page.offset, page.limit, budget.Visited(), len(legacy))
		}
	}
}

// TestListDirBudgetStopsOnCancellationMidWalk is the issue's cancellation
// regression proof for the unconfined arm: cancelling the context mid-walk
// must abort the traversal and surface the cancellation, not let the walk
// run to completion. The instrumented listing seam cancels on its second
// listing, partway through the fixture.
func TestListDirBudgetStopsOnCancellationMidWalk(t *testing.T) {
	root := listDirTreeFixture(t)
	env := NewLocalExecutionEnvironment(root)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	listings := 0
	stubListDirRead(t, func(cctx context.Context, absPath, dir string, budget *GlobBudget) ([]fs.DirEntry, error) {
		listings++
		if listings == 2 {
			cancel()
		}
		return listDirReadOS(cctx, absPath, dir, budget)
	})
	entries, err := env.ListDirectoryWithBudget(ctx, root, 4, NewDirListBudget(0, 1000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-walk cancellation = (%v, %v), want context.Canceled", entries, err)
	}
	if listings > 2 {
		t.Fatalf("walk kept going after cancellation: %d listings", listings)
	}
}

// TestListDirBudgetClampsModelSuppliedDepth pins the depth bound: depth is
// model-supplied and previously unbounded, so the budgeted walk clamps it to
// MaxListDirDepth — a depth of 1000 over a 24-level chain returns exactly the
// depth-16 listing, and depth <= 0 still means 1 as the legacy walk treats it.
func TestListDirBudgetClampsModelSuppliedDepth(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeListDirChain(t, root, 24)
	env := NewLocalExecutionEnvironment(root)

	want, err := env.ListDirectory(root, MaxListDirDepth)
	if err != nil {
		t.Fatalf("legacy walk at the clamp: %v", err)
	}
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 1000, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("budgeted walk at depth 1000: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("depth 1000 = %d entries, want the clamped depth-%d listing of %d", len(got), MaxListDirDepth, len(want))
	}
	unclamped, err := env.ListDirectory(root, 24)
	if err != nil {
		t.Fatalf("legacy walk past the clamp: %v", err)
	}
	if slices.Equal(got, unclamped) {
		t.Fatalf("depth 1000 walked past the clamp: %d entries, same as the full depth-24 chain", len(got))
	}
	shallow, err := env.ListDirectory(root, 1)
	if err != nil {
		t.Fatalf("legacy shallow walk: %v", err)
	}
	zeroDepth, err := env.ListDirectoryWithBudget(t.Context(), root, 0, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("budgeted walk at depth 0: %v", err)
	}
	if !slices.Equal(zeroDepth, shallow) {
		t.Fatalf("depth 0 = %v, want the legacy depth-1 listing %v", zeroDepth, shallow)
	}
}

// TestListDirBudgetStopsAtTheWalkEntriesCap pins the walk's own ceiling: a
// caller asking for an enormous page must not get an unbounded walk — the
// page need is clamped to the walk's entry-visit cap, which reports through
// TruncatedAt exactly like a page-sized need does.
func TestListDirBudgetStopsAtTheWalkEntriesCap(t *testing.T) {
	stubMaxListDirWalkEntries(t, 6)
	root := listDirTreeFixture(t)
	env := NewLocalExecutionEnvironment(root)
	legacy, err := env.ListDirectory(root, 4)
	if err != nil {
		t.Fatalf("legacy ListDirectory: %v", err)
	}
	budget := NewDirListBudget(0, 1000, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 4, budget)
	if err != nil {
		t.Fatalf("capped walk: %v", err)
	}
	if len(got) != 6 || budget.TruncatedAt() != 6 {
		t.Fatalf("walk with limit 1000 against a cap of 6 = (%d entries, truncatedAt=%d), want (6, 6)", len(got), budget.TruncatedAt())
	}
	if !slices.Equal(got, legacy[:6]) {
		t.Fatalf("capped walk = %v, want the legacy prefix %v", got, legacy[:6])
	}
}

// TestListDirBudgetConstructorClampsOffsetAndLimit pins the arithmetic that
// turns a page request into the walk's skip and window: the offset is
// charged-and-discarded prefix however deep it is, the retained window is
// limit+1 clamped to the walk's cap, and no model-supplied extreme overflows
// either.
func TestListDirBudgetConstructorClampsOffsetAndLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		offset, limit, charBudget   int
		wantSkip, wantWindow, wantC int
	}{
		{offset: 0, limit: 1, wantSkip: 0, wantWindow: 2},
		{offset: -5, limit: 0, wantSkip: 0, wantWindow: 2},
		{offset: 4, limit: 2, wantSkip: 4, wantWindow: 3},
		{offset: 0, limit: maxListDirWalkEntries, wantSkip: 0, wantWindow: maxListDirWalkEntries},
		{offset: maxListDirWalkEntries - 2, limit: 5, wantSkip: maxListDirWalkEntries - 2, wantWindow: 6},
		{offset: maxListDirWalkEntries, limit: 5, wantSkip: maxListDirWalkEntries, wantWindow: 6},
		{offset: math.MaxInt, limit: math.MaxInt, wantSkip: math.MaxInt, wantWindow: maxListDirWalkEntries},
		{offset: 0, limit: 1, charBudget: -7, wantSkip: 0, wantWindow: 2, wantC: 0},
	} {
		b := NewDirListBudget(tc.offset, tc.limit, tc.charBudget)
		if b.skip != tc.wantSkip || b.window != tc.wantWindow || b.charBudget != tc.wantC {
			t.Fatalf("NewDirListBudget(offset=%d, limit=%d, charBudget=%d) = (skip=%d, window=%d, charBudget=%d), want (%d, %d, %d)",
				tc.offset, tc.limit, tc.charBudget, b.skip, b.window, b.charBudget, tc.wantSkip, tc.wantWindow, tc.wantC)
		}
	}
}

// TestListDirBudgetRefusesOnePathologicalDirectory pins the per-directory
// bound on the unconfined arm: one directory too large to materialize is
// refused through the same globBudgetError glob raises, visibly, rather than
// read whole.
func TestListDirBudgetRefusesOnePathologicalDirectory(t *testing.T) {
	stubMaxGlobDirEntries(t, 10)
	root := flatEntriesFixture(t, 30)
	env := NewLocalExecutionEnvironment(root)
	entries, err := env.ListDirectoryWithBudget(t.Context(), root, 1, NewDirListBudget(0, 1000, 0))
	refusal, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("listing a 30-entry directory against a per-directory budget of 10 = (%d entries, %v), want a *globBudgetError", len(entries), err)
	}
	if refusal.op != "list_dir" || refusal.kind != budgetEntries {
		t.Fatalf("refusal = (op=%q, kind=%v), want (list_dir, budgetEntries)", refusal.op, refusal.kind)
	}
}

// TestListDirBudgetRefusalAdviceNamesTheListDirLever pins the refusal's
// model-facing advice: a list_dir call has no pattern to tighten and no
// .gitignore to read, so its lever must be the path or the depth, not the
// glob "pattern" advice that would otherwise leak into list_dir's errors.
func TestListDirBudgetRefusalAdviceNamesTheListDirLever(t *testing.T) {
	t.Parallel()
	entries := &globBudgetError{op: "list_dir", kind: budgetEntries}
	if entries.entryAdvice() == (&globBudgetError{op: "glob", kind: budgetEntries}).entryAdvice() {
		t.Fatalf("entryAdvice collapsed the list_dir/glob distinction: list_dir cannot spell a pattern that lists less, so it needs its own lever")
	}
	listings := &globBudgetError{op: "list_dir", kind: budgetListings}
	if listings.advice() == (&globBudgetError{op: "glob", kind: budgetListings}).advice() {
		t.Fatalf("advice collapsed the list_dir/glob distinction: a list_dir call has no pattern to narrow")
	}
}

// TestListDirListingNameIsTheSlashKeyTheAncestorLogicRecognizes pins the
// budget-key half of the walk's accounting: the name a listing is charged
// under must be the slash-separated form isGlobWalkAncestor matches on, with
// the walk's root charging as "." — the ancestor of everything. The
// Windows-native half of the contract (a relPrefix like "a\b" crossing
// through filepath.ToSlash into "a/b" for the key alone) is the windows
// crossbuild's to compile and a Windows runner's to execute; a non-Windows
// runner never produces a backslashed prefix to feed it.
func TestListDirListingNameIsTheSlashKeyTheAncestorLogicRecognizes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ relPrefix, want string }{
		{"", "."},
		{"dir00", "dir00"},
		{"dir00/leaf.md", "dir00/leaf.md"},
	} {
		if got := listDirListingName(tc.relPrefix); got != tc.want {
			t.Fatalf("listDirListingName(%q) = %q, want %q", tc.relPrefix, got, tc.want)
		}
	}
	if !isGlobWalkAncestor(listDirListingName(""), listDirListingName("dir00")) {
		t.Fatalf("the root's listing name %q must be an ancestor key of %q", listDirListingName(""), listDirListingName("dir00"))
	}
	if !isGlobWalkAncestor("dir00", listDirListingName("dir00/leaf.md")) {
		t.Fatalf("the nested listing name %q lost its ancestor %q", listDirListingName("dir00/leaf.md"), "dir00")
	}
}

// TestSandboxedListDirBudgetMatchesTheLegacyWalkExactly pins the confined
// arm's parity: with a budget generous enough to finish, the fd-anchored
// budgeted walk preserves the legacy walk's semantics exactly — masked
// subtrees skipped, symlinks reported but never descended, sizes and mod
// times resolved beneath the directory fd.
func TestSandboxedListDirBudgetMatchesTheLegacyWalkExactly(t *testing.T) {
	t.Parallel()
	env, home, worktree := sandboxedEnvWithDenylist(t, sandbox.ModeReadOnly, filepath.Join("~", "vault"))
	vault := filepath.Join(home, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"project/keep.txt", "project/sub/x.txt", "project/sub/.hidden/y.txt", "project/.dotfile"} {
		path := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(worktree, "keep.txt"), filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}

	legacy, err := env.ListDirectory(home, 3)
	if err != nil {
		t.Fatalf("legacy confined walk: %v", err)
	}
	budget := NewDirListBudget(0, 1000, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), home, 3, budget)
	if err != nil {
		t.Fatalf("budgeted confined walk: %v", err)
	}
	if !slices.Equal(got, legacy) {
		t.Fatalf("budgeted confined walk = %v, want exactly the legacy confined listing %v", got, legacy)
	}
	if budget.TruncatedAt() != 0 {
		t.Fatalf("a confined walk that enumerated the whole tree reported truncatedAt=%d, want 0", budget.TruncatedAt())
	}
	for _, e := range got {
		if strings.HasPrefix(e.Name, "vault") {
			t.Fatalf("masked subtree leaked into the confined listing: %q", e.Name)
		}
	}
}

// TestSandboxedListDirBudgetStopsWalkingOnceThePageIsFull is the issue's
// limit=1 regression proof for the confined arm: the fd-anchored walk stops
// after the root and the first subdirectory — 2 of the fixture's 7 listings,
// read off the budget's own accounting — holding the exact legacy prefix.
func TestSandboxedListDirBudgetStopsWalkingOnceThePageIsFull(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeListDirTree(t, worktree)
	legacy, err := env.ListDirectory(worktree, 4)
	if err != nil {
		t.Fatalf("legacy confined walk: %v", err)
	}
	budget := NewDirListBudget(0, 1, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 4, budget)
	if err != nil {
		t.Fatalf("limit=1 confined page: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=1 confined walk collected %d entries, want 2", len(got))
	}
	if !slices.Equal(got, legacy[:2]) {
		t.Fatalf("limit=1 confined walk = %v, want the legacy prefix %v", got, legacy[:2])
	}
	if budget.TruncatedAt() != 2 {
		t.Fatalf("limit=1 confined walk reported truncatedAt=%d, want 2", budget.TruncatedAt())
	}
	if budget.listings != 2 {
		t.Fatalf("limit=1 confined walk listed %d directories, want 2 (the root and the first subdirectory)", budget.listings)
	}
}

// TestSandboxedListDirBudgetStopsOnCancellationMidWalk is the issue's
// cancellation regression proof for the confined arm: cancelling the context
// mid-traversal aborts the fd-anchored walk and surfaces the cancellation.
// The budget's onListing seam cancels on the second listing, after the walk
// has already collected the root's entries.
func TestSandboxedListDirBudgetStopsOnCancellationMidWalk(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeListDirTree(t, worktree)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	budget := NewDirListBudget(0, 1000, 0)
	listings := 0
	budget.onListing = func() {
		listings++
		if listings == 2 {
			cancel()
		}
	}
	entries, err := env.ListDirectoryWithBudget(ctx, worktree, 4, budget)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-walk cancellation = (%v, %v), want context.Canceled", entries, err)
	}
}

// TestSandboxedListDirBudgetRefusesOnePathologicalDirectory pins the
// per-directory bound on the confined arm: a single directory too large to
// materialize is refused through the shared chunked reader's budget, not
// read whole off its fd.
func TestSandboxedListDirBudgetRefusesOnePathologicalDirectory(t *testing.T) {
	stubMaxGlobDirEntries(t, 10)
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	flat := filepath.Join(worktree, "flat")
	if err := os.MkdirAll(flat, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if err := os.WriteFile(filepath.Join(flat, fmt.Sprintf("leaf%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := env.ListDirectoryWithBudget(t.Context(), flat, 1, NewDirListBudget(0, 1000, 0))
	refusal, refused := errors.AsType[*globBudgetError](err)
	if !refused {
		t.Fatalf("confined listing of a 30-entry directory against a per-directory budget of 10 = (%d entries, %v), want a *globBudgetError", len(entries), err)
	}
	if refusal.op != "list_dir" || refusal.kind != budgetEntries {
		t.Fatalf("refusal = (op=%q, kind=%v), want (list_dir, budgetEntries)", refusal.op, refusal.kind)
	}
}

// TestSandboxedListDirBudgetClampsModelSuppliedDepth pins the depth bound on
// the confined arm: a model-supplied depth of 1000 returns exactly the
// depth-MaxListDirDepth listing over a deeper chain.
func TestSandboxedListDirBudgetClampsModelSuppliedDepth(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeListDirChain(t, worktree, 24)
	want, err := env.ListDirectory(worktree, MaxListDirDepth)
	if err != nil {
		t.Fatalf("legacy confined walk at the clamp: %v", err)
	}
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 1000, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("budgeted confined walk at depth 1000: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("depth 1000 = %d entries, want the clamped depth-%d confined listing of %d", len(got), MaxListDirDepth, len(want))
	}
}

// TestListDirBudgetNamesTheRequestedPathInRootErrors pins the fix-round
// finding for #2375: a root-listing failure must name the requested path. The
// first budgeted walk was fs-rooted and reported "open .: no such file or
// directory", which told the model nothing about which of its paths was
// wrong; the walk now reads absolute OS paths, whose listing errors name the
// path exactly the legacy walk's raw paths did. The underlying error (here
// fs.ErrNotExist) must stay preserved for errors.Is.
func TestListDirBudgetNamesTheRequestedPathInRootErrors(t *testing.T) {
	t.Parallel()
	const missing = "/definitely-not-here-xyz"
	env := NewLocalExecutionEnvironment(t.TempDir())

	_, err := env.ListDirectoryWithBudget(t.Context(), missing, 1, NewDirListBudget(0, 1000, 0))
	if err == nil {
		t.Fatal("listing a missing directory succeeded")
	}
	var perr *fs.PathError
	if !errors.As(err, &perr) || perr.Path != missing {
		t.Fatalf("missing-root error = %q, want a *fs.PathError naming the requested path %q", err, missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing-root error = %q, want fs.ErrNotExist preserved through the rewritten path", err)
	}
	_, legacyErr := env.ListDirectory(missing, 1)
	if legacyErr == nil || err.Error() != legacyErr.Error() {
		t.Fatalf("missing-root error = %q, want exactly the legacy walk's %q", err, legacyErr)
	}
}

// TestSandboxedListDirBudgetNamesTheRequestedPathInRootErrors pins the same
// contract for the confined arm: the model must see which path it got wrong.
// The legacy confined walk returns the resolver's bare errno for a missing
// root; the budgeted walk — the only one the tool calls — wraps it with the
// requested path, matching the unconfined arm's message shape, while the
// underlying error stays inspectable.
func TestSandboxedListDirBudgetNamesTheRequestedPathInRootErrors(t *testing.T) {
	t.Parallel()
	const missing = "/definitely-not-here-xyz"
	env, _, _ := sandboxedEnv(t, sandbox.ModeReadOnly)

	_, err := env.ListDirectoryWithBudget(t.Context(), missing, 1, NewDirListBudget(0, 1000, 0))
	if err == nil {
		t.Fatal("confined listing of a missing directory succeeded")
	}
	var perr *fs.PathError
	if !errors.As(err, &perr) || perr.Path != missing {
		t.Fatalf("confined missing-root error = %q, want a *fs.PathError naming the requested path %q", err, missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("confined missing-root error = %q, want fs.ErrNotExist preserved through the wrap", err)
	}
}

// TestListDirBudgetReachesPagesPastTheWalkEntriesCap pins the deep-offset
// half of the window contract: the walk's entry cap bounds what is RETAINED —
// the page window — not what is visited, so the offset prefix is charged and
// discarded instead of clamping the walk short of the page. A flat directory
// bigger than a shrunk cap must still answer a page from beyond the cap.
func TestListDirBudgetReachesPagesPastTheWalkEntriesCap(t *testing.T) {
	stubMaxListDirWalkEntries(t, 6)
	root := flatEntriesFixture(t, 20)
	env := NewLocalExecutionEnvironment(root)
	legacy, err := env.ListDirectory(root, 1)
	if err != nil {
		t.Fatalf("legacy ListDirectory: %v", err)
	}

	budget := NewDirListBudget(8, 1, 0) // offset past the shrunk cap of 6
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 1, budget)
	if err != nil {
		t.Fatalf("offset-past-the-cap page: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("offset-past-the-cap page collected %d entries, want the page entry plus its lookahead (the prefix is charged, not held)", len(got))
	}
	if !slices.Equal(got, legacy[8:10]) {
		t.Fatalf("offset-past-the-cap page = %v, want the canonical listing's window %v", got, legacy[8:10])
	}
	if budget.TruncatedAt() != 10 {
		t.Fatalf("offset-past-the-cap page reported truncatedAt=%d, want 10 (the visit position where the walk stopped: 8 skipped plus the 2-entry window)", budget.TruncatedAt())
	}
}

// TestSandboxedListDirBudgetReachesPagesPastTheWalkEntriesCap is the
// deep-offset regression for the confined arm.
func TestSandboxedListDirBudgetReachesPagesPastTheWalkEntriesCap(t *testing.T) {
	stubMaxListDirWalkEntries(t, 6)
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	for i := range 20 {
		if err := os.WriteFile(filepath.Join(worktree, fmt.Sprintf("leaf%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := env.ListDirectory(worktree, 1)
	if err != nil {
		t.Fatalf("legacy confined walk: %v", err)
	}

	budget := NewDirListBudget(8, 1, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 1, budget)
	if err != nil {
		t.Fatalf("confined offset-past-the-cap page: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("confined offset-past-the-cap page collected %d entries, want the page entry plus its lookahead", len(got))
	}
	if !slices.Equal(got, legacy[8:10]) {
		t.Fatalf("confined offset-past-the-cap page = %v, want the canonical listing's window %v", got, legacy[8:10])
	}
	if budget.TruncatedAt() != 10 {
		t.Fatalf("confined offset-past-the-cap page reported truncatedAt=%d, want 10", budget.TruncatedAt())
	}
}

// listDirCharWindowFixture builds the character-budget scenario: a root of
// eight short files followed by an oversized zz/ (bigCount files), with the
// plain files sorting first so a page that fills its character budget ends
// among them, the oversized directory still unvisited ahead.
func listDirCharWindowFixture(t *testing.T, root string, bigCount int) {
	t.Helper()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt", "g.txt", "h.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	zz := filepath.Join(root, "zz")
	if err := os.MkdirAll(zz, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range bigCount {
		if err := os.WriteFile(filepath.Join(zz, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListDirBudgetStopsAtTheRenderedCharacterBudget pins the character half
// of the window contract on the unconfined arm: the walk must stop once the
// page's rendered size is full plus one lookahead, exactly where the rendered
// page will cut — so an oversized directory beyond the rendered page is never
// opened and cannot fail a call whose page is already complete.
func TestListDirBudgetStopsAtTheRenderedCharacterBudget(t *testing.T) {
	stubMaxGlobDirEntries(t, 10)
	root := t.TempDir()
	listDirCharWindowFixture(t, root, 30)
	env := NewLocalExecutionEnvironment(root)

	// Each 5-character name renders as 21 budgeted characters, so a
	// 100-character page fits four entries and retains the fifth as its
	// lookahead; zz/ sits beyond both and must never be opened.
	budget := NewDirListBudget(0, 1000, 100)
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 2, budget)
	if err != nil {
		t.Fatalf("a page the character budget already filled must not be failed by the directory beyond it: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("character-budgeted walk collected %d entries, want the 4-entry page plus its lookahead", len(got))
	}
	if got[4].Name != "e.txt" {
		t.Fatalf("the character budget's lookahead = %q, want the 5th file", got[4].Name)
	}
	if budget.TruncatedAt() != 5 {
		t.Fatalf("character-budgeted walk reported truncatedAt=%d, want 5 (the visit position where the rendered page filled)", budget.TruncatedAt())
	}
	if budget.listings != 1 {
		t.Fatalf("the walk listed %d directories, want 1 (the root only; the oversized directory beyond the rendered page must not be opened)", budget.listings)
	}
}

// TestSandboxedListDirBudgetStopsAtTheRenderedCharacterBudget is the
// character-budget regression for the confined arm.
func TestSandboxedListDirBudgetStopsAtTheRenderedCharacterBudget(t *testing.T) {
	stubMaxGlobDirEntries(t, 10)
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	listDirCharWindowFixture(t, worktree, 30)

	budget := NewDirListBudget(0, 1000, 100)
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 2, budget)
	if err != nil {
		t.Fatalf("a confined page the character budget already filled must not be failed by the directory beyond it: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("confined character-budgeted walk collected %d entries, want the 4-entry page plus its lookahead", len(got))
	}
	if budget.TruncatedAt() != 5 {
		t.Fatalf("confined character-budgeted walk reported truncatedAt=%d, want 5", budget.TruncatedAt())
	}
	if budget.listings != 1 {
		t.Fatalf("the confined walk listed %d directories, want 1 (the root only; the oversized directory beyond the rendered page must not be opened)", budget.listings)
	}
}

// writeListDirBadName builds the non-UTF-8 scenario: a root holding plain.txt
// and a directory whose name contains a byte sequence that is a legal Linux
// filename but not valid UTF-8, which io/fs path validation rejects. The
// names are written through raw OS calls — os.Mkdir and os.WriteFile with the
// byte string — because fs helpers would refuse the name just as io/fs does.
func writeListDirBadName(t *testing.T, root string) {
	t.Helper()
	// "bad" plus one raw 0xff byte, built from a byte slice so the invalid
	// byte is spelled as a byte rather than an escape a reader must decode.
	badName := "bad" + string([]byte{0xff})
	bad := filepath.Join(root, badName)
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListDirBudgetListsDirectoriesWhoseNamesAreNotValidUTF8 pins the
// traverse a directory whose name io/fs path validation rejects, exactly as
// the legacy walk's raw OS paths did, instead of failing the whole page on
// it.
func TestListDirBudgetListsDirectoriesWhoseNamesAreNotValidUTF8(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeListDirBadName(t, root)
	env := NewLocalExecutionEnvironment(root)

	legacy, err := env.ListDirectory(root, 2)
	if err != nil {
		t.Fatalf("legacy walk over a non-UTF-8 name: %v", err)
	}
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 2, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("a recursive listing must traverse a non-UTF-8 directory name the way the legacy walk does, not error on it: %v", err)
	}
	if !slices.Equal(got, legacy) {
		t.Fatalf("budgeted walk = %v, want exactly the legacy listing %v", got, legacy)
	}
	if !slices.ContainsFunc(got, func(e DirEntry) bool { return e.Name == "bad\xff" && e.IsDir }) {
		t.Fatalf("the non-UTF-8 directory is missing from the listing: %v", got)
	}
}

// TestSandboxedListDirBudgetListsDirectoriesWhoseNamesAreNotValidUTF8 pins
// the same contract for the confined arm, which reads through descriptors and
// openat with raw names and never consults io/fs path validation — so a
// non-UTF-8 name must list identically on both walks.
func TestSandboxedListDirBudgetListsDirectoriesWhoseNamesAreNotValidUTF8(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	writeListDirBadName(t, worktree)

	legacy, err := env.ListDirectory(worktree, 2)
	if err != nil {
		t.Fatalf("legacy confined walk over a non-UTF-8 name: %v", err)
	}
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 2, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("confined budgeted walk over a non-UTF-8 name: %v", err)
	}
	if !slices.Equal(got, legacy) {
		t.Fatalf("confined budgeted walk = %v, want exactly the legacy confined listing %v", got, legacy)
	}
}

// listDirLookaheadFixture builds the reviewer's #2375 scenario: a root holding
// a.txt and b/, where b holds more files than the per-directory entry budget
// a test shrinks to. A page whose need is met at b — a.txt collected, b
// collected as the lookahead entry that proves more exist — still has b's
// listing ahead of it, so the walkers must stop before opening b rather than
// let its oversized listing fail a call whose page is already valid.
func listDirLookaheadFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "b")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if err := os.WriteFile(filepath.Join(big, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListDirBudgetKeepsThePageWhenTheLookaheadDirectoryIsTooBig pins the
// no-fail-closed half of the budget contract on the unconfined arm: once the
// page need is met, a subdirectory that would never be shown must not be
// opened either — an oversized one must not turn a valid page into a
// globBudgetError.
func TestListDirBudgetKeepsThePageWhenTheLookaheadDirectoryIsTooBig(t *testing.T) {
	stubMaxGlobDirEntries(t, 5)
	root := t.TempDir()
	listDirLookaheadFixture(t, root)
	env := NewLocalExecutionEnvironment(root)

	budget := NewDirListBudget(0, 1, 0) // need 2: the page entry plus the lookahead
	got, err := env.ListDirectoryWithBudget(t.Context(), root, 2, budget)
	if err != nil {
		t.Fatalf("a page already collected must not be failed by a lookahead directory: %v", err)
	}
	var names []string
	for _, e := range got {
		names = append(names, filepath.ToSlash(e.Name))
	}
	if want := []string{"a.txt", "b"}; !slices.Equal(names, want) {
		t.Fatalf("limit=1 page = %v, want %v", names, want)
	}
	if budget.TruncatedAt() != 2 {
		t.Fatalf("lookahead walk reported truncatedAt=%d, want 2", budget.TruncatedAt())
	}
	if budget.listings != 1 {
		t.Fatalf("the walk listed %d directories, want 1 (the root only; a need-met walk must not open the lookahead)", budget.listings)
	}
}

// TestSandboxedListDirBudgetKeepsThePageWhenTheLookaheadDirectoryIsTooBig is
// the same regression for the confined arm: the fd-anchored walk must not
// openat a subdirectory once the page need is met, so an oversized one cannot
// fail a call whose page is already in hand.
func TestSandboxedListDirBudgetKeepsThePageWhenTheLookaheadDirectoryIsTooBig(t *testing.T) {
	stubMaxGlobDirEntries(t, 5)
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	listDirLookaheadFixture(t, worktree)

	budget := NewDirListBudget(0, 1, 0)
	got, err := env.ListDirectoryWithBudget(t.Context(), worktree, 2, budget)
	if err != nil {
		t.Fatalf("a confined page already collected must not be failed by a lookahead directory: %v", err)
	}
	var names []string
	for _, e := range got {
		names = append(names, filepath.ToSlash(e.Name))
	}
	if want := []string{"a.txt", "b"}; !slices.Equal(names, want) {
		t.Fatalf("confined limit=1 page = %v, want %v", names, want)
	}
	if budget.TruncatedAt() != 2 {
		t.Fatalf("confined lookahead walk reported truncatedAt=%d, want 2", budget.TruncatedAt())
	}
	if budget.listings != 1 {
		t.Fatalf("the confined walk listed %d directories, want 1 (the root only; a need-met walk must not openat the lookahead)", budget.listings)
	}
}
