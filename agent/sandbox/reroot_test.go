package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// twoLinkedLanes materializes a main checkout with two linked worktrees and
// returns (main, laneA, laneB), all symlink-resolved. Skips when git is absent.
func twoLinkedLanes(t *testing.T) (string, string, string) {
	t.Helper()
	requireGitHarness(t)
	main := resolveCleanPath(t.TempDir())
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	laneA := main + "-a"
	laneB := main + "-b"
	gitHarness(t, main, "worktree", "add", "-q", laneA)
	gitHarness(t, main, "worktree", "add", "-q", laneB)
	return main, resolveCleanPath(laneA), resolveCleanPath(laneB)
}

func TestReRootToLinkedWorktree(t *testing.T) {
	_, laneA, laneB := twoLinkedLanes(t)
	facts := bwrapFacts(t.TempDir())

	net := true
	rpA, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, laneA)
	if err != nil {
		t.Fatalf("resolve at lane A: %v", err)
	}
	rpB, err := rpA.ReRoot(laneB)
	if err != nil {
		t.Fatalf("re-root to lane B: %v", err)
	}

	if rpB.Git.WorktreeRoot != laneB {
		t.Errorf("re-rooted worktree = %q, want lane B %q", rpB.Git.WorktreeRoot, laneB)
	}
	if !rootGrants(rpB.FileTool.WriteRoots, laneB) {
		t.Errorf("re-rooted write roots must anchor at lane B %q: %v", laneB, rpB.FileTool.WriteRoots)
	}
	if rootGrants(rpB.FileTool.WriteRoots, laneA) {
		t.Errorf("re-rooted write roots must NOT still cover lane A %q (leaked roots): %v", laneA, rpB.FileTool.WriteRoots)
	}
	// The two lanes share one main repo, so the common-.git read grant is the same
	// after re-root, but each lane's per-worktree gitdir is distinct — a re-root
	// that recomputed the gitdir yields a different GitDir, a copy would not.
	if rpB.Git.CommonDir != rpA.Git.CommonDir {
		t.Errorf("re-rooted CommonDir = %q, want shared main .git %q", rpB.Git.CommonDir, rpA.Git.CommonDir)
	}
	if rpB.Git.GitDir == rpA.Git.GitDir {
		t.Errorf("re-root did not recompute the per-worktree gitdir: both lanes report %q", rpB.Git.GitDir)
	}
	if !slices.Contains(rpB.Git.ReadGrantPaths, rpB.Git.CommonDir) {
		t.Errorf("re-rooted linked worktree must read-grant its common .git %q: %v", rpB.Git.CommonDir, rpB.Git.ReadGrantPaths)
	}
}

func TestReRootSiblingLanesDisjoint(t *testing.T) {
	_, laneA, laneB := twoLinkedLanes(t)
	facts := bwrapFacts(t.TempDir())

	net := true
	base, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, laneA)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	toA, err := base.ReRoot(laneA)
	if err != nil {
		t.Fatalf("re-root to A: %v", err)
	}
	toB, err := base.ReRoot(laneB)
	if err != nil {
		t.Fatalf("re-root to B: %v", err)
	}
	if rootGrants(toA.FileTool.WriteRoots, laneB) {
		t.Errorf("lane A's re-rooted policy must not grant sibling lane B %q: %v", laneB, toA.FileTool.WriteRoots)
	}
	if rootGrants(toB.FileTool.WriteRoots, laneA) {
		t.Errorf("lane B's re-rooted policy must not grant sibling lane A %q: %v", laneA, toB.FileTool.WriteRoots)
	}
}

func TestReRootRefusesUnsatisfiable(t *testing.T) {
	_, laneA, _ := twoLinkedLanes(t)
	facts := bwrapFacts(t.TempDir())

	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net}, facts, laneA)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// A target whose .git pointer names an unrecognized shape cannot be classified,
	// so the re-root fails closed with a typed refusal rather than a guessed grant.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, ".git"), []byte("gitdir: /nonexistent/not-a-worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rerootErr := rp.ReRoot(resolveCleanPath(bad))
	if rerootErr == nil {
		t.Fatalf("re-root onto an unclassifiable target must refuse, got nil")
	}
	var ref *RefusalError
	if !errors.As(rerootErr, &ref) {
		t.Fatalf("re-root refusal must be a typed *RefusalError, got %T: %v", rerootErr, rerootErr)
	}
}

func TestControlPolicyGrantsRegistry(t *testing.T) {
	facts := bwrapFacts(t.TempDir())
	main := MaterializeWorkspace(t, MainCheckout)
	registry := filepath.Join(main, ".git", "worktrees")
	config := filepath.Join(main, ".git", "config")
	hooks := filepath.Join(main, ".git", "hooks")

	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, main)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ctrl, err := rp.ControlPolicy(main)
	if err != nil {
		t.Fatalf("control policy: %v", err)
	}

	if !rootGrants(ctrl.Spawned.WriteRoots, registry) {
		t.Errorf("control policy must grant the worktree registry %q writable: %v", registry, ctrl.Spawned.WriteRoots)
	}
	// .git/config and hooks stay write-PROTECTED and must never be a write ROOT in
	// their own right (the enforcement layers re-deny them even nested under the
	// writable main-repo root; keep the resolved set honest too).
	for _, denied := range []string{config, hooks} {
		if !slices.Contains(ctrl.Git.ProtectedPaths, denied) {
			t.Errorf("control policy must keep %q write-protected: %v", denied, ctrl.Git.ProtectedPaths)
		}
		for _, wr := range slices.Concat(ctrl.FileTool.WriteRoots, ctrl.Spawned.WriteRoots) {
			if wr == denied {
				t.Errorf("control policy must NOT list %q as a write root: %v", denied, wr)
			}
		}
	}
}

// A read-only session's control env must stay read-only: ControlPolicy must not
// widen it to write the main repo or the registry (read-only cannot create a
// worktree).
func TestControlPolicyReadOnlyNotWidened(t *testing.T) {
	facts := bwrapFacts(t.TempDir())
	main := MaterializeWorkspace(t, MainCheckout)

	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeReadOnly, Network: &net}, facts, main)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ctrl, err := rp.ControlPolicy(main)
	if err != nil {
		t.Fatalf("control policy: %v", err)
	}
	if len(ctrl.FileTool.WriteRoots) != 0 || len(ctrl.Spawned.WriteRoots) != 0 {
		t.Errorf("read-only control policy must grant no writes, got file=%v spawned=%v", ctrl.FileTool.WriteRoots, ctrl.Spawned.WriteRoots)
	}
}

func TestReRootOffIsNil(t *testing.T) {
	// A nil resolved policy (the env carries none — off) re-roots to nil, no error.
	var nilPol *ResolvedPolicy
	got, err := nilPol.ReRoot("/anywhere")
	if err != nil || got != nil {
		t.Errorf("nil policy re-root = (%v, %v), want (nil, nil)", got, err)
	}

	// An off policy produced by Resolve re-roots to an equivalent off policy.
	off, err := Resolve(SandboxPolicy{Mode: ModeOff}, HostFacts{OS: "linux", Home: "/home/x"}, "/work")
	if err != nil {
		t.Fatalf("resolve off: %v", err)
	}
	rerooted, err := off.ReRoot("/other")
	if err != nil {
		t.Fatalf("re-root off: %v", err)
	}
	if rerooted == nil || rerooted.Enforced() {
		t.Errorf("re-rooted off policy must stay a non-enforced off policy, got %+v", rerooted)
	}
}

func TestReRootContract(t *testing.T) {
	AssertReRoot(t, ReRootCases())
}

// FuzzReRoot drives ReRoot with an arbitrary target cwd against a real resolved
// policy: it must never panic, never widen a grant onto a masked/pseudo-fs path,
// and every refusal must be typed.
func FuzzReRoot(f *testing.F) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		f.Skip("git not available for the fuzz seed workspace")
	}
	main := resolveCleanPath(f.TempDir())
	seed := func(dir string, args ...string) {
		requireGitHarness(f)
		gitHarness(f, dir, args...)
	}
	seed(main, "init", "-q")
	seed(main, "commit", "-q", "--allow-empty", "-m", "init")
	facts := bwrapFacts(f.TempDir())

	net := true
	base, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, main)
	if err != nil {
		f.Fatalf("seed resolve: %v", err)
	}

	f.Add("/tmp")
	f.Add(main)
	f.Add("relative/path")
	f.Add("")
	f.Add("/proc/self")

	f.Fuzz(func(t *testing.T, cwd string) {
		rerooted, err := base.ReRoot(cwd)
		if err != nil {
			var ref *RefusalError
			if !errors.As(err, &ref) {
				t.Fatalf("re-root error must be a typed *RefusalError, got %T: %v", err, err)
			}
			return
		}
		if rerooted == nil {
			return
		}
		roots := slices.Concat(
			rerooted.FileTool.ReadRoots, rerooted.FileTool.WriteRoots,
			rerooted.Spawned.ReadRoots, rerooted.Spawned.WriteRoots,
		)
		for _, r := range roots {
			for _, m := range rerooted.MaskedPaths {
				if r == m || pathUnder(r, m) {
					t.Fatalf("re-rooted grant %q lands at/under masked path %q", r, m)
				}
			}
		}
		// Recompute-not-copy: the file-tool write roots (worktree-only for the seed
		// mode) must all fall within the RE-ROOTED worktree. A ReRoot that copied the
		// source policy would keep granting the seed worktree, which — unless the
		// fuzzed cwd is an ancestor of it — escapes the new worktree and trips here.
		for _, r := range rerooted.FileTool.WriteRoots {
			if r != rerooted.Git.WorktreeRoot && !pathUnder(r, rerooted.Git.WorktreeRoot) {
				t.Fatalf("re-rooted FileTool write root %q escapes the re-rooted worktree %q (copy bug?)", r, rerooted.Git.WorktreeRoot)
			}
		}
	})
}
