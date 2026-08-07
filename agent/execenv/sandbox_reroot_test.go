package execenv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/sandbox"
)

// twoLanes builds a main repo with two linked worktrees and returns their
// symlink-resolved paths, plus a fake home for the credential denylist anchor.
func twoLanes(t *testing.T) (laneA, laneB, home string) {
	t.Helper()
	base := t.TempDir()
	main := filepath.Join(base, "main")
	runGit(t, base, "init", "-q", "main")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	runGit(t, main, "worktree", "add", "-q", a, "-b", "fa")
	runGit(t, main, "worktree", "add", "-q", b, "-b", "fb")
	return evalSym(t, a), evalSym(t, b), t.TempDir()
}

func resolvedAt(t *testing.T, home, cwd string, mode sandbox.Mode) *sandbox.ResolvedPolicy {
	t.Helper()
	net := true
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: mode, Network: &net},
		sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true},
		cwd)
	if err != nil {
		t.Fatalf("Resolve(%v) at %s: %v", mode, cwd, err)
	}
	return &rp
}

// TestWithWorkingDirectoryReRootsPolicy: a restricted policy anchored at lane A,
// re-rooted to lane B, must have its roots recomputed for B — not carry A's.
func TestWithWorkingDirectoryReRootsPolicy(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	env := NewLocalExecutionEnvironment(laneA)
	env.Sandbox = resolvedAt(t, home, laneA, sandbox.ModeRestricted)

	child := env.WithWorkingDirectory(laneB)
	if err := child.SandboxReRootError(); err != nil {
		t.Fatalf("re-root to a sibling lane must succeed, got %v", err)
	}
	if child.Sandbox == nil {
		t.Fatal("re-rooted child must carry a Sandbox policy")
	}
	if child.Sandbox.Git.WorktreeRoot != laneB {
		t.Errorf("re-rooted worktree = %q, want lane B %q", child.Sandbox.Git.WorktreeRoot, laneB)
	}
	if !slices.Contains(child.Sandbox.FileTool.WriteRoots, laneB) {
		t.Errorf("re-rooted write roots must include lane B %q: %v", laneB, child.Sandbox.FileTool.WriteRoots)
	}
	if slices.Contains(child.Sandbox.FileTool.WriteRoots, laneA) {
		t.Errorf("re-rooted write roots must NOT still include lane A %q (leaked): %v", laneA, child.Sandbox.FileTool.WriteRoots)
	}
	// Restricted file-tool reads are worktree-only; they must anchor at B, not A.
	if !slices.Contains(child.Sandbox.FileTool.ReadRoots, laneB) || slices.Contains(child.Sandbox.FileTool.ReadRoots, laneA) {
		t.Errorf("re-rooted read roots must be worktree-only at lane B: %v", child.Sandbox.FileTool.ReadRoots)
	}
}

// TestWithWorkingDirectoryReRootDepthInheritance: the policy must survive TWO
// re-root hops (a depth-2 grandchild delegate) and anchor at the deepest lane —
// proving the retained inputs propagate through each re-root rather than a
// re-root freezing after the first hop.
func TestWithWorkingDirectoryReRootDepthInheritance(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	// A third lane sharing the same main repo for the grandchild.
	base := filepath.Dir(laneA)
	main := filepath.Join(base, "main")
	laneC := filepath.Join(base, "c")
	runGit(t, main, "worktree", "add", "-q", laneC, "-b", "fc")
	laneC = evalSym(t, laneC)

	env := NewLocalExecutionEnvironment(laneA)
	env.Sandbox = resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)

	child := env.WithWorkingDirectory(laneB)
	grandchild := child.WithWorkingDirectory(laneC)
	if err := grandchild.SandboxReRootError(); err != nil {
		t.Fatalf("second re-root hop must succeed, got %v", err)
	}
	if grandchild.Sandbox == nil || grandchild.Sandbox.Git.WorktreeRoot != laneC {
		t.Errorf("grandchild must be re-rooted at lane C %q, got %+v", laneC, grandchild.Sandbox)
	}
	if !slices.Contains(grandchild.Sandbox.FileTool.WriteRoots, laneC) || slices.Contains(grandchild.Sandbox.FileTool.WriteRoots, laneB) {
		t.Errorf("grandchild write roots must be lane C only: %v", grandchild.Sandbox.FileTool.WriteRoots)
	}
}

// TestWithWorkingDirectoryReRootsWrapper: the kernel wrapper's policy must also
// re-anchor to the target lane, so a delegate's spawned processes are confined to
// ITS worktree.
func TestWithWorkingDirectoryReRootsWrapper(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	rp := resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)
	w, err := sandbox.NewWrapper(*rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env := NewLocalExecutionEnvironment(laneA)
	env.Sandbox = rp
	env.Wrapper = w

	child := env.WithWorkingDirectory(laneB)
	if err := child.SandboxReRootError(); err != nil {
		t.Fatalf("wrapper re-root must succeed, got %v", err)
	}
	if child.Wrapper == nil {
		t.Fatal("re-rooted child must carry a kernel wrapper")
	}
	if got := child.Wrapper.Policy().Git.WorktreeRoot; got != laneB {
		t.Errorf("re-rooted wrapper policy worktree = %q, want lane B %q", got, laneB)
	}
	if slices.Contains(child.Wrapper.Policy().Spawned.WriteRoots, laneA) {
		t.Errorf("re-rooted wrapper must NOT still grant lane A %q: %v", laneA, child.Wrapper.Policy().Spawned.WriteRoots)
	}
}

// TestWithWorkingDirectoryReRootRefusesUnsatisfiable: a re-root the host cannot
// satisfy leaves Sandbox/Wrapper nil and surfaces a sticky error — fail closed,
// never a silently unconfined child.
func TestWithWorkingDirectoryReRootRefusesUnsatisfiable(t *testing.T) {
	laneA, _, home := twoLanes(t)
	rp := resolvedAt(t, home, laneA, sandbox.ModeRestricted)
	w, err := sandbox.NewWrapper(*rp, "/usr/bin/bwrap", t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	env := NewLocalExecutionEnvironment(laneA)
	env.Sandbox = rp
	env.Wrapper = w

	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, ".git"), []byte("gitdir: /nonexistent/not-a-worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := env.WithWorkingDirectory(bad)
	if child.SandboxReRootError() == nil {
		t.Fatal("an unsatisfiable re-root must record a sticky SandboxReRootError()")
	}
	if child.Sandbox != nil || child.Wrapper != nil {
		t.Errorf("a refused re-root must leave Sandbox/Wrapper nil, got Sandbox=%v Wrapper=%v", child.Sandbox, child.Wrapper)
	}
}

// TestWithWorkingDirectoryOffIsInert: an off (nil-policy) env re-roots byte-
// identically to today — no policy fabricated, no sticky error.
func TestWithWorkingDirectoryOffIsInert(t *testing.T) {
	env := NewLocalExecutionEnvironment("/work")
	child := env.WithWorkingDirectory("/work/sub")
	if child.Sandbox != nil || child.Wrapper != nil {
		t.Errorf("off env must stay unsandboxed after re-root, got Sandbox=%v Wrapper=%v", child.Sandbox, child.Wrapper)
	}
	if child.SandboxReRootError() != nil {
		t.Errorf("off re-root must not record an error, got %v", child.SandboxReRootError())
	}
	if child.RootDir != "/work/sub" {
		t.Errorf("child RootDir = %q, want /work/sub", child.RootDir)
	}
}

// TestEnableSandboxProvisionsAndRetainsSessionTmp: EnableSandbox provisions an
// owned session tmp and wires the wrapper's TMPDIR to it; normal Cleanup retains
// it for the human handoff, while explicit disposal removes it. A re-rooted clone
// never owns (nor disposes) the tmp.
func TestEnableSandboxProvisionsAndRetainsSessionTmp(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	env := NewLocalExecutionEnvironment(laneA)
	rp := resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)
	if err := env.EnableSandbox(rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	if env.Wrapper == nil {
		t.Fatal("EnableSandbox must build a bwrap kernel wrapper")
	}
	tmp := env.Wrapper.SessionTmp()
	if tmp == "" {
		t.Fatal("wrapper must carry a session tmp")
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("session tmp must exist after EnableSandbox: %v", err)
	}

	// A re-rooted clone shares the wrapper tmp path but does not own the dir.
	child := env.WithWorkingDirectory(laneB)
	child.Cleanup()
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("a re-rooted clone's Cleanup must NOT remove the owner's session tmp: %v", err)
	}

	// Normal owner teardown retains the path for the human handoff.
	env.Cleanup()
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("owner Cleanup must retain the session tmp for manual cleanup: %v", err)
	}

	// Explicit disposal is the manual cleanup operation.
	env.DisposeSandboxScratch()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("explicit scratch disposal must remove the session tmp, stat err = %v", err)
	}
}

// TestDisposeSandboxScratch: the narrow scratch dispose removes the env's OWN
// session tmp (the leak-avoidance path for a spawn that EnableSandbox'd a fresh env
// and then failed) and never disturbs a different env's independent scratch.
func TestDisposeSandboxScratch(t *testing.T) {
	laneA, laneB, home := twoLanes(t)
	owner := NewLocalExecutionEnvironment(laneA)
	if err := owner.EnableSandbox(resolvedAt(t, home, laneA, sandbox.ModeWorkspaceWrite)); err != nil {
		t.Fatalf("owner EnableSandbox: %v", err)
	}
	ownerTmp := owner.Wrapper.SessionTmp()
	if _, err := os.Stat(ownerTmp); err != nil {
		t.Fatalf("owner session tmp must exist after EnableSandbox: %v", err)
	}

	// A distinct env provisioning its own box has its OWN scratch.
	fresh := NewLocalExecutionEnvironment(laneB)
	if err := fresh.EnableSandbox(resolvedAt(t, home, laneB, sandbox.ModeWorkspaceWrite)); err != nil {
		t.Fatalf("fresh EnableSandbox: %v", err)
	}
	freshTmp := fresh.Wrapper.SessionTmp()

	fresh.DisposeSandboxScratch()
	if _, err := os.Stat(freshTmp); !os.IsNotExist(err) {
		t.Fatalf("DisposeSandboxScratch must remove the env's own scratch, stat err = %v", err)
	}
	if fresh.ownedSessionTmp != nil {
		t.Error("DisposeSandboxScratch must clear ownedSessionTmp")
	}
	if _, err := os.Stat(ownerTmp); err != nil {
		t.Fatalf("disposing one env's scratch must NOT remove another's: %v", err)
	}

	owner.Cleanup()
	owner.DisposeSandboxScratch()
}

// TestEnableSandboxOffIsNoOp: an off/nil policy provisions no tmp and no wrapper.
func TestEnableSandboxOffIsNoOp(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if err := env.EnableSandbox(nil); err != nil {
		t.Fatalf("EnableSandbox(nil): %v", err)
	}
	if env.Wrapper != nil || env.ownedSessionTmp != nil {
		t.Errorf("off EnableSandbox must not provision a wrapper or tmp")
	}
}
