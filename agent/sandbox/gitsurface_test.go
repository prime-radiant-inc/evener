package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests drive the BWRAP argv builder and its git-surface preparation from
// any build host: bwrapFacts reports OS "linux" (Resolve keys on HostFacts.OS,
// not runtime.GOOS), and every asserted path comes from a canonicalized fixture,
// so darwin's /var → /private/var firmlink cannot make them spurious. Nothing
// here starts bubblewrap; they pin policy assembly and argv structure.

func gitSurfaceFixture(t *testing.T, kind WorkspaceKind, mode Mode) (ResolvedPolicy, string) {
	t.Helper()
	home := resolveCleanPath(t.TempDir())
	cwd := MaterializeWorkspace(t, kind)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, bwrapFacts(home), cwd)
	if err != nil {
		t.Fatalf("Resolve(%v, %v): %v", kind, mode, err)
	}
	return rp, cwd
}

func mustWrapper(t *testing.T, rp ResolvedPolicy) *Wrapper {
	t.Helper()
	w, err := NewWrapper(rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	return w
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// A main checkout has no commondir, and it sits under the worktree write root —
// so bubblewrap used to pin it with /dev/null and leave an EMPTY commondir behind,
// which git treats as fatal for that repo forever. Wrapper construction must
// instead create it holding ".", the self-referential common dir a git dir
// without the file already means.
func TestPrepareGitSurfacesCreatesCommondirForMainCheckout(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeWorkspaceWrite)
	commondir := filepath.Join(cwd, ".git", "commondir")
	if pathExists(commondir) {
		t.Fatalf("fixture precondition: a main checkout must start without %s", commondir)
	}

	mustWrapper(t, rp)

	if got := readFile(t, commondir); got != ".\n" {
		t.Errorf("pre-created commondir = %q, want %q", got, ".\n")
	}
	// The prepared surface must be pinned over ITSELF, never over /dev/null —
	// a /dev/null pin is what materialized the empty file in the first place.
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if !hasSeq(args, "--ro-bind", commondir, commondir) {
		t.Errorf("commondir must be re-bound read-only over itself: %v", args)
	}
	if hasSeq(args, "--ro-bind", "/dev/null", commondir) {
		t.Errorf("commondir must never be pinned with a /dev/null bind: %v", args)
	}
}

// The surfaces serf does NOT prepare must be left strictly alone: preparation is
// a write into the user's real .git, so it is confined to the one surface whose
// empty residue is fatal.
func TestPrepareGitSurfacesTouchesOnlyCommondir(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeWorkspaceWrite)
	gitDir := filepath.Join(cwd, ".git")

	mustWrapper(t, rp)

	for _, leaf := range []string{"config.worktree", "gitdir"} {
		if pathExists(filepath.Join(gitDir, leaf)) {
			t.Errorf("%s must not be pre-created: it has no correct inert content and its empty residue is harmless", leaf)
		}
	}
	// No staging file may be left behind in the user's .git.
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".serf-sandbox-") {
			t.Errorf("preparation left a staging file behind: %s", e.Name())
		}
	}
}

// Preparation must never modify a surface that already exists — truncating a real
// commondir (or any protected file) would be its own corruption bug.
func TestPrepareGitSurfacesNeverModifiesExistingFile(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeWorkspaceWrite)
	commondir := filepath.Join(cwd, ".git", "commondir")
	const sentinel = "../weird-but-not-ours\n"
	if err := os.WriteFile(commondir, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(cwd, ".git", "config")
	before := readFile(t, config)

	if err := prepareGitSurfaces(rp); err != nil {
		t.Fatalf("prepareGitSurfaces: %v", err)
	}

	if got := readFile(t, commondir); got != sentinel {
		t.Errorf("existing commondir was rewritten: got %q, want %q", got, sentinel)
	}
	if got := readFile(t, config); got != before {
		t.Errorf("existing config was rewritten: got %q, want %q", got, before)
	}
}

// Concurrent sessions starting on the same repo race to claim the same surface.
// The claim is a hardlink, so exactly one wins and the losers succeed on EEXIST —
// and the name never appears with partial content.
func TestPrepareGitSurfacesIsConcurrencySafe(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeWorkspaceWrite)
	commondir := filepath.Join(cwd, ".git", "commondir")

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = prepareGitSurfaces(rp)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent prepare %d: %v", i, err)
		}
	}
	if got := readFile(t, commondir); got != ".\n" {
		t.Errorf("after concurrent preparation commondir = %q, want %q", got, ".\n")
	}
}

// A read-only session pins nothing (no write root can reach a protected surface),
// so it must write nothing into the user's .git either.
func TestPrepareGitSurfacesSkipsReadOnlyMode(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeReadOnly)
	mustWrapper(t, rp)
	if pathExists(filepath.Join(cwd, ".git", "commondir")) {
		t.Error("a read-only session must not create anything in the user's .git")
	}
}

// The Seatbelt backend matches path strings and creates nothing, so it needs no
// preparation and must not write into the user's repo.
func TestPrepareGitSurfacesNotRunForSeatbelt(t *testing.T) {
	requireGitHarness(t)
	home := resolveCleanPath(t.TempDir())
	cwd := MaterializeWorkspace(t, MainCheckout)
	net := true
	facts := HostFacts{OS: "darwin", Home: home, SandboxExecPath: "/usr/bin/sandbox-exec"}
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rp.Backend != BackendSeatbelt {
		t.Fatalf("expected the seatbelt backend, got %v", rp.Backend)
	}
	mustWrapper(t, rp)
	if pathExists(filepath.Join(cwd, ".git", "commondir")) {
		t.Error("the seatbelt backend must not prepare (or create) git surfaces")
	}
}
