package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
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
	assertNoDelegateLane(t, r, delegateID, lanePath)
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived")
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

// A lane record that never resolved its branch (a future caller's omission)
// must not let the mechanics delete the sidecar and strand the real branch
// behind it: nothing is touched, and the lane stays exactly as it was.
func TestDelegateLaneBranchCanary_UnresolvedBranchLeavesLaneForPrune(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)

	local := r.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	controlEnv, _, done, ok := laneControlEnv(local, lanePath)
	if !ok {
		t.Fatal("laneControlEnv failed")
	}
	defer done()
	run := r.s.newWorktreeGitRunner(context.Background(), controlEnv)
	metaDir := metaDirForLane(lanePath)
	locked, reason, lockErr := lockStateOf(run, lanePath)
	if lockErr != nil || !locked {
		t.Fatalf("lock state: locked=%v err=%v", locked, lockErr)
	}
	st := worktree.ClassifyReason(reason, r.s.id, id)

	outcome, _ := r.s.disposeUnchangedLaneMechanics(run, st, isolationLane{delegateID: id, path: lanePath}, metaDir, downgradeUnlockKeep, false)

	if outcome != laneDeclined {
		t.Fatalf("outcome = %v, want laneDeclined (nothing touched)", outcome)
	}
	if !r.lanePresent(lanePath) {
		t.Error("the lane directory was touched")
	}
	if !r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch was touched")
	}
	if _, err := worktree.ReadSidecar(metaDir, id); err != nil {
		t.Errorf("the sidecar was deleted: %v", err)
	}
}

// A pre-existing branch refuses the spawn with zero residue: no sidecar, no
// lane directory, and the existing branch's tip untouched. The refusal is
// newly reachable for delegate lanes — a user-supplied name can collide where
// a freshly-minted delegate id never could.
func TestDelegateLaneBranchCanary_CollisionRefusesWithZeroResidue(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	takenTip := r.commitInMainCheckout(t, "taken-branch", "taken.txt", "taken\n", "advance taken-branch")
	args := delegateArgs{Task: "collide", DelegationAllowance: new(0), Isolation: "worktree", Name: "taken-branch"}
	runtime, reservation, project := reserveWorktreeIsolatedDelegateArgs(t, r.s, args)

	_, err := runtime.prepareIsolation(context.Background(), reservation, project, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision: err = %v, want the branch-exists refusal", err)
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), reservation.delegateID); !os.IsNotExist(scErr) {
		t.Errorf("a refused collision left a sidecar: %v", scErr)
	}
	if r.branchExists(t, reservation.delegateID) {
		t.Error("a refused collision left a delegate-id branch")
	}
	if r.lanePresent(reservation.worktreePath) {
		t.Error("a refused collision left a lane directory")
	}
	if got := strings.TrimSpace(wtGit(t, r.mainRoot, "rev-parse", "refs/heads/taken-branch")); got != takenTip {
		t.Errorf("the taken branch's tip moved: got %s, want %s", got, takenTip)
	}
}

// A legacy sidecar that records no branch disposes by the fallback (the lane
// name), never silently skipping the branch delete.
func TestDelegateLaneBranchCanary_LegacySidecarFallsBackToName(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, "")
	if err := r.s.updateWorktreeSidecar(metaDirForLane(lanePath), id, func(sc *worktree.Sidecar) { sc.Branch = "" }); err != nil {
		t.Fatalf("strip the sidecar's branch: %v", err)
	}

	res, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("dispose a legacy-branch-less lane: %v", err)
	}
	if res.Branch != id {
		t.Errorf("result Branch = %q, want the name fallback %q", res.Branch, id)
	}
	assertNoDelegateLane(t, r, id, lanePath)
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
	assertCanaryLaneGone(t, r, id, lanePath)
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
	assertCanaryLaneGone(t, r, id, lanePath)
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
	assertCanaryLaneGone(t, r, id, lanePath)
}

// remove steps 9-10 (delete_branch) must act on the named branch of a
// record-less divergent lane — the arm the force-cascade canary never reaches,
// because the cascade arm returns before step 9.
func TestDelegateLaneBranchCanary_RemoveDeleteBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedForeignUnlockedLaneOnBranch(t, canaryLaneBranch)

	out, err := r.removeOp(t, map[string]any{"name": id, "delete_branch": true})
	if err != nil {
		t.Fatalf("remove delete_branch: %v", err)
	}
	if got := out["branch"]; got != canaryLaneBranch {
		t.Errorf("remove result branch = %v, want %q", got, canaryLaneBranch)
	}
	if out["branch_deleted"] != true {
		t.Errorf("branch_deleted = %v, want true (unchanged lane: tip == base)", out["branch_deleted"])
	}
	assertCanaryLaneGone(t, r, id, lanePath)
}

// A branch any existing sidecar claims refuses the spawn even when git no
// longer has the branch (the residue a failed sidecar delete can leave after a
// branch collection). Without the refusal a stale sidecar and a new lane would
// both claim the branch, and prune sweep 2 would judge each claim by its own —
// possibly outdated — metadata, deleting the new lane's branch. The stale
// claim itself must be untouched: creation is not prune.
func TestDelegateLaneBranchCanary_CreationRefusesSidecarClaimedBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)
	halfRemoveLane(t, r, lanePath)
	wtGit(t, r.mainRoot, "branch", "-D", canaryLaneBranch)

	args := delegateArgs{Task: "reclaim", DelegationAllowance: new(0), Isolation: "worktree", Name: canaryLaneBranch}
	runtime, reservation, project := reserveWorktreeIsolatedDelegateArgs(t, r.s, args)

	_, err := runtime.prepareIsolation(context.Background(), reservation, project, nil)
	if err == nil || !strings.Contains(err.Error(), "claimed") {
		t.Fatalf("reclaim a sidecar-claimed branch: err = %v, want the claim refusal", err)
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), reservation.delegateID); !os.IsNotExist(scErr) {
		t.Errorf("a refused reclaim left a sidecar: %v", scErr)
	}
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("a refused reclaim created the claimed branch")
	}
	if r.lanePresent(reservation.worktreePath) {
		t.Error("a refused reclaim left a lane directory")
	}
	if _, scErr := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), id); scErr != nil {
		t.Errorf("the refusal deleted the stale claim's sidecar: %v", scErr)
	}
}

// The unresolved-branch guard covers the half-removed arm too: a lane record
// that reaches disposal with no resolved branch must not have its sidecar
// deleted with the real branch stranded behind it. Today's only production
// path resolves via Sidecar.BranchOrName(); the guard exists for a future
// caller that passes an unresolved lane.
func TestDelegateLaneBranchCanary_HalfRemovedUnresolvedBranchRefuses(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLaneOpts(t, canaryLaneBranch)

	local := r.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	controlEnv, _, done, ok := laneControlEnv(local, lanePath)
	if !ok {
		t.Fatal("laneControlEnv failed")
	}
	defer done()
	run := r.s.newWorktreeGitRunner(context.Background(), controlEnv)
	metaDir := metaDirForLane(lanePath)

	// lanePresent=false selects the half-removed arm; that arm never touches
	// the worktree, so the lane's physical presence does not matter to it.
	_, err := r.s.disposeStableExecute(context.Background(), run, stableDelegateWorktreeSnapshot{delegateID: id}, lanePath, metaDir, "", nil, false, worktree.Unlocked, false, false)
	if err == nil || !strings.Contains(err.Error(), "branch was never resolved") {
		t.Fatalf("half-removed disposal with an unresolved branch: err = %v, want the unresolved-branch refusal", err)
	}
	if !r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch was touched")
	}
	if _, serr := worktree.ReadSidecar(metaDir, id); serr != nil {
		t.Errorf("the sidecar was deleted: %v", serr)
	}
}

// A sidecar-less forced remove must derive delete_branch's target from the
// worktree's own checkout, never from the directory name: a named delegate
// lane's directory is its id, and the id is not its branch.
func TestDelegateLaneBranchCanary_RemoveWithoutSidecarDerivesBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, _, _ := r.seedForeignUnlockedLaneOnBranch(t, canaryLaneBranch)
	if err := worktree.DeleteSidecar(r.metaDir(t, r.canonicalMain(t)), id); err != nil {
		t.Fatalf("lose the sidecar: %v", err)
	}

	out, err := r.removeOp(t, map[string]any{"name": id, "force": true, "delete_branch": true})
	if err != nil {
		t.Fatalf("remove a sidecar-less lane: %v", err)
	}
	if got := out["branch"]; got != canaryLaneBranch {
		t.Errorf("remove result branch = %v, want %q (derived from the checkout)", got, canaryLaneBranch)
	}
	if out["branch_deleted"] != true {
		t.Errorf("branch_deleted = %v, want true", out["branch_deleted"])
	}
	if r.branchExists(t, canaryLaneBranch) {
		t.Error("the named branch survived a delete_branch remove")
	}
}

// A sidecar-less lane whose checkout names no branch must refuse
// delete_branch outright: falling back to the directory name would delete
// any unrelated branch that happens to share the delegate id.
func TestDelegateLaneBranchCanary_RemoveWithoutSidecarRefusesUnresolvableBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedForeignUnlockedLaneOnBranch(t, canaryLaneBranch)
	wtGit(t, lanePath, "checkout", "--detach")
	// An unrelated branch that merely shares the delegate id: exactly the
	// branch a directory-name fallback would have deleted.
	wtGit(t, r.mainRoot, "branch", id)
	if err := worktree.DeleteSidecar(r.metaDir(t, r.canonicalMain(t)), id); err != nil {
		t.Fatalf("lose the sidecar: %v", err)
	}

	out, err := r.removeOp(t, map[string]any{"name": id, "force": true, "delete_branch": true})
	if err != nil {
		t.Fatalf("remove a detached sidecar-less lane: %v", err)
	}
	if out["branch_deleted"] == true {
		t.Error("delete_branch deleted a branch it could not attribute")
	}
	if !r.branchExists(t, id) {
		t.Error("the id-named unrelated branch was deleted")
	}
	if !r.branchExists(t, canaryLaneBranch) {
		t.Error("the detached lane's branch was deleted")
	}
}
