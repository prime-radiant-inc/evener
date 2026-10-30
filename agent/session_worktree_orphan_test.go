package agent

import (
	"fmt"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
)

// REAL-git integration tests for the orphaned-delegate reclaim arm (#5471):
// a delegate lane's lock belongs to its parent's disposal lifecycle, and when
// the parent session dies without clean closing, that lifecycle can never
// run — the lane stays locked forever, invisible to every bulk operation,
// reducible only by a manual per-delegate dispose. Prune with
// orphaned_delegate_days releases exactly those locks: parents provably
// unable to act (their store closed the delegate, or the session is gone or
// long-inactive), never a live one, and the ordinary rungs still decide
// removal (live work keeps the lane; only unchanged or merged lanes go).
//
// They build on the wtRepo harness from session_tools_worktree_create_test.go.

// pruneOrphanedOp drives the prune tool with the orphan arm armed.
func (r *wtRepo) pruneOrphanedOp(t *testing.T, days float64) (map[string]any, error) {
	return r.pruneOpArgs(t, map[string]any{"orphaned_delegate_days": days})
}

// seedOrphanParent writes the durable state of a foreign parent session: a
// delegates journal naming one resumable delegate (resumability-closed when
// closed), written through the store's own append API, and a transcript file
// whose mtime carries the parent's last activity — the same signal the
// session listings read.
func seedOrphanParent(t *testing.T, r *wtRepo, sid, dlg string, closed bool, lastActive time.Time) {
	t.Helper()
	store, err := delegatestore.Open(delegateJournalPath(r.stateDir, sid))
	if err != nil {
		t.Fatalf("open parent delegates journal: %v", err)
	}
	defer store.Close()
	events := []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateCreated,
		Seq:        1,
		TS:         time.Now().UTC(),
		DelegateID: dlg,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   "child-of-" + sid,
			TranscriptRef:    "local:child-of-" + sid,
			OwnerSessionID:   sid,
			VisibleSessionID: sid,
			Task:             "scout the area",
			AgentType:        "default",
			Resumable:        true,
			ToolNameCeiling:  []string{"communicate"},
		}},
	}}
	if closed {
		events = append(events, delegatestore.Event{
			Kind:               delegatestore.EventDelegateResumabilityClosed,
			Seq:                2,
			TS:                 time.Now().UTC(),
			DelegateID:         dlg,
			ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "isolation_disposed"},
		})
	}
	if _, _, err := store.AppendBatch(delegatestore.State{}, events); err != nil {
		t.Fatalf("append parent delegate events: %v", err)
	}
	tp := schema.SessionTranscriptPath(r.stateDir, sid)
	if err := os.WriteFile(tp, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write parent transcript: %v", err)
	}
	if err := os.Chtimes(tp, lastActive, lastActive); err != nil {
		t.Fatalf("chtimes parent transcript: %v", err)
	}
}

// delegateLane cuts a managed lane the way a foreign parent's isolation spawn
// leaves it: sidecar stamped with the delegate id, lock carrying the delegate
// marker, the session never inside it. It drives the production create core
// with the delegate-lane event and marker, exactly as createDelegateWorktree
// does for a live parent — only the marker names a foreign session, which is
// the residue the arm exists for.
func (r *wtRepo) delegateLane(t *testing.T, name, dlg, parentSID string) string {
	t.Helper()
	active, ok := r.s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatal("delegateLane requires a local execution environment")
	}
	res, err := r.s.worktreeCreateCore(t.Context(), active, name, name, "", worktree.EvDelegateCreate,
		worktree.FormatDelegateMarker(dlg, parentSID), "orphan-arm test lane",
		func(sc *worktree.Sidecar) { sc.DelegateID = dlg })
	if err != nil {
		t.Fatalf("create delegate lane: %v", err)
	}
	res.Done()
	return res.Path
}

// A stale parent that crashed without clean-closing is the residue the arm
// exists for: its delegate is still resumable in its store, but the session
// has been inactive past the threshold, so its disposal lifecycle will not
// run before the operator judges it abandoned. An unchanged lane of that
// parent is fully reclaimable: released and collected in one pass.
func TestWorktreePrune_OrphanArmCollectsUnchangedStaleParentLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignstale0000000000000"
	const dlg = "dlg_034foreignstale000000"
	lane := r.delegateLane(t, "orphan-unchanged", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, false, time.Now().Add(-30*24*time.Hour))

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-unchanged") == nil {
		t.Fatalf("stale parent's unchanged lane not collected: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); locked {
		t.Errorf("lane still locked after collection: %q", reason)
	}
	if _, statErr := os.Stat(lane); !os.IsNotExist(statErr) {
		t.Errorf("lane worktree survived collection: err=%v", statErr)
	}
}

// The unlock is the deliverable for an orphan holding work: the branch
// stays, the worktree stays, but the lock goes, so the lane is visible to
// the merge and force paths that were the only cleanup before — and to a
// later prune once its work lands.
func TestWorktreePrune_OrphanArmUnlocksButKeepsUnmergedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignstale0000000000001"
	const dlg = "dlg_034foreignstale000001"
	lane := r.delegateLane(t, "orphan-unmerged", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, false, time.Now().Add(-30*24*time.Hour))
	// Unmerged work: a commit only this branch carries.
	commitInWorktree(t, lane, "unmerged.txt", "work the gates must keep", "unmerged work")

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-unmerged") != nil {
		t.Fatalf("an unmerged lane was collected: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-unmerged") == nil {
		t.Fatalf("the orphaned lane was not reported unlocked: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); locked {
		t.Errorf("work-holding orphan still locked: %q", reason)
	}
	if _, statErr := os.Stat(lane); statErr != nil {
		t.Errorf("work-holding orphan's worktree removed: %v", statErr)
	}
	if !branchExistsInRepo(t, r.mainRoot, "orphan-unmerged") {
		t.Error("work-holding orphan's branch deleted")
	}
}

// A parent active inside the threshold is not judged abandoned: its lane
// keeps its lock whatever the flag says.
func TestWorktreePrune_OrphanArmSparesActiveParent(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignlive0000000000000"
	const dlg = "dlg_034foreignlive000000"
	lane := r.delegateLane(t, "orphan-active-parent", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, false, time.Now())

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-active-parent"); e != nil {
		t.Fatalf("active parent's lane unlocked: %+v", e)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-active-parent"); e != nil {
		t.Fatalf("active parent's lane collected: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("active parent's lock released: %q", reason)
	}
}

// A delegate whose own store closed its resumability is dead regardless of
// activity: its disposal lifecycle already ran, and only the lane's lock
// lagged behind it.
func TestWorktreePrune_OrphanArmReleasesStoreDeadDelegate(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreigndead0000000000000"
	const dlg = "dlg_034foreigndead000000"
	lane := r.delegateLane(t, "orphan-store-dead", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, true, time.Now())

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-store-dead") == nil {
		t.Fatalf("store-dead delegate's unchanged lane not collected: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); locked {
		t.Errorf("store-dead delegate's lane still locked: %q", reason)
	}
}

// Without the flag the locked-lane rule stays absolute: today's behavior.
func TestWorktreePrune_OrphanArmOffByDefault(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignstale0000000000002"
	const dlg = "dlg_034foreignstale000002"
	lane := r.delegateLane(t, "orphan-flag-off", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, false, time.Now().Add(-30*24*time.Hour))

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "orphan-flag-off"); e == nil {
		t.Fatalf("locked lane not skipped without the flag: %+v", out)
	} else if got, want := fmt.Sprint(e["reason"]), "locked ("+worktree.FormatDelegateMarker(dlg, parentSID)+")"; got != want {
		t.Errorf("skip reason = %q, want %q", got, want)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("lock released without the flag: %q", reason)
	}
}

// This session's own delegates are not the arm's business: their disposer
// can still act, through the dispose operation that exists for exactly that.
func TestWorktreePrune_OrphanArmSparesOwnSessionDelegates(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const dlg = "dlg_034ownsdlginflagtest0"
	lane := r.delegateLane(t, "own-delegate-lane", dlg, r.s.id)

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "own-delegate-lane"); e == nil {
		t.Fatalf("own session's delegate lane not skipped: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("own session's delegate lock released: %q", reason)
	}
}

// The marker and the sidecar must name the same delegate: a lane whose
// provenance disagrees with the lock the plan was made around stays locked.
func TestWorktreePrune_OrphanArmSparesSidecarMismatch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignstale0000000000003"
	const dlg = "dlg_034foreignstale000003"
	lane := r.delegateLane(t, "orphan-mismatch", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, false, time.Now().Add(-30*24*time.Hour))
	// Someone rewrote the sidecar to name a different delegate.
	metaDir := r.metaDir(t, r.canonicalMain(t))
	if err := worktree.UpdateSidecar(metaDir, "orphan-mismatch", func(sc *worktree.Sidecar) {
		sc.DelegateID = "dlg_034somebodyelse00000"
	}); err != nil {
		t.Fatalf("rewrite sidecar: %v", err)
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "skipped"), "orphan-mismatch"); e == nil {
		t.Fatalf("sidecar-mismatch lane not skipped: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("sidecar-mismatch lane's lock released: %q", reason)
	}
}
