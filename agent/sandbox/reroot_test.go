package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestFuzzReRootFixtureUsesStructuralLinkedWorktrees(t *testing.T) {
	fixture := newFuzzReRootFixture(t)
	for _, cwd := range []string{fixture.laneA, fixture.laneB} {
		layout, err := ClassifyWorkspace(cwd)
		if err != nil {
			t.Fatalf("ClassifyWorkspace(%q): %v", cwd, err)
		}
		if layout.Kind != LinkedWorktree {
			t.Fatalf("ClassifyWorkspace(%q).Kind = %v, want %v", cwd, layout.Kind, LinkedWorktree)
		}
		if layout.CommonDir != filepath.Join(fixture.main, ".git") {
			t.Fatalf("ClassifyWorkspace(%q).CommonDir = %q, want %q", cwd, layout.CommonDir, filepath.Join(fixture.main, ".git"))
		}
	}
}

type fuzzReRootFixture struct {
	root      string
	main      string
	laneA     string
	laneB     string
	malformed string
}

// newFuzzReRootFixture materializes only the filesystem shapes ClassifyWorkspace
// consumes. It intentionally does not use git: the fuzzer needs a deterministic
// linked-worktree layout, not a real repository or subprocess.
func newFuzzReRootFixture(t TestingT) fuzzReRootFixture {
	t.Helper()
	root := resolveCleanPath(t.TempDir())
	fixture := fuzzReRootFixture{
		root:      root,
		main:      filepath.Join(root, "main"),
		laneA:     filepath.Join(root, "lane-a"),
		laneB:     filepath.Join(root, "lane-b"),
		malformed: filepath.Join(root, "malformed"),
	}
	commonDir := filepath.Join(fixture.main, ".git")
	for _, dir := range []string{
		commonDir,
		filepath.Join(commonDir, "worktrees", "lane-a"),
		filepath.Join(commonDir, "worktrees", "lane-b"),
		fixture.laneA,
		fixture.laneB,
		fixture.malformed,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir fixture %q: %v", dir, err)
		}
	}
	for _, lane := range []struct {
		name string
		path string
	}{
		{name: "lane-a", path: fixture.laneA},
		{name: "lane-b", path: fixture.laneB},
	} {
		gitDir := filepath.Join(commonDir, "worktrees", lane.name)
		if err := os.WriteFile(filepath.Join(lane.path, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
			t.Fatalf("write linked-worktree pointer for %q: %v", lane.path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.malformed, ".git"), []byte("gitdir: "+filepath.Join(root, "not-a-git-layout")+"\n"), 0o644); err != nil {
		t.Fatalf("write malformed-worktree pointer: %v", err)
	}
	return fixture
}

// cwd maps arbitrary fuzz bytes to a path whose entire ancestry is inside this
// test-owned fixture. ClassifyWorkspace may walk parents while it looks for .git,
// so passing raw fuzz paths here would otherwise inspect the developer's host.
func (fixture fuzzReRootFixture) cwd(raw string) (cwd, worktree string, malformed bool) {
	bases := []struct {
		cwd       string
		worktree  string
		malformed bool
	}{
		{cwd: fixture.laneA, worktree: fixture.laneA},
		{cwd: fixture.laneB, worktree: fixture.laneB},
		{cwd: fixture.main, worktree: fixture.main},
		{cwd: fixture.malformed, malformed: true},
	}
	index := 0
	if len(raw) > 0 {
		index = int(raw[0]) % len(bases)
	}
	chosen := bases[index]
	if len(raw)%2 == 0 {
		return chosen.cwd, chosen.worktree, chosen.malformed
	}
	digest := sha256.Sum256([]byte(raw))
	return filepath.Join(chosen.cwd, "fuzz", hex.EncodeToString(digest[:8])), chosen.worktree, chosen.malformed
}

// FuzzReRoot drives ReRoot with arbitrary target selection against a structural,
// temp-root Git fixture. It never launches git or inspects ambient paths. It must
// never panic, never widen a grant onto a masked/pseudo-fs path, and every refusal
// must be typed.
func FuzzReRoot(f *testing.F) {
	fixture := newFuzzReRootFixture(f)
	home := filepath.Join(fixture.root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		f.Fatalf("mkdir fake home: %v", err)
	}
	facts := bwrapFacts(home)

	net := true
	base, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, fixture.laneA)
	if err != nil {
		f.Fatalf("seed resolve: %v", err)
	}

	f.Add("lane-a")
	f.Add("lane-b")
	f.Add("main")
	f.Add("malformed")
	f.Add("")
	f.Add("../../ambient/path")

	f.Fuzz(func(t *testing.T, raw string) {
		cwd, wantWorktree, wantRefusal := fixture.cwd(raw)
		rerooted, err := base.ReRoot(cwd)
		if err != nil {
			var ref *RefusalError
			if !errors.As(err, &ref) {
				t.Fatalf("re-root error must be a typed *RefusalError, got %T: %v", err, err)
			}
			if !wantRefusal {
				t.Fatalf("ReRoot(%q) unexpectedly refused a valid structural workspace: %v", cwd, err)
			}
			return
		}
		if wantRefusal {
			t.Fatalf("ReRoot(%q) accepted a deliberately malformed Git pointer", cwd)
		}
		if rerooted == nil {
			t.Fatal("ReRoot returned nil policy for an enforced source policy")
		}
		if rerooted.Git.WorktreeRoot != wantWorktree {
			t.Fatalf("ReRoot(%q).Git.WorktreeRoot = %q, want %q", cwd, rerooted.Git.WorktreeRoot, wantWorktree)
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
		// source policy would keep granting lane A after targeting lane B or main.
		for _, r := range rerooted.FileTool.WriteRoots {
			if r != rerooted.Git.WorktreeRoot && !pathUnder(r, rerooted.Git.WorktreeRoot) {
				t.Fatalf("re-rooted FileTool write root %q escapes the re-rooted worktree %q (copy bug?)", r, rerooted.Git.WorktreeRoot)
			}
		}
		if wantWorktree != fixture.laneA && rootGrants(rerooted.FileTool.WriteRoots, fixture.laneA) {
			t.Fatalf("ReRoot(%q) retained source lane A write grant: %v", cwd, rerooted.FileTool.WriteRoots)
		}
	})
}
