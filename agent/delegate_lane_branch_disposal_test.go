package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/worktree"
)

// The divergent-branch lifecycle canary (the delegate-lane branch-names
// design): one lane shape — a git branch that differs from its directory name —
// driven through every branch-acting disposal surface. Every test cuts its lane
// on a mnemonic branch and asserts the surface deletes or reports THAT branch,
// never the delegate id, so a surface that regresses to the name fails
// behaviorally. This is the only enforcement available for a rule the Go
// compiler cannot check.
//
// REAL git throughout: each assertion is a registry or ref effect (a branch
// deleted, a worktree deregistered, a sidecar removed) only git can produce —
// exactly what the divergent shape stresses, because the branch the surface
// must act on is not the name it addresses the lane by.

const canaryLaneBranch = "canary-mnemonic-lane"

// halfRemoveLane takes a LOCKED lane out of band down to the half-removed
// residue shape: worktree gone, branch and sidecar remain (a crash between `git
// worktree remove` and the branch delete).
func halfRemoveLane(t *testing.T, r *wtRepo, lanePath string) {
	t.Helper()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "remove", "--force", "--", lanePath)
}

func assertCanaryLaneGone(t *testing.T, r *wtRepo, delegateID, lanePath string) {
	t.Helper()
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived")
	}
	if r.branchExists(t, delegateID) {
		t.Error("a branch named after the delegate id exists")
	}
	if r.lanePresent(lanePath) {
		t.Error("the lane directory survived")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), delegateID); !os.IsNotExist(err) {
		t.Errorf("the sidecar survived: %v", err)
	}
}

// The full-lane dispose arm must delete the named branch and report it.
func TestDelegateLaneBranchCanary_DisposeFullLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose: %v", err)
	}
	if res.Branch != canaryLaneBranch {
		t.Errorf("result Branch = %q, want %q", res.Branch, canaryLaneBranch)
	}
	assertCanaryLaneGone(t, r, id, lanePath)
}

// The half-removed arm (worktree gone, branch and sidecar remain) must judge
// the named branch's tip and delete the named branch.
func TestDelegateLaneBranchCanary_DisposeHalfRemovedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)
	halfRemoveLane(t, r, lanePath)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose half-removed lane: %v", err)
	}
	if res.Branch != canaryLaneBranch {
		t.Errorf("result Branch = %q, want %q", res.Branch, canaryLaneBranch)
	}
	if !strings.Contains(res.Message, canaryLaneBranch) {
		t.Errorf("message = %q, want it to name the deleted branch %q", res.Message, canaryLaneBranch)
	}
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived a half-removed dispose")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), id); !os.IsNotExist(err) {
		t.Errorf("the sidecar survived: %v", err)
	}
}

// The already-disposed remnants arm (idempotent re-dispose after a crash
// between the worktree remove and the branch delete) must still clean up the
// named branch.
func TestDelegateLaneBranchCanary_DisposeAlreadyDisposedRemnants(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)
	closeStableWorktreeForTest(t, r.s, id, stableWorktreeDisposalReason)
	halfRemoveLane(t, r, lanePath)

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("re-dispose of an already-disposed half-removed lane: %v", err)
	}
	if !res.AlreadyDisposed {
		t.Errorf("AlreadyDisposed = %v, want true", res.AlreadyDisposed)
	}
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived the already-disposed remnants cleanup")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), id); !os.IsNotExist(err) {
		t.Errorf("the sidecar survived: %v", err)
	}
}

// Close-time disposal must delete the named branch of an unchanged lane.
func TestDelegateLaneBranchCanary_CloseTimeDisposal(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)

	r.s.Close()

	assertCanaryLaneGone(t, r, id, lanePath)
}

// remove's sanctioned cascade on a retained-idle delegate lane must report the
// lane's actual branch, and the cascade must have deleted it.
func TestDelegateLaneBranchCanary_RemoveCascadeReportsNamedBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)
	// The lock gate refuses a dlg-marked lane regardless of force (a live
	// delegate's lane is not the remover's to unlock through remove), so take
	// the crash-residue shape: unlocked lane, retained-idle delegate still
	// blocking it, which is what the force cascade exists for.
	r.unlockLane(t, lanePath)

	out, err := r.removeOp(t, map[string]any{"name": id, "force": true})
	if err != nil {
		t.Fatalf("remove force (sanctioned cascade): %v", err)
	}
	if got := out["branch"]; got != canaryLaneBranch {
		t.Errorf("remove result branch = %v, want %q (the lane's actual branch)", got, canaryLaneBranch)
	}
	assertCanaryLaneGone(t, r, id, lanePath)
}

// The P3 residue sweep (prune sweep 1 through collectLane) must collect an
// unlocked, unchanged, past-grace foreign lane by deleting its named branch.
func TestDelegateLaneBranchCanary_P3ResidueSweepCollectsNamedBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedForeignUnlockedLaneOnBranch(t, canaryLaneBranch)
	r.ageBeyondGrace(t, id)

	r.s.runLaneResidueSweep(context.Background())

	assertCanaryLaneGone(t, r, id, lanePath)
}

// Prune sweep 2 (sidecar reconciliation: worktree gone, branch and sidecar
// remain) must resolve the orphan's named branch — not the sidecar's Name — or
// it would misread the lane as branch-less, delete the sidecar, and strand the
// named branch behind it.
func TestDelegateLaneBranchCanary_PruneSweep2CollectsNamedBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id := r.s.delegateController.newDelegateID()
	lanePath, _, _, _, _, err := r.s.createDelegateWorktree(context.Background(), id, canaryLaneBranch)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	halfRemoveLane(t, r, lanePath)
	ageSidecar(t, r.metaDir(t, r.canonicalMain(t)), id, worktree.ReconcileGrace+time.Minute)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), id)
	if e == nil {
		t.Fatal("the divergent orphan lane was not reported removed")
	}
	if e["reason"] != "unchanged" {
		t.Errorf("reason = %v, want unchanged (tip == base)", e["reason"])
	}
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived sweep-2 collection")
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), id); !os.IsNotExist(scErr) {
		t.Errorf("the sidecar survived: %v", scErr)
	}
}
