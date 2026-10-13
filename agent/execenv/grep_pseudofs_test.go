package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// stubPseudoFsRoots points the pseudo-filesystem skip at roots for the
// duration of a test and restores the production list when it ends, so a
// fixture tree can exercise the real walk-time skip without /proc itself.
func stubPseudoFsRoots(t *testing.T, roots ...string) {
	t.Helper()
	orig := pseudoFsRoots
	pseudoFsRoots = roots
	t.Cleanup(func() { pseudoFsRoots = orig })
}

// isPseudoFsPath is the decision both grep arms' walks consult: which paths
// name pseudo-filesystem content. The kernel mount points and anything under
// them are skipped by a walk entering from outside; everything a session
// actually works on — home trees, relative paths, the filesystem root itself,
// and a directory merely named like a mount point — is not.
func TestIsPseudoFsPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"/proc", true},
		{"/proc/", true},
		{"/proc/123/root", true},
		{"/proc/self/environ", true},
		{"/sys", true},
		{"/sys/kernel", true},
		{"/dev", true},
		{"/dev/null", true},
		// /dev/shm is carved back out: a tmpfs of ordinary files — the RAM
		// staging area the test gates mint their scratch under — so a
		// search rooted there is ordinary, on the rg arm when rg is present.
		{"/dev/shm", false},
		{"/dev/shm/gate-scratch/001", false},
		{"/", false},
		{"/home/x", false},
		{"/home/x/proc", false},
		{"/procology", false},
		{"/devology", false},
		{"/sysroot", false},
		{"proc/123", false},
		{"./proc", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isPseudoFsPath(tc.path); got != tc.want {
			t.Errorf("isPseudoFsPath(%q) = %t, want %t", tc.path, got, tc.want)
		}
	}
}

// grepReachesPseudoRoot decides which search roots are never handed to
// ripgrep: the filesystem root — every absolute path's ancestor — and any
// other proper ancestor of a pseudo-filesystem root. A root that is itself a
// pseudo root (/proc named directly) does not reach: naming it is a deliberate
// request, like naming a file, and its read is bounded per file instead.
func TestGrepReachesPseudoRoot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dir  string
		want bool
	}{
		{"/", true},
		{"/proc", false},
		{"/sys", false},
		{"/dev", false},
		{"/proc/1", false},
		{"/usr", false},
		{"/home/x", false},
		{"relative", false},
	}
	for _, tc := range cases {
		if got := grepReachesPseudoRoot(tc.dir); got != tc.want {
			t.Errorf("grepReachesPseudoRoot(%q) = %t, want %t", tc.dir, got, tc.want)
		}
	}
}

// writeGrepPseudoFsTree lays one match under a real directory, under the
// directory the stubbed pseudo-filesystem roots claim, and under a directory
// merely named like a mount point, plus the needle as a directly named file.
func writeGrepPseudoFsTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"real/keep.txt":        "needle\n",
		"pseudo/keep.txt":      "needle\n",
		"pseudo/deep/keep.txt": "needle\n",
		"pseudo/named.txt":     "needle\n",
		"proc/keep.txt":        "needle\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// grepPseudoFsWalkResults are the files_with_matches a search of root must
// report: the directory named proc (a basename, not a pseudo root) and the
// real match — never the stubbed pseudo tree's contents.
func grepPseudoFsWalkResults(root string) []string {
	return []string{
		filepath.FromSlash("proc/keep.txt"),
		filepath.FromSlash("real/keep.txt"),
	}
}

// A walk entering pseudo-filesystem content from outside skips it — /proc,
// /sys and /dev hold runtime state, not files to search, and /proc/kcore
// presents as a ~128TB regular file (#4480). A search explicitly rooted
// inside the pseudo tree still searches it, and a file explicitly named as
// the target is still read: the skip bounds what a walk wanders into, never
// what the caller pointed at.
func TestGrepWalkSkipsPseudoFsDirectories(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	writeGrepPseudoFsTree(t, root)
	stubPseudoFsRoots(t, filepath.Join(root, "pseudo"))
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(got, "\n"); !slices.Equal(lines, grepPseudoFsWalkResults(root)) {
		t.Fatalf("walk from outside = %q, want %q", got, grepPseudoFsWalkResults(root))
	}

	// Explicitly rooted inside the pseudo tree: searched, not skipped.
	got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pseudo"), "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	wantInside := []string{filepath.FromSlash("deep/keep.txt"), "keep.txt", "named.txt"}
	if lines := strings.Split(got, "\n"); !slices.Equal(lines, wantInside) {
		t.Fatalf("walk rooted inside the pseudo tree = %q, want %q", got, wantInside)
	}

	// A file named as the target is read even though a walk skips its tree.
	got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pseudo", "named.txt"), "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1:needle" {
		t.Fatalf("named pseudo-tree file = %q, want %q", got, "1:needle")
	}
}

// The sandboxed walk skips pseudo-filesystem directories the same way, and
// reads a named file inside one through its single-file path.
func TestSandboxedGrepWalkSkipsPseudoFsDirectories(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	writeGrepPseudoFsTree(t, worktree)
	stubPseudoFsRoots(t, filepath.Join(worktree, "pseudo"))

	got, err := env.Grep(t.Context(), "needle", worktree, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(got, "\n"); !slices.Equal(lines, grepPseudoFsWalkResults(worktree)) {
		t.Fatalf("sandboxed walk from outside = %q, want %q", got, grepPseudoFsWalkResults(worktree))
	}

	got, err = env.Grep(t.Context(), "needle", filepath.Join(worktree, "pseudo", "named.txt"), "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1:needle" {
		t.Fatalf("sandboxed named pseudo-tree file = %q, want %q", got, "1:needle")
	}
}

// A search root that reaches a pseudo-filesystem root is never handed to
// ripgrep: rg has no per-file read bound, so /proc/kcore streams into it and
// its match output grows without bound (#4480). The native walk runs instead
// and skips those roots; a root that reaches nothing pseudo still goes to
// rg.
func TestGrepNeverHandsAPseudoFsReachingRootToRipgrep(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	writeGrepPseudoFsTree(t, root)
	stubPseudoFsRoots(t, filepath.Join(root, "pseudo"))
	rec := &grepArgvRecorder{}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = rec
	env.lookPath = func(string) (string, error) { return "/fixture/rg", nil }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if rec.argvCalls != 0 {
		t.Fatalf("grep of a pseudo-fs-reaching root handed the search to ripgrep %d time(s)", rec.argvCalls)
	}
	if lines := strings.Split(got, "\n"); !slices.Equal(lines, grepPseudoFsWalkResults(root)) {
		t.Fatalf("native walk after refusing ripgrep = %q, want %q", got, grepPseudoFsWalkResults(root))
	}

	if _, err := env.Grep(t.Context(), "needle", filepath.Join(root, "real"), "", false, 100, "files_with_matches"); err != nil {
		t.Fatal(err)
	}
	// The rg arm also runs rg --files listings for its oversize disclosure
	// (#4574 round 5), so the pin is that ripgrep was used at all, and that
	// the pseudo-rooted search below adds no further call to it.
	afterRgSearch := rec.argvCalls
	if afterRgSearch == 0 {
		t.Fatalf("a root reaching no pseudo root must use ripgrep: argv calls = %d", rec.argvCalls)
	}

	// A file named inside the pseudo tree is still searched — naming it is a
	// deliberate request — but by the native arm: ripgrep cannot bound a
	// named stream that stats small, so nothing inside a pseudo root is handed
	// to it.
	got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pseudo", "named.txt"), "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if rec.argvCalls != afterRgSearch {
		t.Fatalf("a file named inside the pseudo tree must not go to ripgrep: argv calls = %d, want %d (the earlier call only)", rec.argvCalls, afterRgSearch)
	}
	if want := "1:needle"; got != want {
		t.Fatalf("file named inside the pseudo tree = %q, want %q", got, want)
	}
}
