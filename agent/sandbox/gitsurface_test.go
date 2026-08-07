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

// The packed-refs parity grant: a linked worktree's common dir has no writable
// parent, so it is bound writable as a whole DIRECTORY. That is what lets git
// create packed-refs.lock / packed-refs.new / logs / rr-cache there, which a
// leaf-by-leaf bind cannot express.
func TestBwrapCommonDirGrantedWritableForLinkedWorktree(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, LinkedWorktree, ModeWorkspaceWrite)
	cd := rp.Git.CommonDir
	if cd == "" || isUnderAnyRoot(cd, rp.Spawned.WriteRoots) {
		t.Fatalf("fixture precondition: a linked worktree's common dir %q must sit outside every write root", cd)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if !hasSeq(args, "--bind", cd, cd) {
		t.Fatalf("common dir %q must be bound writable at directory level: %v", cd, args)
	}
	// The gap this closes: names git creates lazily are now reachable because
	// their PARENT is writable, not because each name was bound.
	for _, leaf := range []string{"packed-refs.lock", "packed-refs.new", "rr-cache"} {
		if pathExists(filepath.Join(cd, leaf)) {
			t.Fatalf("fixture precondition: %s must not exist yet", leaf)
		}
	}
}

func TestBwrapCommonDirNotGrantedForMainCheckout(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, MainCheckout, ModeWorkspaceWrite)
	if got := commonDirWriteGrant(rp); got != "" {
		t.Errorf("a main checkout's .git is already under the worktree write root; extra grant %q is a widening", got)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if hasSeq(args, "--bind", rp.Git.CommonDir, rp.Git.CommonDir) {
		t.Errorf("main checkout must not get a separate common-dir bind: %v", args)
	}
}

func TestBwrapCommonDirNotGrantedInReadOnlyMode(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, LinkedWorktree, ModeReadOnly)
	if got := commonDirWriteGrant(rp); got != "" {
		t.Errorf("a read-only session must never bind the common dir writable, got %q", got)
	}
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	if hasSeq(args, "--bind", rp.Git.CommonDir, rp.Git.CommonDir) {
		t.Errorf("read-only session must not bind the common dir writable: %v", args)
	}
}

// bwrapMount is one mount operation from a bwrap flag vector, with its argv index.
type bwrapMount struct {
	op     string
	target string
	at     int
}

// parseBwrapMounts extracts the mount operations from a bwrap flag vector in
// order, skipping flags that mount nothing. bwrap applies operations in argv
// order and a later mount shadows an earlier one, so the LAST operation covering
// a path decides what that path is.
func parseBwrapMounts(args []string) []bwrapMount {
	var out []bwrapMount
	for i := 0; i < len(args); {
		switch args[i] {
		case "--ro-bind", "--bind", "--dev-bind":
			if i+2 < len(args) {
				out = append(out, bwrapMount{args[i], args[i+2], i})
			}
			i += 3
		case "--tmpfs", "--remount-ro", "--dev", "--proc", "--tmp-overlay":
			if i+1 < len(args) {
				out = append(out, bwrapMount{args[i], args[i+1], i})
			}
			i += 2
		case "--overlay-src", "--chdir", "--argv0":
			i += 2
		default:
			i++
		}
	}
	return out
}

// governingMount returns the last mount operation covering path (the path itself
// or an ancestor), and false when nothing covers it.
func governingMount(mounts []bwrapMount, path string) (bwrapMount, bool) {
	var found bwrapMount
	ok := false
	for _, m := range mounts {
		if m.target == path || pathUnder(path, m.target) {
			found, ok = m, true
		}
	}
	return found, ok
}

// hostWritableMount reports whether a mount makes what it covers writable through
// to the host filesystem. A bind is writable; a tmpfs or tmp-overlay is writable
// but private (host untouched); a ro-bind, a remount-ro, and the fresh /dev and
// /proc are not host-writable.
func hostWritableMount(op string) bool { return op == "--bind" || op == "--dev-bind" }

// Every protected git surface must be write-denied in every mode and layout, and
// the proof must not rest on reading two argv entries in the right order: it
// resolves, for each protected path, which mount actually governs it after all
// binds are applied — including the new writable common-dir bind that now sits
// UNDER the protection re-binds.
//
// This is an argv-structural proof plus bwrap's documented later-mount-wins
// ordering; it is not an executed bwrap run (this backend runs only on Linux).
func TestBwrapProtectedGitSurfacesStayWriteDenied(t *testing.T) {
	requireGitHarness(t)
	for _, kind := range []WorkspaceKind{MainCheckout, LinkedWorktree} {
		for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
			t.Run(kind.String()+"/"+mode.String(), func(t *testing.T) {
				rp, cwd := gitSurfaceFixture(t, kind, mode)
				mustWrapper(t, rp) // preparation runs exactly as it does in production
				args := buildBwrapArgv(rp, t.TempDir(), cwd)
				mounts := parseBwrapMounts(args)
				if len(rp.Git.ProtectedPaths) == 0 {
					t.Fatalf("fixture precondition: %v/%v resolved no protected paths", kind, mode)
				}
				for _, p := range rp.Git.ProtectedPaths {
					m, ok := governingMount(mounts, p)
					if !ok {
						continue // nothing mounts it: unreachable, so unwritable
					}
					if hostWritableMount(m.op) {
						t.Errorf("protected surface %q is governed by a writable %s of %q: %v", p, m.op, m.target, args)
					}
				}
			})
		}
	}
}

// The protection re-binds must be emitted AFTER the writable common-dir bind, or
// the bind would land on top and silently invert the denial.
func TestBwrapProtectedRebindsFollowCommonDirGrant(t *testing.T) {
	requireGitHarness(t)
	rp, cwd := gitSurfaceFixture(t, LinkedWorktree, ModeWorkspaceWrite)
	mustWrapper(t, rp)
	args := buildBwrapArgv(rp, t.TempDir(), cwd)
	mounts := parseBwrapMounts(args)
	cd := rp.Git.CommonDir
	grant := seqIndex(args, "--bind", cd, cd)
	if grant < 0 {
		t.Fatalf("expected a writable common-dir bind for %q: %v", cd, args)
	}
	protectedInCommon := 0
	for _, p := range rp.Git.ProtectedPaths {
		if !pathUnder(p, cd) || p == cd {
			continue
		}
		protectedInCommon++
		m, ok := governingMount(mounts, p)
		if !ok || m.target != p {
			t.Errorf("protected surface %q under the granted common dir must be re-bound in its own right: %v", p, args)
			continue
		}
		if m.at < grant {
			t.Errorf("protection for %q (idx %d) must come after the common-dir bind (idx %d): %v", p, m.at, grant, args)
		}
	}
	if protectedInCommon == 0 {
		t.Fatal("fixture precondition: the common dir must carry protected surfaces")
	}
}
