package agent

// Dead-owner delegate lane collection (issue #5471): a delegate isolation lane
// carries an `evener:dlg:<dlg>:<owner>` occupancy lock that only the owner
// session's own disposal lifecycle — `dispose`, or its close pass — ever
// releases. When the owner's process dies without a clean close, the lock
// outlives the session tree and `prune` reported the lane "locked" forever,
// so dead owners' lanes accumulate (the issue observed 58 locked trees holding
// ~6.7 GB that no bulk prune could touch).
//
// These tests pin the recovery contract: `prune` collects a lane whose owner
// session tree is VERIFIABLY dead — provable because the root session's
// livelock file exists on disk with no live process holding its flock (the OS
// released the advisory lock when the process died) — and keeps the lock on an
// owner whose liveness cannot be verified. The live-owner preservation cases
// that must hold a real flock live in the _unix sibling of this file.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

const (
	deadOwnerDlgID   = "dlg_034deadlanelane01"
	deadOwnerRootSID = "034deadownerrootsession"
	deadOwnerSubSID  = "034deadownersubsession1"
)

// seedOwnerSessionMeta writes the minimal session meta the owner-liveness walk
// reads: <stateDir>/sessions/<sid>.meta.json with the subagent flag and the
// parent link. isSubagent=true with a parent names one hop of a session tree.
func seedOwnerSessionMeta(t *testing.T, stateDir, sid string, isSubagent bool, parentSID string) {
	t.Helper()
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":                sid,
		"is_subagent":       isSubagent,
		"parent_session_id": parentSID,
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".meta.json"), body, 0o644); err != nil {
		t.Fatalf("write %s meta: %v", sid, err)
	}
}

// seedDeadLivelock creates the root session's livelock file with no process
// holding it: the on-disk state a root session process leaves behind once it
// is gone. A live process holds the file's flock for its lifetime, and the OS
// releases that at process exit — so a present-but-unheld livelock is positive
// proof the owner's process is dead.
func seedDeadLivelock(t *testing.T, stateDir, sid string) {
	t.Helper()
	path := filepath.Join(stateDir, "sessions", sid+".livelock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create dead livelock: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close dead livelock: %v", err)
	}
}

// addDelegateLaneFixture creates a managed worktree lane named for a delegate
// id, records the delegate sidecar (delegate id + creator session), and locks
// it with the `evener:dlg:` marker — the on-disk shape of a delegate isolation
// lane. ownerSID is the session id the marker names as the disposal owner.
// Real git, not the scripted harness: the assertions depend on git's own
// worktree-lock file and porcelain reason surviving a prune pass unclaimed.
func (r *wtRepo) addDelegateLaneFixture(t *testing.T, dlgID, ownerSID string) (path, metaDir string) {
	t.Helper()
	// Hermetic rendezvous: the oracle consults the (real) rendezvous run
	// directory when the seam is empty; point it at an empty temp dir so these
	// tests never read the developer's live daemons — unless the test already
	// pointed the seam at its own fixture entries.
	if r.s.worktreeRendezvousDir == "" {
		r.s.worktreeRendezvousDir = t.TempDir()
	}
	path = r.addManagedWorktreeFixture(t, dlgID)
	metaDir = r.metaDir(t, r.canonicalMain(t))
	if err := worktree.UpdateSidecar(metaDir, dlgID, func(sc *worktree.Sidecar) {
		sc.DelegateID = dlgID
		sc.CreatorSession = ownerSID
	}); err != nil {
		t.Fatalf("update %s sidecar: %v", dlgID, err)
	}
	marker := "evener:dlg:" + dlgID + ":" + ownerSID
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", marker, path)
	return path, metaDir
}

// delegateLaneLockedViaPorcelain reports whether the lane at path still carries
// the given marker, straight from git's own porcelain output.
func (r *wtRepo) delegateLaneLockedViaPorcelain(t *testing.T, path, marker string) bool {
	t.Helper()
	e := r.porcelainEntry(t, path)
	return e.Locked && e.LockReason == marker
}

// TestWorktreePrune_DeadOwnerDelegateLaneCollected: the issue's core case. A
// delegate lane whose owner root session is verifiably dead (livelock present,
// unheld) is collectible by prune, not refuse-locked forever.
func TestWorktreePrune_DeadOwnerDelegateLaneCollected(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, metaDir := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not collected; skipped: %+v", deadOwnerDlgID, pruneEntries(t, out, "skipped"))
	}
	if e["reason"] != "unchanged" {
		t.Errorf("reason = %v, want unchanged", e["reason"])
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("dead-owner lane worktree survived prune: err=%v", statErr)
	}
	if branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("dead-owner lane branch survived prune")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, deadOwnerDlgID); !os.IsNotExist(scErr) {
		t.Errorf("dead-owner lane sidecar survived: err=%v", scErr)
	}
}

// TestWorktreePrune_DeadOwnerUnmergedLaneKeepsLock: a dead owner authorizes
// nothing beyond what an owner's own close would do. A lane with unmerged
// commits is skipped by the merge gate, and the release happens only on the
// path that actually collects — so the lock is still on the lane afterward,
// exactly as protected as before the prune.
func TestWorktreePrune_DeadOwnerUnmergedLaneKeepsLock(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID
	commitInWorktree(t, path, "u.txt", "u\n", "advance dead-owner lane")

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "unmerged") {
		t.Errorf("reason = %q, want it to name the merge gate that actually kept the lane", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("unmerged dead-owner lane lost its lock: the release must happen only on the path that collects")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("unmerged dead-owner lane branch was deleted")
	}
}

// TestWorktreePrune_UnverifiableOwnerDelegateLaneKept: a lock whose owner has
// no live-process record at all (a session from before livelocks existed, or a
// missing meta) is not guessed at. The lane stays locked and the skip reason
// says the liveness could not be verified, so the deliberate manual unlock of
// the docs' recovery path stays a human decision.
func TestWorktreePrune_UnverifiableOwnerDelegateLaneKept(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	// Owner meta exists, but no livelock file and no rendezvous entry: nothing
	// can prove the owner gone.
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	reason, _ := e["reason"].(string)
	if !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal that keeps the lane untouched", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("unverifiable-owner lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("unverifiable-owner lane branch was deleted")
	}
}

// TestWorktreePrune_NestedOwnerDeadRootCollected: a nested delegate lane's
// marker names a subagent session as its owner, but a subagent never hosts a
// process — its root does. Liveness must resolve through the parent chain to
// the root: when the root is verifiably dead, the nested lane is collected.
func TestWorktreePrune_NestedOwnerDeadRootCollected(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerSubSID, true, deadOwnerRootSID)
	path, metaDir := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerSubSID)

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not collected; skipped: %+v", deadOwnerDlgID, pruneEntries(t, out, "skipped"))
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("nested dead-owner lane worktree survived prune: err=%v", statErr)
	}
	if branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("nested dead-owner lane branch survived prune")
	}
	if _, scErr := worktree.ReadSidecar(metaDir, deadOwnerDlgID); !os.IsNotExist(scErr) {
		t.Errorf("nested dead-owner lane sidecar survived: err=%v", scErr)
	}
}
