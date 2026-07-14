package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// nonTmpMainAndLanes materializes a main repo and n linked worktree lanes under
// the package dir (NOT /tmp): the sandbox mounts a fresh tmpfs over /tmp, so a
// /tmp lane would be shadowed and hide a real confinement result. Each lane gets a
// planted secret + marker file. Returns the main root and the lane paths.
func nonTmpMainAndLanes(t *testing.T, n int) (string, []string) {
	t.Helper()
	base, err := os.MkdirTemp("/var/tmp", "sbxlane-")
	if err != nil {
		t.Skipf("create non-/tmp sandbox fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")

	lanes := make([]string, n)
	for i := range lanes {
		lane := filepath.Join(base, "lane"+string(rune('a'+i)))
		gitHarness(t, main, "worktree", "add", "-q", lane, "-b", "feat"+string(rune('a'+i)))
		lane = resolveCleanPath(lane)
		if err := os.WriteFile(filepath.Join(lane, "secret.txt"), []byte("LANE-SECRET-"+filepath.Base(lane)), 0o644); err != nil {
			t.Fatal(err)
		}
		lanes[i] = lane
	}
	return resolveCleanPath(main), lanes
}

// runArgv runs a wrapped argv with the sandbox env floor applied and returns the
// combined output.
func runArgv(t *testing.T, argv []string, rp ResolvedPolicy, sessionTmp string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = ApplyEnvFloor(os.Environ(), rp, sessionTmp)
	cmd.ExtraFiles = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// crossLaneScript writes into own/other lanes and reads another lane's secret, so
// the caller can assert what the kernel allowed vs denied.
func crossLaneScript(own, parent, sibling string) string {
	return strings.Join([]string{
		`echo "own=$(echo ok > '` + filepath.Join(own, "child.txt") + `' 2>&1; echo exit=$?)"`,
		`echo "parent-write=$(echo pwned > '` + filepath.Join(parent, "pwned.txt") + `' 2>&1; echo exit=$?)"`,
		`echo "sibling-write=$(echo pwned > '` + filepath.Join(sibling, "pwned.txt") + `' 2>&1; echo exit=$?)"`,
		`echo "parent-read=$(cat '` + filepath.Join(parent, "secret.txt") + `' 2>/dev/null)"`,
	}, "; ")
}

// TestBwrapDelegateRestrictedConfinedToOwnLane is the crown-jewel M4 escape test:
// a RESTRICTED parent policy re-rooted to a child delegate's lane (exactly what
// WithWorkingDirectory does) must kernel-confine the child to ITS lane — it cannot
// read or write the parent's lane nor write a sibling delegate's lane.
func TestBwrapDelegateRestrictedConfinedToOwnLane(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	main, lanes := nonTmpMainAndLanes(t, 3)
	_ = main
	parent, child, sibling := lanes[0], lanes[1], lanes[2]
	sessionTmp := t.TempDir()

	net := true
	rpParent, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net}, facts, parent)
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	wParent, err := NewWrapper(rpParent, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	// Re-root to the child lane, mirroring env.WithWorkingDirectory(child).
	rpChild, err := rpParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root child: %v", err)
	}
	wChild, err := wParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root wrapper: %v", err)
	}

	argv := wChild.Wrap([]string{"/bin/bash", "-c", crossLaneScript(child, parent, sibling)}, child)
	out, err := runArgv(t, argv, *rpChild, sessionTmp)
	if err != nil {
		t.Fatalf("child command failed to run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "own=exit=0") {
		t.Errorf("child must be able to write its OWN lane:\n%s", out)
	}
	if strings.Contains(out, "parent-write=exit=0") {
		t.Errorf("child WROTE the parent lane — cross-lane escape:\n%s", out)
	}
	if strings.Contains(out, "sibling-write=exit=0") {
		t.Errorf("child WROTE a sibling lane — cross-lane escape:\n%s", out)
	}
	if strings.Contains(out, "LANE-SECRET-"+filepath.Base(parent)) {
		t.Errorf("child READ the parent lane's secret (restricted = worktree-only reads):\n%s", out)
	}
	// The host must show no planted files afterward.
	for _, p := range []string{filepath.Join(parent, "pwned.txt"), filepath.Join(sibling, "pwned.txt")} {
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("a cross-lane write persisted to the host: %s", p)
		}
	}
}

// TestBwrapWorkspaceWriteDelegateCannotWriteOtherLanes: in workspace-write a child
// may READ anywhere (read-anywhere baseline) but must not WRITE the parent's or a
// sibling's lane.
func TestBwrapWorkspaceWriteDelegateCannotWriteOtherLanes(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	_, lanes := nonTmpMainAndLanes(t, 3)
	parent, child, sibling := lanes[0], lanes[1], lanes[2]
	sessionTmp := t.TempDir()

	net := true
	rpParent, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, parent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rpChild, err := rpParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root: %v", err)
	}
	wParent, err := NewWrapper(rpParent, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	wChild, err := wParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root wrapper: %v", err)
	}

	argv := wChild.Wrap([]string{"/bin/bash", "-c", crossLaneScript(child, parent, sibling)}, child)
	out, err := runArgv(t, argv, *rpChild, sessionTmp)
	if err != nil {
		t.Fatalf("child command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "own=exit=0") {
		t.Errorf("child must write its own lane:\n%s", out)
	}
	if strings.Contains(out, "parent-write=exit=0") || strings.Contains(out, "sibling-write=exit=0") {
		t.Errorf("workspace-write child WROTE another lane — cross-lane escape:\n%s", out)
	}
}

// laneCommitScript writes a file in the lane, then git add + git commit it —
// exactly what an isolation=worktree delegate does in its lane. The lane's git dir
// lives in the MAIN repo's .git/worktrees/<id> (a different tree from the lane
// cwd), so this exercises whether the resolved policy grants the pointer target's
// per-worktree dir + common-dir subpaths a commit needs. It also probes that the
// config/hook protected surfaces stay write-denied.
func laneCommitScript(lane string) string {
	return strings.Join([]string{
		`export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@e GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@e`,
		`export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null`,
		`cd '` + lane + `'`,
		`echo change > lanefile.txt`,
		`echo "add=$(git add lanefile.txt 2>&1; echo exit=$?)"`,
		`echo "commit=$(git commit -q -m lanec 2>&1; echo exit=$?)"`,
		`echo "config-write=$(git config core.hooksPath /evil 2>&1; echo exit=$?)"`,
		// The per-worktree dir is granted whole-writable; its config.worktree + hooks
		// must still be re-bound read-only ON TOP (the fix must not expose them).
		`echo "wt-config-write=$(echo x > '` + gitDirOf(lane) + `/config.worktree' 2>&1; echo exit=$?)"`,
		`echo "wt-hook-write=$(echo x > '` + gitDirOf(lane) + `/hooks/pre-commit' 2>&1; echo exit=$?)"`,
	}, "; ")
}

// gitDirOf returns a lane's linked per-worktree git dir (main/.git/worktrees/<id>),
// reading the lane's .git pointer file the same way the classifier does.
func gitDirOf(lane string) string {
	data, err := os.ReadFile(filepath.Join(lane, ".git"))
	if err != nil {
		return filepath.Join(lane, ".git-unresolved")
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
}

// TestBwrapWorkspaceWriteDelegateCanCommitInLane is the vmm6 regression test: an
// isolation=worktree delegate re-rooted to its lane must be able to `git add` +
// `git commit` inside that lane — the lane's git dir lives in the main repo's
// .git/worktrees/<id>, a separate tree from the lane cwd. The config/hook
// protected surfaces must STILL be write-denied (the fix must not blanket-mount
// .git writable).
func TestBwrapWorkspaceWriteDelegateCanCommitInLane(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	_, lanes := nonTmpMainAndLanes(t, 2)
	parent, child := lanes[0], lanes[1]
	sessionTmp := t.TempDir()

	net := true
	rpParent, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, parent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rpChild, err := rpParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root: %v", err)
	}
	wParent, err := NewWrapper(rpParent, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	wChild, err := wParent.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root wrapper: %v", err)
	}

	argv := wChild.Wrap([]string{"/bin/bash", "-c", laneCommitScript(child)}, child)
	out, err := runArgv(t, argv, *rpChild, sessionTmp)
	if err != nil {
		t.Fatalf("lane commit command failed to run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "add=exit=0") {
		t.Errorf("delegate must be able to `git add` in its lane:\n%s", out)
	}
	if !strings.Contains(out, "commit=exit=0") {
		t.Errorf("delegate must be able to `git commit` in its lane:\n%s", out)
	}
	if strings.Contains(out, "config-write=exit=0") {
		t.Errorf("delegate WROTE the common git config — protected surfaces must stay guarded:\n%s", out)
	}
	if strings.Contains(out, "wt-config-write=exit=0") {
		t.Errorf("delegate WROTE the per-worktree config.worktree — whole-dir grant leaked a protected surface:\n%s", out)
	}
	if strings.Contains(out, "wt-hook-write=exit=0") {
		t.Errorf("delegate WROTE a per-worktree hook — whole-dir grant leaked a protected surface:\n%s", out)
	}
}

// TestBwrapResumedDelegateReconfined: a RESTORED delegate re-resolves its persisted
// policy inputs against its lane (as job_delegate's restore path does) and the
// kernel re-applies confinement — a write outside the lane is denied.
func TestBwrapResumedDelegateReconfined(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	_, lanes := nonTmpMainAndLanes(t, 2)
	other, lane := lanes[0], lanes[1]
	sessionTmp := t.TempDir()

	// Restore re-resolves the PERSISTED INPUTS (mode restricted) at the lane +
	// freshly-probed facts — not a stored resolved blob.
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted}, facts, lane)
	if err != nil {
		t.Fatalf("resolve at lane: %v", err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	script := `echo "own=$(echo ok > '` + filepath.Join(lane, "r.txt") + `' 2>&1; echo exit=$?)"; ` +
		`echo "out=$(echo pwned > '` + filepath.Join(other, "pwned.txt") + `' 2>&1; echo exit=$?)"`
	argv := w.Wrap([]string{"/bin/bash", "-c", script}, lane)
	out, err := runArgv(t, argv, rp, sessionTmp)
	if err != nil {
		t.Fatalf("resumed delegate command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "own=exit=0") {
		t.Errorf("resumed delegate must write its own lane:\n%s", out)
	}
	if strings.Contains(out, "out=exit=0") {
		t.Errorf("resumed delegate WROTE outside its lane — confinement not re-applied:\n%s", out)
	}
}

// TestBwrapDepthGrandchildConfined: a depth-2 re-root (grandchild delegate) confines
// the grandchild to ITS lane — it cannot write the parent or grandparent lane.
func TestBwrapDepthGrandchildConfined(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	_, lanes := nonTmpMainAndLanes(t, 3)
	grandparent, parent, grandchild := lanes[0], lanes[1], lanes[2]
	sessionTmp := t.TempDir()

	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net}, facts, grandparent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, sessionTmp)
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	// Two re-root hops: grandparent → parent → grandchild.
	rp2, err := rp.ReRoot(parent)
	if err != nil {
		t.Fatalf("re-root 1: %v", err)
	}
	rpGC, err := rp2.ReRoot(grandchild)
	if err != nil {
		t.Fatalf("re-root 2: %v", err)
	}
	w2, err := w.ReRoot(parent)
	if err != nil {
		t.Fatalf("wrapper re-root 1: %v", err)
	}
	wGC, err := w2.ReRoot(grandchild)
	if err != nil {
		t.Fatalf("wrapper re-root 2: %v", err)
	}
	script := `echo "own=$(echo ok > '` + filepath.Join(grandchild, "gc.txt") + `' 2>&1; echo exit=$?)"; ` +
		`echo "parent=$(echo pwned > '` + filepath.Join(parent, "pwned.txt") + `' 2>&1; echo exit=$?)"; ` +
		`echo "gp=$(echo pwned > '` + filepath.Join(grandparent, "pwned.txt") + `' 2>&1; echo exit=$?)"`
	argv := wGC.Wrap([]string{"/bin/bash", "-c", script}, grandchild)
	out, err := runArgv(t, argv, *rpGC, sessionTmp)
	if err != nil {
		t.Fatalf("grandchild command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "own=exit=0") {
		t.Errorf("grandchild must write its own lane:\n%s", out)
	}
	if strings.Contains(out, "parent=exit=0") || strings.Contains(out, "gp=exit=0") {
		t.Errorf("grandchild WROTE an ancestor lane — depth re-root leaked confinement:\n%s", out)
	}
}

// TestReRootNetOffInherited: a net=off parent's re-rooted child stays net=off, and
// the child's kernel wrapper still emits --unshare-net (egress inherited across the
// re-root). Real egress denial under --unshare-net is M3's contract; M4's addition
// is that it survives re-rooting to the child lane.
func TestReRootNetOffInherited(t *testing.T) {
	facts := bwrapFacts(t.TempDir())
	_, lanes := twoLinkedLanesInline(t)
	parent, child := lanes[0], lanes[1]

	off := false
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &off}, facts, parent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rpChild, err := rp.ReRoot(child)
	if err != nil {
		t.Fatalf("re-root: %v", err)
	}
	if rpChild.Network {
		t.Errorf("re-rooted child of a net=off parent must stay net=off")
	}
	w, err := NewWrapper(*rpChild, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("wrapper: %v", err)
	}
	argv := w.Wrap([]string{"/bin/true"}, child)
	if !slices.Contains(argv, "--unshare-net") {
		t.Errorf("net=off child wrapper must emit --unshare-net: %v", argv)
	}
}

// twoLinkedLanesInline materializes a main + two linked worktrees under /tmp (fine
// here — this test only inspects policy/argv values, it does not run bwrap).
func twoLinkedLanesInline(t *testing.T) (string, []string) {
	t.Helper()
	requireGitHarness(t)
	main := resolveCleanPath(t.TempDir())
	gitHarness(t, main, "init", "-q")
	gitHarness(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	a := main + "-a"
	b := main + "-b"
	gitHarness(t, main, "worktree", "add", "-q", a)
	gitHarness(t, main, "worktree", "add", "-q", b)
	return main, []string{resolveCleanPath(a), resolveCleanPath(b)}
}
