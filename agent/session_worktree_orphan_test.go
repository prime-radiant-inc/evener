package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
// delegates journal naming one resumable delegate (resumability closed with
// closureReason when non-empty), written through the store's own append API,
// and a transcript file whose mtime carries the parent's last activity — the
// same signal the session listings read.
func seedOrphanParent(t *testing.T, r *wtRepo, sid, dlg, closureReason string, lastActive time.Time) {
	t.Helper()
	store, err := delegatestore.Open(schema.SessionDelegateJournalPath(r.stateDir, sid))
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
	if closureReason != "" {
		events = append(events, delegatestore.Event{
			Kind:               delegatestore.EventDelegateResumabilityClosed,
			Seq:                2,
			TS:                 time.Now().UTC(),
			DelegateID:         dlg,
			ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: closureReason},
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
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))

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

// A held livelock is the one durable witness that a live process owns the
// session id, and it outranks the transcript clock: a daemon from before the
// activity-evidence refresh leaves a live tree's transcript stale for a whole
// long command's run, so inactivity alone must not release a live owner's
// lane. Crashes release the flock with the process, so once the livelock is
// gone the same stale parent is reclaimable again.
func TestWorktreePrune_OrphanArmWithholdsReleaseWhileLivelockHeld(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034livestaleparent00000001"
	const dlg = "dlg_034livestaleparent000"
	lane := r.delegateLane(t, "orphan-live-owner", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))
	release, applied, err := schema.AcquireSessionLivelock(r.stateDir, parentSID)
	if err != nil || !applied || release == nil {
		t.Fatalf("hold parent livelock: applied=%t err=%v", applied, err)
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune with live owner: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-live-owner") != nil {
		t.Fatalf("a live owner's lane was collected on stale-evidence inactivity: %+v", out)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-live-owner"); e != nil {
		t.Fatalf("a live owner's lane was reported unlocked: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("a live owner's lane was released: %q", reason)
	}

	// The flock dies with the process: once released, the same stale parent
	// is the crash residue the arm exists for.
	release()
	out, err = r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune after livelock release: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-live-owner") == nil {
		t.Fatalf("stale parent's lane not collected after the livelock released: %+v", out)
	}
}

// A nested lane's marker names its immediate parent subagent, and subagents
// never take a livelock: only the hosting root's process does. The guard
// must resolve the parent to its tree root and probe the ROOT's livelock, or
// a live tree's nested lanes lose the live-owner protection entirely — the
// upgrade window again, one level down.
func TestWorktreePrune_OrphanArmSparesNestedLaneWhileRootHoldsLivelock(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const rootSID = "034nestedroot00000000000000"
	const parentSID = "034nestedsub000000000000000"
	const dlg = "dlg_034nestedsub00000000"
	lane := r.delegateLane(t, "orphan-nested-live", dlg, parentSID)
	// The immediate parent is a subagent hosted by the tree root: its meta
	// chains upward, and the root's process is the only thing a livelock can
	// witness.
	if err := schema.SaveSessionMeta(r.stateDir, schema.SessionMeta{ID: parentSID, IsSubagent: true, ParentSessionID: rootSID}); err != nil {
		t.Fatalf("save nested parent meta: %v", err)
	}
	if err := schema.SaveSessionMeta(r.stateDir, schema.SessionMeta{ID: rootSID}); err != nil {
		t.Fatalf("save tree root meta: %v", err)
	}
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))
	release, applied, err := schema.AcquireSessionLivelock(r.stateDir, rootSID)
	if err != nil || !applied || release == nil {
		t.Fatalf("hold tree root livelock: applied=%t err=%v", applied, err)
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune with live tree root: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-nested-live") != nil {
		t.Fatalf("a live tree's nested lane was collected on stale-evidence inactivity: %+v", out)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-nested-live"); e != nil {
		t.Fatalf("a live tree's nested lane was reported unlocked: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("a live tree's nested lane was released: %q", reason)
	}

	// The flock dies with the root's process: once released, the same stale
	// nested parent is reclaimable again.
	release()
	out, err = r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune after the root's livelock released: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-nested-live") == nil {
		t.Fatalf("stale nested parent's lane not collected after the root's livelock released: %+v", out)
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
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))
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
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now())

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
	seedOrphanParent(t, r, parentSID, dlg, stableWorktreeDisposalReason, time.Now())

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
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "skipped"), "orphan-flag-off") == nil {
		t.Fatalf("locked lane not skipped without the flag: %+v", out)
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
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))
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

// A turn-budget-exhausted delegate is not disposal residue. Exhaustion also
// closes resumability, but without the quiescence-checked isolation disposal
// — the exhaustion path does not drain the lane's shells — so an active
// parent's lane keeps its lock even with the arm on: only the recorded
// isolation disposal proves the parent's disposal lifecycle ran.
func TestWorktreePrune_OrphanArmSparesTurnExhaustedDelegate(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignlive0000000000001"
	const dlg = "dlg_034foreignlive000001"
	lane := r.delegateLane(t, "orphan-turn-exhausted", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, "turn_budget_exhausted", time.Now())

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-turn-exhausted"); e != nil {
		t.Fatalf("turn-exhausted delegate's lane unlocked: %+v", e)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-turn-exhausted"); e != nil {
		t.Fatalf("turn-exhausted delegate's lane collected: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("turn-exhausted delegate's lock released: %q", reason)
	}
}

// A nested delegate's lifecycle authority is its tree root's journal, not its
// immediate parent's: the arm resolves the root before reading, so a
// root-disposed delegate's lane is reclaimable even while its immediate
// parent's transcript is fresh.
func TestWorktreePrune_OrphanArmResolvesNestedParentJournal(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const rootSID = "034foreignroot0000000000000"
	const parentSID = "034foreignnest0000000000000"
	const dlg = "dlg_034foreignnest000000"
	lane := r.delegateLane(t, "orphan-nested", dlg, parentSID)
	// The immediate parent's meta names the tree root whose journal holds the
	// delegate; the parent's own transcript stays fresh, so only the store can
	// settle the lane.
	if err := schema.SaveSessionMeta(r.stateDir, schema.SessionMeta{ID: parentSID, JobTreeRootSessionID: rootSID}); err != nil {
		t.Fatalf("save parent meta: %v", err)
	}
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now())
	seedOrphanParent(t, r, rootSID, dlg, stableWorktreeDisposalReason, time.Now())

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-nested") == nil {
		t.Fatalf("nested delegate's disposed lane not collected: %+v", out)
	}
	if _, locked, reason := r.laneLocked(t, lane); locked {
		t.Errorf("nested delegate's lane still locked: %q", reason)
	}
}

// A session without a state directory cannot judge any parent's activity:
// the transcript stat would resolve against the process working directory
// and answer "missing" even for a live, running parent — the arm would treat
// every foreign delegate lane as orphaned. The arm disarms instead.
func TestOrphanArmDisarmsWithoutStateDir(t *testing.T) {
	t.Parallel()
	s := &Session{id: "034nonpersistentses0000", stateDir: ""}
	if _, _, ok := s.orphanedDelegateMarker(context.Background(),
		worktree.FormatDelegateMarker("dlg_034somebody00000", "034someparent000000000"),
		7*24*time.Hour, time.Now(), nil); ok {
		t.Fatal("the orphan arm claimed a lane without a state directory")
	}
}

// The arm's safety contract runs both directions: a released lane must be
// re-locked by the parent's own revival. Close-time disposal, the orphan arm,
// and the documented manual unlock all leave a kept lane unlocked, so a
// delegate_send that restarts the child must re-take the marker before the
// child runs another turn in it.
func TestDelegateReviveReLocksUnlockedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "revive-unlocked", "dlg_revivetest00000", "034placeholder000000000")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("revive an unlocked lane: %v", err)
	}
	if restored {
		sub.sess.discardRestoredCandidate()
	}
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))

	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatal("revived delegate's lane left unlocked")
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != root.id {
		t.Fatalf("revived lane's lock = %q, want this session's marker for %s", reason, fixture.delegateID)
	}
}

// A lane the release exposed may have been adopted by another session since.
// Revival refuses rather than run the child in a checkout someone else owns,
// and never displaces the newer lock.
func TestDelegateReviveRefusesForeignLockedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "revive-foreign", "dlg_revivetest00001", "034placeholder000000001")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	const foreignSID = "034somebodyelse00000000000"
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	// The way a real adoption locks: another session switches in and locks
	// under its own session marker. (A delegate marker's identity is its
	// delegate id — lockstate classifies the same dlg id as that delegate's
	// own marker whatever session id it carries — so this is the shape that
	// genuinely belongs to someone else.)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath)

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); err == nil {
		t.Fatal("revive proceeded into a lane held by another session")
	}

	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatal("the foreign lock was displaced")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("foreign lock = %q, want the other session's marker", reason)
	}
}

// A retained child is the other revive path: a second delegate_send returns
// the resident child without reconstructing it, so that return must carry the
// same lane revalidation — the arm's released lock cannot be left off a child
// that is about to run another turn.
func TestDelegateSendReLocksUnlockedLaneForRetainedChild(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "revive-retained", "dlg_revivetest00002", "034placeholder000000002")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	// The lane was cut under a stand-in identity; re-lock it under the delegate
	// that owns it, the shape a real spawn leaves.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	if first.result.TimedOut {
		t.Fatalf("the lane-owning delegate's first send timed out; the presentation window cannot start with the spawn still in flight")
	}
	if sub := root.subagents.get(fixture.childID); sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The end state the orphan arm and the manual unlock leave.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)

	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	second := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "second turn", 10_000)
	if second.result.Err != nil {
		t.Fatalf("second send: %v", second.result.Err)
	}

	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatal("retained child ran another turn without the lane re-locked")
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != root.id {
		t.Fatalf("re-locked lane's lock = %q, want this session's marker for %s", reason, fixture.delegateID)
	}
}

// A removed lane must fail revival loudly. The arm (or dispose) removes only
// lanes the ordinary gates judged disposable, but a cold delegate's environment
// reconstruction does not validate that the recorded working directory still
// exists, so without this refusal a send could launch the provider into a
// checkout that is no longer there.
func TestDelegateReviveRefusesGoneLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "revive-gone", "dlg_revivetest00003", "034placeholder000000003")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir, testConfig{sandboxProber: unsandboxedProber(fixture.workspace)})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	// What a collection leaves: no checkout directory at all — removed while
	// this parent stays resident, the shape an armed prune leaves behind.
	if err := os.RemoveAll(lanePath); err != nil {
		t.Fatalf("remove the collected lane's directory: %v", err)
	}
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	_, _, err = (delegateRuntime{owner: root}).restoreIdle(started)
	if err == nil {
		t.Fatal("revive proceeded into a removed lane")
	}
}

// The arm's residue can wake a retained child through the automatic paths
// too, not just an explicit send: a notification-driven drive turn launches
// on the retained child's session. That launch revalidates the lane the same
// way — the released lock is re-taken before the turn runs.
func TestDelegateWakeReLocksUnlockedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "wake-relock", "dlg_waketest0000000", "034placeholder000000004")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The end state the orphan arm and the manual unlock leave.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)

	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	if !root.driveSubagentNotificationTurn(sub) {
		t.Fatal("notification drive skipped the idle child")
	}

	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatal("notification turn launched without the lane re-locked")
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != root.id {
		t.Fatalf("re-locked lane's lock = %q, want this session's marker for %s", reason, fixture.delegateID)
	}
}

// And when someone else holds the released lane, the automatic wake must not
// launch into their checkout: the drive reports not-launched and the newer
// lock is never touched.
func TestDelegateWakeRefusesForeignHeldLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "wake-foreign", "dlg_waketest0000001", "034placeholder000000005")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	const foreignSID = "034somebodyelse00000000001"
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath)

	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	if root.driveSubagentNotificationTurn(sub) {
		t.Fatal("notification drive launched into a lane held by another session")
	}

	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatal("the foreign lock was displaced")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("foreign lock = %q, want the other session's marker", reason)
	}
}

// sharedDescendantLane is a delegate lane whose owner's turn has spawned a
// retained non-isolated child: a delegate that runs in the lane without
// owning it, and whose notification wake therefore validates an ancestor's
// lane, not a lane of its own.
type sharedDescendantLane struct {
	fixture          coldDelegateFixture
	lanePath         string
	root             *Session  // the fixture's root, which owns the lane-owning delegate
	delegateSub      *subagent // the lane-owning delegate, as root sees it
	delegateSess     *Session  // the lane-owning delegate's runtime, which drives the child's wake
	sharedSub        *subagent
	sharedDelegateID string
	finish           func(llm.Request) llm.Response
	// classifications records every delegateDeliveryClassified fire in this
	// tree, so a tripwire can name whether the retained child's report reached
	// the delivery admission path at all and with which deferral.
	classifications *delegateDeliveryClassificationLog
}

// delegateDeliveryClassificationLog is the thread-safe record behind the
// fixture's delegateDeliveryClassified hook.
type delegateDeliveryClassificationLog struct {
	mu     sync.Mutex
	events []string
}

func (l *delegateDeliveryClassificationLog) observe(s *Session, deferred bool) {
	if l == nil || s == nil {
		return
	}
	l.mu.Lock()
	l.events = append(l.events, fmt.Sprintf("%s deferred=%t", s.id, deferred))
	l.mu.Unlock()
}

func (l *delegateDeliveryClassificationLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// spawnSharedDescendantLane builds that tree: a cold fixture delegate with an
// isolated lane, restored and sent once through a turn that spawns exactly
// one non-isolated child sharing the lane and then finishes, leaving both the
// delegate and its child retained. The fixture lives in the repo harness's
// state dir, so a harness-session prune reads the same parent evidence the
// orphan arm reads. notificationTurn, when given, scripts the delegate's
// attention turn presenting the retained child's report up front — the fake
// adapter serves its steps by a cursor that survives reassignment, so a step
// appended after the cursor would never be served.
func spawnSharedDescendantLane(t *testing.T, r *wtRepo, laneName, standInDlg, standInSID string, notificationTurn ...func(llm.Request) llm.Response) *sharedDescendantLane {
	t.Helper()
	var lanePath string
	fixture := newColdDelegateFixtureConfiguredInState(t, "", r.stateDir, func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		d.ToolNameCeiling = []string{"communicate", "delegate"}
		d.DelegationAllowance = 1
		lanePath = r.delegateLane(t, laneName, standInDlg, standInSID)
		d.WorkingDir = lanePath
	})
	// The tree must not release on its own (the idle-release machinery claims
	// settled subtree leaves on a schedule the test cannot see) and must not
	// depend on the runner having a sandbox wrapper: the wake's lane validation
	// runs git through the delegate session's own environment, and the CI
	// runners have no bwrap to wrap it with — a capable-prober fixture would
	// exit 127 on every git call there.
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir, testConfig{
		sandboxProber:              unsandboxedProber(fixture.workspace),
		disableDelegateIdleRelease: true,
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(root.Close)
	// Record delivery classification across the whole tree. The hook rides the
	// root's test config into every child session created afterwards
	// (subagentConfigFromFrozenDescriptor re-takes testOnly from the live
	// parent), so it names whether the child's report reached delivery
	// admission and whether that admission was deferred.
	classifications := &delegateDeliveryClassificationLog{}
	root.cfg.testOnly.delegateDeliveryClassified = classifications.observe
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	spawn := func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{
			ID:        "call_spawn_child",
			Name:      "delegate",
			Arguments: json.RawMessage(`{"prompt":"grandchild sentinel task"}`),
			Type:      "function",
		})
	}
	notify := finish
	if len(notificationTurn) > 0 && notificationTurn[0] != nil {
		notify = notificationTurn[0]
	}
	// The delegate's turn spawns its child, the child's turn finishes, the
	// delegate's follow-up turn finishes, and the delegate's attention turn
	// presenting the retained child's report answers the scripted step (or the
	// adapter's bare-text fallback when none is given).
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{spawn, finish, finish, notify})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	delegateSub := root.subagents.get(fixture.childID)
	if delegateSub == nil || delegateSub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The shared child is found through the tree's durable records by its
	// parent edge, the way the tree's own tests do.
	var sharedDelegateID, sharedChildID string
	shared := 0
	tree := root.delegateController
	tree.mu.Lock()
	for id, agg := range tree.durable {
		if agg.Descriptor.ParentDelegateID == fixture.delegateID {
			shared++
			sharedDelegateID = id
			sharedChildID = agg.Descriptor.ChildSessionID
		}
	}
	tree.mu.Unlock()
	if shared != 1 {
		t.Fatalf("the lane-owning delegate spawned %d children, want exactly 1", shared)
	}
	sharedSub := delegateSub.sess.subagents.get(sharedChildID)
	if sharedSub == nil || sharedSub.sess == nil {
		t.Fatalf("the delegate's child %s is not retained after its run", sharedChildID)
	}
	return &sharedDescendantLane{
		fixture:          fixture,
		lanePath:         lanePath,
		root:             root,
		delegateSub:      delegateSub,
		delegateSess:     delegateSub.sess,
		sharedSub:        sharedSub,
		sharedDelegateID: sharedDelegateID,
		finish:           finish,
		classifications:  classifications,
	}
}

// adoptForeignLane installs a foreign session's lock on the lane, contending
// with the tree's own wake machinery: a background wake for the shared child
// revalidates the lane at any moment and re-takes a just-released lane under
// the tree's own marker — the behavior under test — so the adoption retries
// until the foreign lock is the one that holds.
func adoptForeignLane(t *testing.T, r *wtRepo, lanePath, foreignSID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, locked, _ := r.laneLocked(t, lanePath); locked {
			_, _ = runWorktreeGit(r.mainRoot, "worktree", "unlock", lanePath)
		}
		if _, lockErr := runWorktreeGit(r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath); lockErr == nil {
			// git took the lock, but require the lock that reads back to be
			// the foreign one: a background wake re-takes any gap, and only
			// the state, not the command's success, is the adoption.
			if _, locked, reason := r.laneLocked(t, lanePath); locked {
				if m, ok := worktree.ParseMarker(reason); ok && m.SessionID == foreignSID {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			_, _, reason := r.laneLocked(t, lanePath)
			t.Fatalf("adopting the lane for a foreign session kept losing to the tree's own wake: lane lock = %q", reason)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A shared descendant's wake must not launch into an ancestor lane another
// session has adopted: the drive reports not-launched and the newer lock is
// never touched, exactly as a lane-owning delegate's wake does.
func TestDelegateWakeRefusesForeignHeldInheritedLane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	lane := spawnSharedDescendantLane(t, r, "wake-inherited-foreign", "dlg_waketest0000003", "034placeholder000000007")
	const foreignSID = "034somebodyelse00000000002"
	adoptForeignLane(t, r, lane.lanePath, foreignSID)
	// The precondition the wake must refuse, verified where a failure can
	// name which side broke.
	if _, locked, reason := r.laneLocked(t, lane.lanePath); !locked {
		t.Fatalf("the shared child's lane is unlocked before its wake: %q", reason)
	} else if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("the shared child's lane lock before its wake = %q, want the foreign session's marker", reason)
	}

	lane.fixture.adapter.setSteps([]func(llm.Request) llm.Response{lane.finish})
	if lane.delegateSess.driveSubagentNotificationTurn(lane.sharedSub) {
		_, locked, reason := r.laneLocked(t, lane.lanePath)
		revalErr := lane.delegateSess.revalidateLaneForLaunch(lane.sharedDelegateID)
		t.Fatalf("shared descendant's wake launched into a lane held by another session (lane locked=%v reason=%q, lane validation now: %v)",
			locked, reason, revalErr)
	}

	_, locked, reason := r.laneLocked(t, lane.lanePath)
	if !locked {
		t.Fatal("the foreign lock was displaced")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("foreign lock = %q, want the other session's marker", reason)
	}
}

// Two launches sharing an inherited lane race to re-take it: the per-child
// guards serialize each child's own drives, not siblings, so both can inspect
// the lane unlocked and both reach for the lock. The loser's lock fails on
// git's "already locked" once the winner's has landed, and that failure must
// not fail the launch — the tree owns the lane now. The scripted runner
// forces the losing interleave with real git: the sibling's lock lands inside
// the loser's inspect-to-lock gap, and the loser's own lock then really
// refuses.
func TestDelegateLaunchAdoptsSiblingWonLaneAfterLockLoss(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	lane := spawnSharedDescendantLane(t, r, "sibling-won-lane", "dlg_waketest0000006", "034placeholder000000010")
	// The end state the orphan arm and the manual unlock leave.
	wtGit(t, r.mainRoot, "worktree", "unlock", lane.lanePath)

	var siblingWon atomic.Bool
	lane.delegateSess.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "worktree" && args[1] == "lock" && siblingWon.CompareAndSwap(false, true) {
				// The sibling launch takes the lane inside the loser's gap
				// with the tree's own marker, so the loser's real lock
				// following it refuses as already locked.
				if _, err := inner("worktree", "lock", "--reason", worktree.FormatDelegateMarker(lane.fixture.delegateID, lane.fixture.meta.ID), lane.lanePath); err != nil {
					return "", err
				}
			}
			return inner(args...)
		}
	}

	err := lane.delegateSess.revalidateLaneForLaunch(lane.sharedDelegateID)
	if err != nil {
		t.Fatalf("a launch whose sibling won the shared lane failed: %v", err)
	}
	_, locked, reason := r.laneLocked(t, lane.lanePath)
	if !locked {
		t.Fatal("the shared lane is left unlocked")
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != lane.fixture.delegateID || m.SessionID != lane.fixture.meta.ID {
		t.Fatalf("shared lane's lock = %q, want the lane-owning delegate's marker naming its parent session", reason)
	}
}

// spawnRetainedDelegateLane builds the smallest tree the orphan arm can
// misjudge: one isolation delegate whose lane is locked under its parent's
// marker and whose first turn finished, leaving the delegate retained and the
// parent idle. No other child exists, so nothing in the tree can refresh the
// parent's activity evidence on its own. The fixture lives in the repo
// harness's state dir, so the harness session's prune reads the same parent
// evidence the arm reads.
func spawnRetainedDelegateLane(t *testing.T, r *wtRepo, laneName, standInDlg, standInSID string, turns ...func(llm.Request) llm.Response) (coldDelegateFixture, *Session, string) {
	t.Helper()
	var lanePath string
	fixture := newColdDelegateFixtureConfiguredInState(t, "", r.stateDir, func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		d.ToolNameCeiling = []string{"communicate", "delegate", "shell"}
		d.DelegationAllowance = 1
		lanePath = r.delegateLane(t, laneName, standInDlg, standInSID)
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir, testConfig{
		sandboxProber:              unsandboxedProber(fixture.workspace),
		disableDelegateIdleRelease: true,
	})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(root.Close)
	// The lane ends locked under the real delegate's marker, the state a
	// live parent's isolation spawn leaves.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	// The retained delegate's follow-up turns are scripted up front: the
	// fake adapter serves its steps by a cursor that survives reassignment,
	// so a later slice replacement after the cursor would never be served.
	// A turn that ends on its own final response never consumes the trailing
	// steps; a tool-calling one answers its result with them, and the second
	// trailing finish covers any completion nudge that follows.
	steps := []func(llm.Request) llm.Response{finish}
	steps = append(steps, turns...)
	trailingFinishes := []func(llm.Request) llm.Response{finish, finish}
	steps = append(steps, trailingFinishes...)
	fixture.adapter.setSteps(steps)
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil || first.commit == nil {
		t.Fatalf("first send: %+v", first.result)
	}
	// Acknowledge the report the way the parent's delegate_send does, or the
	// finished generation never finalizes and no later start is admitted.
	plans, err := first.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge the first turn: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("apply the first turn's acknowledgment: %v", err)
	}
	return fixture, root, lanePath
}

// A delegate turn that outlasts the operator's inactivity threshold keeps its
// marker parent idle — the parent runs no turn of its own while it waits — so
// the activity evidence the orphan reclaim reads goes stale mid-task even
// though the tree is alive and working. The launch validation refreshes that
// evidence once per launch; the turn's own ongoing activity reports must keep
// it fresh, or a foreign session's prune — whose live-work view cannot see the
// running turn's work under the lane — judges the parent long-inactive and
// collects the clean, unchanged checkout mid-task.
func TestWorktreePrune_OrphanArmSparesLaneWhileDelegateReportsActivity(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)

	// The delegate's second turn wedges inside its first model call: the
	// delegate is running, the tree is alive, and the parent stays idle.
	entered := make(chan struct{})
	release := make(chan struct{})
	var wedgeOnce sync.Once
	wedge := func(llm.Request) llm.Response {
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "active-delegate-lane", "dlg_orphanactive000001", "034orphanactive0001", wedge)
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)

	sendOutcome := make(chan delegateSendOutcome, 1)
	go func() {
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "long turn", 60_000)
		sendOutcome <- outcome
	}()
	select {
	case <-entered:
	case outcome := <-sendOutcome:
		t.Fatalf("the long turn's send returned before it started: %+v", outcome.result)
	// TRIPWIRE: the scripted wedge turn starts in-process in milliseconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		delegateSub := root.subagents.get(fixture.childID)
		var running, driving, subClosed bool
		present := delegateSub != nil
		if present {
			delegateSub.mu.Lock()
			running, driving, subClosed = delegateSub.running, delegateSub.driving, delegateSub.closed
			delegateSub.mu.Unlock()
		}
		t.Fatalf("the delegate's long turn never started (sub present=%v running=%v driving=%v closed=%v)",
			present, running, driving, subClosed)
	}

	// The parent has now been idle past the operator's threshold: nothing of
	// its own has touched its transcript since the launch refreshed it.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The running turn reports activity through the production closure its
	// send installed — the same path its own phase reports take — once the
	// report publication floor has cleared since the turn started.
	delegateSub := root.subagents.get(fixture.childID)
	if delegateSub == nil || delegateSub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	time.Sleep(delegateActivityPublishInterval + 200*time.Millisecond)
	delegateSub.sess.mu.Lock()
	activity := delegateSub.sess.cfg.spawn.parentJobActivity
	delegateSub.sess.mu.Unlock()
	if activity == nil {
		t.Fatal("the running delegate carries no parent activity binding")
	}
	activity(fixture.delegateID, jobPhaseToolRunning)

	// The ongoing report is the parent's activity evidence now: a parent
	// whose delegate works is not inactive, whatever its own transcript last
	// recorded.
	info, err := os.Stat(parentTranscript)
	if err != nil {
		t.Fatalf("stat the parent's transcript: %v", err)
	}
	if !info.ModTime().After(stale) {
		t.Fatal("the running delegate's ongoing activity did not refresh the lane owner's evidence")
	}

	// A foreign session's prune — armed with a threshold the backdate crosses
	// but the refresh does not — must not release or collect the lane.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "active-delegate-lane") != nil {
		t.Fatalf("an active delegate's lane was reclaimed mid-activity: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "active-delegate-lane") != nil {
		t.Fatalf("an active delegate's lane was released by the orphan arm: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the active delegate's lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != fixture.meta.ID {
		t.Fatalf("the active delegate's lane lock = %q, want the delegate marker naming its parent session", reason)
	}

	// The turn finishes cleanly once released.
	close(release)
	select {
	case outcome := <-sendOutcome:
		if outcome.result.Err != nil {
			t.Fatalf("the long turn failed after the prune: %v", outcome.result.Err)
		}
		if outcome.commit != nil {
			plans, err := outcome.commit.Complete(true)
			if err != nil {
				t.Fatalf("acknowledge the long turn: %v", err)
			}
			if err := root.executeDelegateMutationPlans(plans); err != nil {
				t.Fatalf("apply the long turn's acknowledgment: %v", err)
			}
		}
	// TRIPWIRE: the released wedge returns and the run settles within seconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the long turn never finished after release")
	}
}

// A retained child still owes its parent the report of the run that left it
// retained. Delivery appends that report as durable attention on the parent's
// own transcript, and the parent's attention turn presents it — not the
// child's notification drive turn, which with nothing queued renders nothing,
// makes no model request, and finishes. The owed attention arms a moment after
// the spawning send settles, so a drive that reads it once can miss and must
// try again; the loop below drives the parent's own attention primitive until
// its turn actually starts. Through all of it the tree is alive: the running
// turn's ongoing activity reports refresh the marker parent's evidence, or a
// foreign prune — whose live-work view cannot see the turn — judges the tree
// inactive past the operator's threshold and collects the clean, unchanged
// checkout mid-report.
func TestWorktreePrune_OrphanArmSparesLaneWhileRetainedChildNotifies(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var wedgeOnce sync.Once
	var wedgedSession atomic.Value
	wedge := func(req llm.Request) llm.Response {
		wedgedSession.Store(req.SessionID)
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "active-notify-lane", "dlg_waketest0000008", "034placeholder000000012", wedge)
	parentTranscript := schema.SessionTranscriptPath(lane.fixture.stateDir, lane.fixture.meta.ID)

	// The retained child's report reaches its parent as a durable attention
	// row, and the parent's attention turn — the turn that presents the
	// report — wedges inside its first model call. The tree is alive, the
	// parent runs a turn, and the lane's own marker parent stays idle. The
	// drive keeps retrying that parent's wake-edge because the owed attention
	// arms asynchronously: an attempt that reads it once can miss it, and
	// exiting on the first attempt would strand the wedge turn on the owner's
	// slow quiet-watch retry. The loop drives the same attention primitive the
	// owner's wake-edge uses, so the scripted step is the presentation turn
	// itself and never an empty notification drive. The loop and the tripwire
	// below share one deadline: a loop budget shorter than the tripwire
	// strands an arm that lands in the gap — nothing drives the turn again
	// until the owner's quiet-watch, past the tripwire.
	// The report can be classified while the root is processing and deferred
	// to the root's delivery queue, where only the background retry would
	// pump it. Flush the queue before each drive so a deferred report is
	// appended deterministically; an empty queue makes it a no-op.
	turnStartDeadline := time.Now().Add(30 * time.Second)
	var flushMu sync.Mutex
	var flushErr error
	flush := func() {
		lane.delegateSub.mu.Lock()
		busy := lane.delegateSub.running || lane.delegateSub.driving || lane.delegateSub.finalizing
		lane.delegateSub.mu.Unlock()
		if busy {
			return
		}
		if err := lane.delegateSess.flushPendingDelegateDeliveries(); err != nil {
			flushMu.Lock()
			flushErr = err
			flushMu.Unlock()
		}
	}
	go func() {
		for time.Now().Before(turnStartDeadline) {
			flush()
			lane.root.driveDelegateAttention(lane.delegateSub)
			time.Sleep(200 * time.Millisecond)
			select {
			case <-entered:
				return
			default:
			}
		}
	}()
	select {
	case <-entered:
	// TRIPWIRE: the armed attention turns over within seconds of the send
	// settling; the shared deadline only fires on a genuine hang. It covers a
	// shard loaded with the rest of this package's parallel tests, where spawn
	// and the arm both stretch.
	case <-time.After(time.Until(turnStartDeadline)):
		// Reproduce the drive's own preconditions, in its order, so the
		// failure names the one that declined: the drive's boolean cannot.
		// All of them green names the other failure mode — the wake-edge
		// keeps launching empty turns because the owed attention never
		// armed.
		delegate := lane.delegateSub
		delegate.mu.Lock()
		closed, running, driving, finalizing := delegate.closed, delegate.running, delegate.driving, delegate.finalizing
		disposeGated, attentionRefused := delegate.disposeGated, delegate.attentionDriveRefused
		delegate.mu.Unlock()
		_, locked, reason := r.laneLocked(t, lane.lanePath)
		mutRelease, mutErr := lane.delegateSess.beginRetirementMutation("delegate_drive")
		if mutErr == nil {
			mutRelease()
		}
		revalErr := lane.delegateSess.revalidateLaneForLaunch(lane.sharedDelegateID)
		driveSlot, driveSlotOK := lane.delegateSess.reserveDriveSlot()
		if driveSlotOK {
			driveSlot.release()
		}
		lane.delegateSess.mu.Lock()
		driverClosing := lane.delegateSess.closingOrClosedLocked()
		lane.delegateSess.mu.Unlock()
		// The attention drive's own gate and its pending rows: the
		// notification-drive preconditions above say nothing about them, and
		// all of them green leaves exactly two failure modes — the owed
		// attention never armed, or a gate held the launch. Name which.
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		childPending, childPendErr := lane.sharedSub.sess.pendingDelegateAttentionIDs()
		flushMu.Lock()
		lastFlushErr := flushErr
		flushMu.Unlock()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		lane.sharedSub.mu.Lock()
		childRunning, childClosed, childFinalizing := lane.sharedSub.running, lane.sharedSub.closed, lane.sharedSub.finalizing
		lane.sharedSub.mu.Unlock()
		stopGated := lane.root.childStopGated(lane.delegateSess.id)
		fatalGated := lane.root.childFatalRunGated(lane.delegateSess.id)
		drainAbandoned := lane.root.childDrainAbandoned(lane.delegateSess.id)
		drainGrace := lane.root.childDrainGracePending(lane.delegateSess.id)
		committedStart := lane.root.childCommittedSendStart(lane.delegateSess.id)
		// The in-memory queue above is not the durable head: the report can wait
		// in the journal while the session queue is empty, and the reverse.
		// durable is keyed by delegate id, so read the parent delegate's own
		// aggregate (lane.fixture.delegateID) and the retained child's
		// (lane.sharedDelegateID) rather than either child session id.
		controller := lane.root.delegateController
		controller.mu.Lock()
		delegatePhase, delegateGen, delegateOpen := "", uint64(0), false
		var delegateDurable []string
		if agg := controller.durable[lane.fixture.delegateID]; agg != nil {
			delegatePhase, delegateGen, delegateOpen = string(agg.Phase), agg.Generation, agg.CurrentRunOpen
			for _, pending := range agg.PendingDeliveries {
				delegateDurable = append(delegateDurable, pending.DeliveryID)
			}
		}
		childPhase, childGen, childOpen, childPrepared := "", uint64(0), false, false
		var childDurable []string
		if agg := controller.durable[lane.sharedDelegateID]; agg != nil {
			childPhase, childGen, childOpen, childPrepared = string(agg.Phase), agg.Generation, agg.CurrentRunOpen, agg.PreparedTerminal != nil
			for _, pending := range agg.PendingDeliveries {
				childDurable = append(childDurable, pending.DeliveryID)
			}
		}
		reportDeliveryID := delegateDeliveryID(lane.sharedDelegateID, 1)
		claimPresent, claimWaiter, claimHeld, claimStartRefused := false, false, false, false
		if claim := controller.deliveryClaims[reportDeliveryID]; claim != nil {
			claimPresent, claimWaiter, claimHeld, claimStartRefused = true, claim.waiter != nil, claim.held, claim.startRefused
		}
		receiptPresent, receiptRetryable, receiptInline := false, false, false
		if receipt := controller.deliveryReceiptLocked(reportDeliveryID); receipt != nil {
			receiptPresent, receiptRetryable, receiptInline = true, receipt.retryable, receipt.inline
		}
		ownerLive := controller.live[lane.fixture.delegateID]
		ownerLivePresent := ownerLive != nil
		ownerRuntimeSet := ownerLive != nil && ownerLive.runtime != nil
		ownerRuntimeIsDelegate := ownerLive != nil && ownerLive.runtime == lane.delegateSess
		ownerBinding := ownerLive != nil && ownerLive.binding != nil
		receiverType := fmt.Sprintf("%T", controller.deliveryReceiverLocked(lane.fixture.delegateID))
		controller.mu.Unlock()
		classifications := lane.classifications.snapshot()
		sharedSessionID := lane.sharedSub.sess.id
		tracked := lane.delegateSess.subagents.get(sharedSessionID) != nil
		lane.sharedSub.mu.Lock()
		childStatus, childTurns, childEndEmitted := lane.sharedSub.status, lane.sharedSub.turnsUsed, lane.sharedSub.endEmitted
		lane.sharedSub.mu.Unlock()
		t.Fatalf("the retained child's report was never presented (lane locked=%v reason=%q, delegate sub closed=%v running=%v driving=%v finalizing=%v disposeGated=%v attentionDriveRefused=%v; retirement mutation=%v, lane validation=%v, drive slot=%v, driver closing=%v; pending attention=%d (%v), child pending=%d (%v), child run=%v/%v/%v, deferred deliveries=%v flush=%v, gates: stop=%v fatal=%v drainAbandoned=%v drainGrace=%v committedSendStart=%v; durable: delegate phase=%s gen=%d open=%v pending=%v; child phase=%s gen=%d open=%v prepared=%v pending=%v; report %s claim=%v(waiter=%v held=%v startRefused=%v) receipt=%v(retryable=%v inline=%v); owner live=%v runtimeSet=%v runtimeIsDelegate=%v binding=%v receiver=%s; classified=%v; child sub session=%s status=%v turns=%d endEmitted=%v tracked=%v)",
			locked, reason, closed, running, driving, finalizing, disposeGated, attentionRefused, mutErr, revalErr, driveSlotOK, driverClosing,
			len(pending), pendErr, len(childPending), childPendErr, childRunning, childClosed, childFinalizing, deferred, lastFlushErr, stopGated, fatalGated, drainAbandoned, drainGrace, committedStart,
			delegatePhase, delegateGen, delegateOpen, delegateDurable,
			childPhase, childGen, childOpen, childPrepared, childDurable,
			reportDeliveryID, claimPresent, claimWaiter, claimHeld, claimStartRefused, receiptPresent, receiptRetryable, receiptInline,
			ownerLivePresent, ownerRuntimeSet, ownerRuntimeIsDelegate, ownerBinding, receiverType,
			classifications, sharedSessionID, childStatus, childTurns, childEndEmitted, tracked)
	}
	lane.delegateSub.mu.Lock()
	delegateRunning := lane.delegateSub.running
	lane.delegateSub.mu.Unlock()
	if !delegateRunning {
		t.Fatal("the retention turn started somewhere other than the lane-owning delegate's run")
	}
	if got, _ := wedgedSession.Load().(string); got != lane.delegateSess.id {
		t.Fatalf("the presentation turn ran on session %q, want the lane-owning delegate %q", got, lane.delegateSess.id)
	}

	// The injected report below must be publication-eligible. The wedged turn
	// reported awaiting-model activity just before the model call, and the
	// report a live lease carries inside delegateActivityPublishInterval of a
	// publication is coalesced away — its evidence refresh rides the next one
	// instead. Let the floor clear before the backdate (never between the
	// backdate and the assertion, where a turn-ticker refresh would satisfy the
	// stat without the injected report doing anything), the same way the
	// running-delegate test does.
	time.Sleep(delegateActivityPublishInterval + 200*time.Millisecond)

	// The parent has now been idle past the operator's threshold: the wake
	// refreshed its evidence once at launch, and nothing since.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The running turn reports activity through the parent-activity closure
	// the child carries. Whichever lease that closure is bound to, the report
	// is the turn's own activity.
	lane.sharedSub.sess.mu.Lock()
	activity := lane.sharedSub.sess.cfg.spawn.parentJobActivity
	lane.sharedSub.sess.mu.Unlock()
	if activity == nil {
		t.Fatal("the retained child carries no parent activity binding")
	}
	activity(lane.sharedDelegateID, jobPhaseToolRunning)

	// The report is the parent's activity evidence now.
	info, err := os.Stat(parentTranscript)
	if err != nil {
		t.Fatalf("stat the parent's transcript: %v", err)
	}
	if !info.ModTime().After(stale) {
		t.Fatal("the retained child's ongoing notification activity did not refresh the lane owner's evidence")
	}

	// A foreign session's prune — armed with a threshold the backdate crosses
	// but the refresh does not — must not release or collect the lane.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "active-notify-lane") != nil {
		t.Fatalf("an active notification turn's lane was reclaimed mid-activity: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "active-notify-lane") != nil {
		t.Fatalf("an active notification turn's lane was released by the orphan arm: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lane.lanePath)
	if !locked {
		t.Fatalf("the active notification turn's lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != lane.fixture.delegateID || m.SessionID != lane.fixture.meta.ID {
		t.Fatalf("the active notification turn's lane lock = %q, want the lane-owning delegate's marker naming its parent session", reason)
	}

	// The wedged turn settles cleanly once released: the run flag clears at
	// the settle, so waiting on it proves the turn finished, not just that a
	// drive launched it.
	close(release)
	// TRIPWIRE: the released turn settles within seconds once the model call returns; 30s only fires on a genuine hang. The deadline absorbs a loaded shard's scheduling delay between the release and the settle.
	waitForCondition(t, 30*time.Second, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
}

// A silent live tool call is active delegate work the per-report refreshes
// cannot see: a foreground shell reports activity once before it starts and
// then stays silent, the quiet watchdog is minutes away, and a foreign
// prune's live-work view cannot see the shell — so the turn itself must keep
// the marker parent's evidence fresh, or the prune unlocks and removes the
// clean checkout before the command finishes. No activity report is injected
// here: the machinery under test is the turn's own.
func TestWorktreePrune_OrphanArmSparesLaneThroughSilentToolCall(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	gate := t.TempDir()
	entered := gate + "/shell-entered"
	release := gate + "/shell-release"
	// A foreground shell call that signals its start and then waits silently.
	// The signal lands outside the lane, so the checkout stays clean.
	blockCmd := "sh -c 'touch " + entered + "; while [ ! -e " + release + " ]; do sleep 0.2; done'"
	args, err := json.Marshal(map[string]any{"command": blockCmd})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	silentCall := func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_silent_shell", Name: "shell", Arguments: args, Type: "function"})
	}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "silent-tool-lane", "dlg_orphanactive000002", "034orphanactive0002", silentCall)
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)

	sendOutcome := make(chan delegateSendOutcome, 1)
	go func() {
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "silent work", 60_000)
		sendOutcome <- outcome
	}()
	// The shell is live: it signaled its start and now waits silently.
	// TRIPWIRE: the real shell touches its start file in milliseconds; 30s only fires on a genuine hang under a loaded suite.
	waitForCondition(t, 30*time.Second, "the silent shell call to start", func() bool {
		_, err := os.Stat(entered)
		return err == nil
	})

	// The parent has now been idle past the operator's threshold — the turn's
	// last phase note fired before the shell started, and nothing else reports
	// while it waits.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The turn's own machinery must keep the evidence fresh through the
	// silent wait.
	// TRIPWIRE: the turn ticker refreshes evidence every second, so the first refresh lands within about one; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the silent tool call to keep the lane owner's evidence fresh", func() bool {
		info, err := os.Stat(parentTranscript)
		return err == nil && info.ModTime().After(stale)
	})

	// A foreign session's prune — armed with a threshold the backdate crosses
	// but the refresh does not — must not release or collect the lane while
	// the command is still running.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "silent-tool-lane") != nil {
		t.Fatalf("a silent live tool call's lane was reclaimed mid-activity: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "silent-tool-lane") != nil {
		t.Fatalf("a silent live tool call's lane was released by the orphan arm: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the silent tool call's lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != fixture.meta.ID {
		t.Fatalf("the silent tool call's lane lock = %q, want the delegate marker naming its parent session", reason)
	}

	// The blocked command finishes and the turn settles cleanly.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("release the blocked command: %v", err)
	}
	select {
	case outcome := <-sendOutcome:
		if outcome.result.Err != nil {
			t.Fatalf("the silent turn failed after the prune: %v", outcome.result.Err)
		}
		if outcome.commit != nil {
			plans, err := outcome.commit.Complete(true)
			if err != nil {
				t.Fatalf("acknowledge the silent turn: %v", err)
			}
			if err := root.executeDelegateMutationPlans(plans); err != nil {
				t.Fatalf("apply the silent turn's acknowledgment: %v", err)
			}
		}
	// TRIPWIRE: the released silent turn settles within seconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the silent turn never finished after release")
	}
}

// requestMentions reports whether a scripted model request carries the
// needle in any of its text parts — the way a step tells two sessions'
// racing requests apart when they share one adapter.
func requestMentions(req llm.Request, needle string) bool {
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentText && strings.Contains(part.Text, needle) {
				return true
			}
		}
	}
	return false
}

// An isolated coordinator that finishes its turn while its isolated
// descendant still runs is active work the turn's evidence ticker cannot
// see: the finalization drain holds the generation open for the descendant
// after the coordinator's turn — and its ticker — have ended. The
// descendant refreshes the coordinator's own transcript, but nothing
// refreshes the root transcript protecting the coordinator's lane, so a
// foreign prune could remove its clean checkout while its generation still
// awaits the descendant, breaking its continuation.
func TestWorktreePrune_OrphanArmSparesLaneWhileCoordinatorDrainsForDescendant(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	gate := t.TempDir()
	enteredB := gate + "/descendant-entered"
	releaseB := gate + "/descendant-release"
	const sentinel = "descendant sentinel task"
	const coordinatorMarker = "isolate a descendant and wait"
	blockCmd := "sh -c 'touch " + enteredB + "; while [ ! -e " + releaseB + " ]; do sleep 0.2; done'"
	shellArgs, err := json.Marshal(map[string]any{"command": blockCmd})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	// The coordinator's second turn spawns an isolated descendant and then
	// finishes, leaving its generation awaiting the descendant in the
	// finalization drain.
	spawnB := func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]string{"prompt": sentinel, "isolation": "worktree"})
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_spawn_isolated_descendant", Name: "delegate", Arguments: args, Type: "function"})
	}
	// The descendant's fresh history cannot contain the coordinator's own
	// input, and the coordinator's follow-up always does, so one scripted
	// step serves whichever of the two racing requests reaches the adapter
	// first: the descendant gets a blocking foreground shell (it blocks in
	// its tool call, holding no adapter lock), the coordinator its final
	// response.
	router := func(req llm.Request) llm.Response {
		if requestMentions(req, coordinatorMarker) {
			return agenttest.FinalResponse("coordinator done")
		}
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_descendant_shell", Name: "shell", Arguments: shellArgs, Type: "function"})
	}
	drainTurns := []func(llm.Request) llm.Response{spawnB, router, router}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "drain-wait-lane", "dlg_orphanactive000003", "034orphanactive0003", drainTurns...)
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)

	sendOutcome := make(chan delegateSendOutcome, 1)
	go func() {
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, coordinatorMarker, 60_000)
		sendOutcome <- outcome
	}()
	// The descendant's shell is live and the coordinator's turn has finished:
	// its generation now sits in the finalization drain, awaiting the
	// descendant.
	// TRIPWIRE: the scripted turns and the drain complete within seconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the coordinator to finish while the descendant works", func() bool {
		if _, err := os.Stat(enteredB); err != nil {
			return false
		}
		coordinatorRequests := 0
		for _, req := range fixture.adapter.Requests() {
			if requestMentions(req, coordinatorMarker) {
				coordinatorRequests++
			}
		}
		return coordinatorRequests >= 2
	})
	// Let the coordinator's turn ticker's last moment pass, so only the
	// drain can keep the evidence fresh from here.
	time.Sleep(delegateActivityPublishInterval + 500*time.Millisecond)

	// The lane's marker parent — the root — has now been idle past the
	// operator's threshold: nothing of its own runs while the tree waits on
	// the descendant.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The finalization drain is active delegate work: its wait must keep
	// the evidence fresh.
	// TRIPWIRE: the drain ticker refreshes evidence every second, so the first refresh lands within about one; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the drain wait to keep the lane owner's evidence fresh", func() bool {
		info, err := os.Stat(parentTranscript)
		return err == nil && info.ModTime().After(stale)
	})

	// A foreign session's prune — armed with a threshold the backdate crosses
	// but the refresh does not — must not release or collect the lane while
	// the generation still awaits the descendant.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "drain-wait-lane") != nil {
		t.Fatalf("a coordinator awaiting its descendant had its lane reclaimed: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "drain-wait-lane") != nil {
		t.Fatalf("a coordinator awaiting its descendant had its lane released: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the coordinator's lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != fixture.meta.ID {
		t.Fatalf("the coordinator's lane lock = %q, want the delegate marker naming its parent session", reason)
	}

	// The descendant finishes, the drain completes, and the coordinator
	// settles cleanly.
	if err := os.WriteFile(releaseB, nil, 0o644); err != nil {
		t.Fatalf("release the descendant: %v", err)
	}
	select {
	case outcome := <-sendOutcome:
		if outcome.result.Err != nil {
			t.Fatalf("the coordinator failed after the prune: %v", outcome.result.Err)
		}
		if outcome.commit != nil {
			plans, err := outcome.commit.Complete(true)
			if err != nil {
				t.Fatalf("acknowledge the coordinator: %v", err)
			}
			if err := root.executeDelegateMutationPlans(plans); err != nil {
				t.Fatalf("apply the coordinator's acknowledgment: %v", err)
			}
		}
	// TRIPWIRE: the drain completes within seconds of the descendant's release; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the coordinator never settled after the descendant finished")
	}
}

// An isolated descendant that wakes after its parent generation has settled
// leaves the parent with neither a turn ticker nor a finalization drain: the
// descendant's activity refreshes only its own lane's parent —
// the coordinator's transcript — while nothing refreshed the root transcript
// protecting the coordinator's lane, so a foreign prune removed the settled
// coordinator's clean lane mid-activity and broke its continuation when the
// descendant's report later woke it. A descendant's activity must refresh
// every isolated ancestor's evidence, its own lane's parent and each
// ancestor's alike.
func TestWorktreePrune_OrphanArmSparesLaneWhileWokenDescendantWorks(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	gate := t.TempDir()
	// The woken turn blocks inside a silent foreground shell: its own
	// activity keeps reporting while nothing of the settled parent does.
	enteredB := gate + "/descendant-entered"
	releaseB := gate + "/descendant-release"
	const sentinel = "timer-woken sentinel task"
	const coordinatorMarker = "spawn a settled descendant"
	// The owed reply's content is the wake request's own input, the one
	// text part only that request carries: the descendant's first answer
	// rides in a communicate tool call's arguments, where a content router
	// cannot see it.
	const owedReply = "the descendant's owed reply is ready"
	blockCmd := "sh -c 'touch " + enteredB + "; while [ ! -e " + releaseB + " ]; do sleep 0.2; done'"
	shellArgs, err := json.Marshal(map[string]any{"command": blockCmd})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	spawnB := func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]string{"prompt": sentinel, "isolation": "worktree"})
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_spawn_isolated_descendant", Name: "delegate", Arguments: args, Type: "function"})
	}
	// Every request the tree can make during the two phases routes by its
	// content, so the adapter cursor's exact position is never the test's
	// assumption: the coordinator's own requests carry its input, the
	// woken descendant's request alone carries the owed reply it wakes to
	// answer, and the one-shot shell fires exactly once.
	var spawned, wakeShelled atomic.Bool
	route := func(req llm.Request) llm.Response {
		switch {
		case requestMentions(req, coordinatorMarker) && spawned.CompareAndSwap(false, true):
			return spawnB(req)
		case requestMentions(req, coordinatorMarker):
			return agenttest.FinalResponse("coordinator done")
		case requestMentions(req, owedReply) && wakeShelled.CompareAndSwap(false, true):
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_wake_shell", Name: "shell", Arguments: shellArgs, Type: "function"})
		default:
			return agenttest.FinalResponse("descendant done")
		}
	}
	routes := []func(llm.Request) llm.Response{route, route, route, route, route, route, route, route}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "woken-descendant-lane", "dlg_orphanactive000004", "034orphanactive0004", routes...)
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)

	// The coordinator's second turn spawns the isolated descendant, whose
	// first turn starts the stable shell job and answers; the coordinator's
	// run settles with the job live, and the parent acknowledges its report
	// the way a live delegate_send does.
	sendOutcome := make(chan delegateSendOutcome, 1)
	go func() {
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, coordinatorMarker, 60_000)
		sendOutcome <- outcome
	}()
	// A diagnostic dump for the settle failures: which requests the tree
	// made and which markers each carried, and where the two generations
	// stand.
	dumpState := func() string {
		var b strings.Builder
		if sub := root.subagents.get(fixture.childID); sub == nil {
			b.WriteString("A: missing; ")
		} else {
			sub.mu.Lock()
			fmt.Fprintf(&b, "A running=%v driving=%v finalizing=%v closed=%v; ", sub.running, sub.driving, sub.finalizing, sub.closed)
			sub.mu.Unlock()
		}
		ctl := root.delegateController
		ctl.mu.Lock()
		for id, agg := range ctl.durable {
			fmt.Fprintf(&b, "row %s phase=%v gen=%d open=%v parent=%q isolation=%q; ", id, agg.Phase, agg.Generation, agg.CurrentRunOpen, agg.Descriptor.ParentDelegateID, agg.Descriptor.Isolation)
		}
		ctl.mu.Unlock()
		fixture.adapter.mu.Lock()
		fmt.Fprintf(&b, "adapter requests=%d cursor=%d: ", len(fixture.adapter.requests), fixture.adapter.i)
		for i, req := range fixture.adapter.requests {
			marks := ""
			if requestMentions(req, coordinatorMarker) {
				marks += "coordinator,"
			}
			if requestMentions(req, sentinel) {
				marks += "sentinel,"
			}
			if requestMentions(req, "descendant done") {
				marks += "answered,"
			}
			fmt.Fprintf(&b, "[%d:%s] ", i, marks)
		}
		fixture.adapter.mu.Unlock()
		return b.String()
	}
	select {
	case outcome := <-sendOutcome:
		if outcome.result.Err != nil {
			t.Fatalf("the coordinator's turn failed: %v; %s", outcome.result.Err, dumpState())
		}
		if outcome.commit != nil {
			plans, err := outcome.commit.Complete(true)
			if err != nil {
				t.Fatalf("acknowledge the coordinator: %v", err)
			}
			if err := root.executeDelegateMutationPlans(plans); err != nil {
				t.Fatalf("apply the coordinator's acknowledgment: %v", err)
			}
		}
	// TRIPWIRE: the whole scripted sequence settles in seconds, and 70s sits above the send's own 60s budget so the send's error surfaces first; only a genuine hang reaches this arm.
	case <-time.After(70 * time.Second):
		t.Fatalf("the coordinator never settled; %s", dumpState())
	}

	// Both generations stand settled: the coordinator neither runs, drives
	// nor finalizes — nothing of its own refreshes the root's evidence from
	// here on — and the descendant sits retained with its stable job live.
	delegateSub := root.subagents.get(fixture.childID)
	if delegateSub == nil || delegateSub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// Both generations settle cleanly now: the descendant's quick turn is
	// done and nothing of the coordinator's holds the drain open.
	settled := false
	for deadline := time.Now().Add(30 * time.Second); !settled && time.Now().Before(deadline); {
		delegateSub.mu.Lock()
		running, driving, finalizing := delegateSub.running, delegateSub.driving, delegateSub.finalizing
		delegateSub.mu.Unlock()
		settled = !running && !driving && !finalizing
		if !settled {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !settled {
		t.Fatalf("the coordinator never settled after its report; %s", dumpState())
	}
	// The descendant's tree row and runtime, found the way the tree's own
	// tests do.
	var descendantID, descendantChildID string
	descendants := 0
	tree := root.delegateController
	tree.mu.Lock()
	for id, agg := range tree.durable {
		if agg.Descriptor.ParentDelegateID == fixture.delegateID {
			descendants++
			descendantID, descendantChildID = id, agg.Descriptor.ChildSessionID
		}
	}
	tree.mu.Unlock()
	if descendants != 1 {
		t.Fatalf("the coordinator spawned %d descendants, want exactly 1", descendants)
	}
	descendantSub := delegateSub.sess.subagents.get(descendantChildID)
	if descendantSub == nil || descendantSub.sess == nil {
		t.Fatalf("the descendant %s is not retained after its run", descendantChildID)
	}
	// TRIPWIRE: the quick scripted turn settles within seconds; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the descendant to settle after its quick turn", func() bool {
		descendantSub.mu.Lock()
		running, finalizing := descendantSub.running, descendantSub.finalizing
		descendantSub.mu.Unlock()
		return !running && !finalizing
	})

	// The settled tree's parent evidence is long stale — the silence the
	// orphan reclaim measures — backdated here rather than waited out.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The descendant owes its parent a reply it never took — the state a
	// cold restart reconstructs for a retained delegate (the stable-shell
	// repair) and the tree arms the same way while both generations stand
	// settled. The owed turn pends durably on the descendant's own
	// transcript and the tree's wake bookkeeping carries it: the
	// already-armed wake a settled tree's descendant later takes.
	const wakeAttentionID = "delegate:woken-descendant-owed-reply"
	if _, err := descendantSub.sess.appendDelegateNotificationDurably(wakeAttentionID, owedReply); err != nil {
		t.Fatalf("pend the descendant's owed attention: %v", err)
	}
	if _, err := root.delegateController.openDelegateAttention(descendantID, wakeAttentionID); err != nil {
		t.Fatalf("arm the descendant's attention wake: %v", err)
	}

	// The wake: the tree's own attention drive, the consumer the root's
	// armed retry timer serves. A drive that cannot launch yet is
	// retryable — the timer re-fires — so mirror it within a bound. The
	// woken turn blocks inside a silent foreground shell, its activity the
	// tree's only life now.
	shellStarted := false
	for driveDeadline := time.Now().Add(30 * time.Second); !shellStarted && time.Now().Before(driveDeadline); {
		root.drivePendingDelegateAttention()
		for pollDeadline := time.Now().Add(2 * time.Second); !shellStarted && time.Now().Before(pollDeadline); {
			if _, err := os.Stat(enteredB); err == nil {
				shellStarted = true
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !shellStarted {
		descendantSub.mu.Lock()
		dRunning, dDriving, dFinalizing, dClosed := descendantSub.running, descendantSub.driving, descendantSub.finalizing, descendantSub.closed
		descendantSub.mu.Unlock()
		fixture.adapter.mu.Lock()
		requestCount, cursor := len(fixture.adapter.requests), fixture.adapter.i
		fixture.adapter.mu.Unlock()
		_, subLocked, subReason := r.laneLocked(t, lanePath)
		t.Fatalf("the woken descendant's shell never started (sub running=%v driving=%v finalizing=%v closed=%v, adapter requests=%d cursor=%d, coordinator lane locked=%v reason=%q)",
			dRunning, dDriving, dFinalizing, dClosed, requestCount, cursor, subLocked, subReason)
	}

	// The launch already recorded this wake in the ancestors' evidence;
	// the silence it must outlast is the rest of the turn, so the ongoing
	// refresh alone must keep every isolated ancestor fresh from here on.
	stale = time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript again: %v", err)
	}

	// The woken descendant's activity is the tree's life now: it must keep
	// the settled coordinator's lane evidence fresh.
	// TRIPWIRE: the woken turn's ticker refreshes the ancestors' evidence within a second; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the woken descendant to keep the coordinator's lane evidence fresh", func() bool {
		info, err := os.Stat(parentTranscript)
		return err == nil && info.ModTime().After(stale)
	})

	// A foreign session's prune — armed with a threshold the backdate crosses
	// but the refresh does not — must not release or collect the settled
	// coordinator's lane while its descendant works.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "woken-descendant-lane") != nil {
		t.Fatalf("a settled coordinator's lane was reclaimed while its descendant worked: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "woken-descendant-lane") != nil {
		t.Fatalf("a settled coordinator's lane was released while its descendant worked: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the coordinator's lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != fixture.meta.ID {
		t.Fatalf("the coordinator's lane lock = %q, want the delegate marker naming its parent session", reason)
	}

	// The woken turn finishes cleanly once released.
	if err := os.WriteFile(releaseB, nil, 0o644); err != nil {
		t.Fatalf("release the woken descendant: %v", err)
	}
	// TRIPWIRE: the released shell returns and the turn settles within seconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the woken turn to finish after release", func() bool {
		descendantSub.mu.Lock()
		running := descendantSub.running
		descendantSub.mu.Unlock()
		return !running
	})
}

// The released inherited lane is re-taken before the shared child's turn
// runs, and the wake counts as activity of the session the lane's marker
// names — the parent the orphan reclaim consults — not of the delegate
// driving the wake.
func TestDelegateWakeReLocksUnlockedInheritedLaneAndRecordsOwnerActivity(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	lane := spawnSharedDescendantLane(t, r, "wake-inherited-relock", "dlg_waketest0000004", "034placeholder000000008")
	// The parent's evidence is long-stale: the shared child's wake runs on the
	// delegate's machinery and touches no parent transcript of its own.
	parentTranscript := schema.SessionTranscriptPath(lane.fixture.stateDir, lane.fixture.meta.ID)
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}
	// The end state the orphan arm and the manual unlock leave.
	wtGit(t, r.mainRoot, "worktree", "unlock", lane.lanePath)

	// A not-launched drive is a retryable result in production — the caller
	// re-drives at the next loop boundary — and a loaded shard can decline a
	// launch for a reason that clears on its own. Mirror the caller within a
	// bound: the outcome assertions below cannot be satisfied without a real
	// launch, and a decline that never clears fails with the state that
	// caused it.
	launched := false
	for deadline := time.Now().Add(10 * time.Second); !launched && time.Now().Before(deadline); {
		lane.fixture.adapter.setSteps([]func(llm.Request) llm.Response{lane.finish})
		launched = lane.delegateSess.driveSubagentNotificationTurn(lane.sharedSub)
		if !launched {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !launched {
		shared := lane.sharedSub
		shared.mu.Lock()
		closed, running, driving := shared.closed, shared.running, shared.driving
		shared.mu.Unlock()
		_, locked, reason := r.laneLocked(t, lane.lanePath)
		// Reproduce the drive's own preconditions, in its order, so the
		// failure names the one that declined: the drive's boolean cannot.
		mutRelease, mutErr := lane.delegateSess.beginRetirementMutation("delegate_drive")
		if mutErr == nil {
			mutRelease()
		}
		revalErr := lane.delegateSess.revalidateLaneForLaunch(lane.sharedDelegateID)
		driveSlot, driveSlotOK := lane.delegateSess.reserveDriveSlot()
		if driveSlotOK {
			driveSlot.release()
		}
		lane.delegateSess.mu.Lock()
		driverClosing := lane.delegateSess.closingOrClosedLocked()
		lane.delegateSess.mu.Unlock()
		t.Fatalf("notification drive never launched the shared child (lane locked=%v reason=%q, sub closed=%v running=%v driving=%v; retirement mutation=%v, lane validation=%v, drive slot=%v, driver closing=%v)",
			locked, reason, closed, running, driving, mutErr, revalErr, driveSlotOK, driverClosing)
	}

	_, locked, reason := r.laneLocked(t, lane.lanePath)
	if !locked {
		t.Fatal("shared descendant's turn launched without the inherited lane re-locked")
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != lane.fixture.delegateID || m.SessionID != lane.fixture.meta.ID {
		t.Fatalf("re-locked lane's lock = %q, want the lane-owning delegate's marker naming its parent session", reason)
	}
	info, err := os.Stat(parentTranscript)
	if err != nil {
		t.Fatalf("stat the parent's transcript: %v", err)
	}
	if !info.ModTime().After(stale) {
		t.Fatal("the lane owner's activity evidence was not refreshed by the wake")
	}
}

// The inactivity verdict is a point-in-time claim, and the parent can resume
// between the eligibility check and the lock release — its revival adopts the
// marker in place, writing nothing the release's marker comparison could
// notice. The release therefore re-verifies the parent's inactivity while the
// claim holds the lock, and a parent that woke has its lock put back,
// untouched, with the lane reported skipped.
func TestWorktreePrune_OrphanArmRestoresWhenParentWakesDuringRelease(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const parentSID = "034foreignstale0000000000004"
	const dlg = "dlg_034foreignstale000004"
	lane := r.delegateLane(t, "orphan-wakes-mid-release", dlg, parentSID)
	seedOrphanParent(t, r, parentSID, dlg, "", time.Now().Add(-30*24*time.Hour))
	// The parent resumes inside the release window: the seam fires after the
	// eligibility plan, before the lock is claimed. A resume leaves the same
	// fresh-transcript signal here.
	var wakeErr error
	r.s.worktreeBeforeStrandedMarkerRelease = func(lanePath string) {
		if lanePath != lane {
			return
		}
		wakeErr = os.Chtimes(schema.SessionTranscriptPath(r.stateDir, parentSID), time.Now(), time.Now())
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if wakeErr != nil {
		t.Fatalf("wake the parent: %v", wakeErr)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-wakes-mid-release") != nil {
		t.Fatalf("a resumed parent's lane was collected: %+v", out)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-wakes-mid-release"); e != nil {
		t.Fatalf("a resumed parent's lane was reported unlocked: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lane); !locked {
		t.Errorf("the resumed parent's lock was not restored: %q", reason)
	}
}

// A child-only wake is still parent activity, and it must not cost the lane.
// A notification drive runs the child's turn on the parent's own machinery,
// and a daemon from before the activity refresh leaves the parent's
// inactivity evidence stale even while the tree is demonstrably alive — its
// wake just ran. The parent's held livelock, not a release-window interleave,
// is the protection: a live owner's lane is never planned for release, so no
// release exists for a wake to race, and the prune leaves the tree's own
// machinery unharmed.
func TestWorktreePrune_OrphanArmSparesLiveOwnerWhileWakeRuns(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	// One state root, the shipped shape: the arm resolves a foreign parent's
	// evidence through the pruning session's state dir, so the parent must
	// live in that same root for the prune to see it at all. The fixture
	// bootstraps the parent straight into the pruning session's root.
	fixture := newColdDelegateFixtureConfiguredInState(t, "", r.stateDir, func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "orphan-wake-interleaved", "dlg_waketest0000002", "034placeholder000000006")
		d.WorkingDir = lanePath
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, r.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatDelegateMarker(fixture.delegateID, fixture.meta.ID), lanePath)
	// The sidecar must name the same delegate the marker does, or the sweep's
	// provenance check refuses the lane before the release.
	if err := worktree.UpdateSidecar(r.metaDir(t, r.canonicalMain(t)), "orphan-wake-interleaved", func(sc *worktree.Sidecar) {
		sc.DelegateID = fixture.delegateID
	}); err != nil {
		t.Fatalf("align the sidecar's delegate: %v", err)
	}
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The tree's own wake runs on the parent's machinery: the drive launches
	// the child's notification turn, and its launch refresh and reports are
	// the parent's activity evidence. The turn must finish before the
	// evidence is backdated, so the backdate is the last write the evidence
	// carries — the shape a daemon from before the activity refresh leaves
	// behind.
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	driveDone := make(chan bool, 1)
	go func() { driveDone <- root.driveSubagentNotificationTurn(sub) }()
	select {
	case drove := <-driveDone:
		if !drove {
			t.Fatal("the tree's own notification wake did not drive its turn")
		}
	// TRIPWIRE: the scripted drive launches in-process in milliseconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the notification drive never reported its handoff")
	}
	// TRIPWIRE: the scripted drive turn settles within seconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the wake's drive turn to finish", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return !sub.driving
	})
	// The parent session now looks long-idle — the inactivity evidence the
	// arm reads is stale while the tree is demonstrably alive, its wake just
	// run. The parent's held livelock, not a release-window interleave, is
	// the protection: a live owner's lane is never planned for release, so
	// no release exists for a wake to race, and the prune leaves the tree's
	// own machinery unharmed. The release seam is the tripwire: no release
	// may even be attempted for a live owner's lane.
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}
	releaseAttempted := false
	r.s.worktreeBeforeStrandedMarkerRelease = func(p string) {
		if p == lanePath {
			releaseAttempted = true
		}
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if releaseAttempted {
		t.Fatal("a live owner's lane was planned for release on stale-evidence inactivity")
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-wake-interleaved") != nil {
		t.Fatalf("a live owner's running lane was collected: %+v", out)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-wake-interleaved"); e != nil {
		t.Fatalf("a live owner's running lane was reported unlocked: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lanePath); !locked {
		t.Errorf("the live owner's lock was not kept: %q", reason)
	}
}

// The evidence tickers fire every second, so the transcript rewrite is the
// hot path for a busy tree — and the mtime is the change stamp every reader
// downstream keys on: a same-size mtime move makes the foldcache refold a
// session's whole transcript and the hub's message search reindex. A
// transcript already touched within the refresh floor is as fresh as every
// consumer's threshold needs, so the rewrite must leave it alone, while a
// stale transcript is still rewritten immediately.
func TestRefreshActivityEvidenceLeavesFreshTranscriptAlone(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	finish := func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{finish})
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil || first.commit == nil {
		t.Fatalf("first send: %+v", first.result)
	}
	plans, err := first.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge the first turn: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("apply the first turn's acknowledgment: %v", err)
	}

	transcript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(transcript, stale, stale); err != nil {
		t.Fatalf("backdate the transcript: %v", err)
	}
	root.refreshActivityEvidence(fixture.meta.ID)
	info, err := os.Stat(transcript)
	if err != nil {
		t.Fatalf("stat the transcript: %v", err)
	}
	if !info.ModTime().After(stale) {
		t.Fatal("a transcript idle past the floor was not refreshed")
	}

	// Inside the floor the mtime is the readers' change stamp: the refresh
	// must not move it.
	recent := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(transcript, recent, recent); err != nil {
		t.Fatalf("freshen the transcript: %v", err)
	}
	root.refreshActivityEvidence(fixture.meta.ID)
	info, err = os.Stat(transcript)
	if err != nil {
		t.Fatalf("stat the transcript: %v", err)
	}
	if !info.ModTime().Equal(recent) {
		t.Fatal("a transcript inside the refresh floor had its mtime rewritten")
	}
}

// A settled delegate that holds a running background job produces no turn,
// drain, or phase report for as long as the job waits silently — every
// existing evidence carrier is gone — so the lane's marker parent would go
// stale enough for a foreign prune to release the lane under the running
// job. The job itself must hold the evidence: the hold starts with the first
// running managed job and is released when the last one leaves the running
// set.
func TestWorktreePrune_OrphanArmSparesLaneWhileSettledJobRuns(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "settled-job-lane", "dlg_orphanactive000004", "034orphanactive000004")
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained", fixture.childID)
	}
	child := sub.sess
	// The controller refuses shell work on a settled delegate's stale
	// lease, so a live job on a settled child is always one a run started
	// and outlived — the shape the job manager's start hook and the last
	// completion's finish hook manage, both scoped to the job's life, not
	// the run's. Engage the hold the way the start hook does.
	child.managedJobStarted()

	// The marker parent has now been idle past the operator's threshold —
	// the only live work is the settled child's running job.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The job's own hold must keep the evidence fresh through the silent
	// wait.
	// TRIPWIRE: the job-held ticker refreshes evidence every second, so the first refresh lands within about one; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the settled background job to keep the lane owner's evidence fresh", func() bool {
		info, err := os.Stat(parentTranscript)
		return err == nil && info.ModTime().After(stale)
	})

	// A foreign session's prune — armed with a threshold the backdate
	// crosses but the refresh does not — must not release or collect the
	// lane while the job is still running.
	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "settled-job-lane") != nil {
		t.Fatalf("a settled child's running job had its lane reclaimed mid-activity: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "unlocked"), "settled-job-lane") != nil {
		t.Fatalf("a settled child's running job had its lane released by the orphan arm: %+v", out)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the settled child's job lane lost its lock: %q", reason)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != fixture.delegateID || m.SessionID != fixture.meta.ID {
		t.Fatalf("the settled child's job lane lock = %q, want the delegate marker naming its parent session", reason)
	}

	// The last job ends and the hold is released with it — no ticker
	// outlives the last running job.
	child.managedJobFinished()
	child.laneEvidenceMu.Lock()
	jobs, stop := child.laneEvidenceJobs, child.laneEvidenceStop
	child.laneEvidenceMu.Unlock()
	if jobs != 0 || stop != nil {
		t.Fatalf("the finished job left its evidence hold behind: jobs=%d stop=%v", jobs, stop != nil)
	}
}

// A notification drive whose lane revalidation refuses — the lane removed,
// foreign-held, or unverifiable — can never deliver the notification it was
// kicked for, and the child's settled run gives the drain's grace machinery
// nothing to arm on. Without a stamp the drain keeps counting the queued
// notification as deliverable work and blocks until its context is
// cancelled; the refusal must mark the child given-up so the drain's
// liveness walks skip it, and a later revive that revalidates the lane
// successfully clears the stamp.
func TestDrainSkipsChildWhoseLaneRefusedDriveValidation(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	gate := t.TempDir()
	entered := gate + "/job-entered"
	release := gate + "/job-release"
	blockCmd := "sh -c 'touch " + entered + "; while [ ! -e " + release + " ]; do sleep 0.2; done'"
	args, err := json.Marshal(map[string]any{"command": blockCmd, "mode": "background"})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	bgCall := func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_bg_shell", Name: "shell", Arguments: args, Type: "function"})
	}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "refused-drive-lane", "dlg_orphanactive000005", "034orphanactive0005", bgCall)

	// The child's turn spawns the background job and finishes; cancelling the
	// generation's send ctx settles it the way every bounded drain ctx does
	// in a real tree, leaving the job running with no generation, no drain,
	// and a settled child.
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()
	sendOutcome := make(chan delegateSendOutcome, 1)
	go func() {
		sendOutcome <- (delegateRuntime{owner: root}).send(sendCtx, fixture.delegateID, "background work", 60_000)
	}()
	// TRIPWIRE: the scripted turn and the job's start complete within seconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the background job to start and its turn to finish", func() bool {
		if _, err := os.Stat(entered); err != nil {
			return false
		}
		return len(fixture.adapter.Requests()) >= 3
	})
	sendCancel()
	select {
	case <-sendOutcome:
		// The cancel cuts the generation's drain mid-wait; either outcome
		// leaves the state this test needs — settled child, live job, no
		// drain holding the completion notification's delivery.
	// TRIPWIRE: the cancelled generation settles within seconds; 10s only fires on a genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled send never settled")
	}

	// A foreign session takes the lane before the job completes, so the
	// completion's wake cannot re-take it: every revalidation the wake and
	// the drain would run now refuses.
	const foreignSID = "034somebodyelse00000000009"
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath)

	// The job finishes and arms its completion attention on the settled
	// child. The attention can never be delivered while the lane is
	// foreign-held, and the child's settled run gives the drain's grace
	// machinery nothing to arm on — without a stamp the drain keeps counting
	// the owed attention as deliverable work.
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatalf("release the background job: %v", err)
	}
	// TRIPWIRE: the job exits and its completion attention arms within seconds of release; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "the finished job to arm its completion attention", func() bool {
		sub := root.subagents.get(fixture.childID)
		if sub == nil || sub.sess == nil {
			return false
		}
		ids, err := sub.sess.pendingDelegateAttentionIDs()
		return err == nil && len(ids) > 0
	})

	// The parent's drain must give up on the child it can no longer deliver
	// to instead of holding every future drain open.
	// TRIPWIRE: the stamped child is skipped within seconds, far inside the drain's own grace windows; 10s only fires on a genuine hang.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	if _, err := root.DrainJobTree(drainCtx); err != nil {
		t.Fatalf("the drain blocked on a child whose lane refused revalidation: %v", err)
	}

	// The refused child is stamped given-up — the gate every drain walk and
	// drive loop reads — and the foreign lock was never displaced.
	if !root.childDriveGated(fixture.childID) {
		t.Fatal("a refused lane validation left the child counted as deliverable drain work")
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the drain displaced the foreign lock: %q", reason)
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("foreign lock = %q, want the other session's marker", reason)
	}
}

// The evidence a live tree refreshes is only as fresh as the refresh floor:
// a positive orphaned_delegate_days shorter than that window could judge a
// running delegate's parent inactive on evidence the tree is still within
// its rights to leave untouched, and release the lane under the running
// work. The tool must refuse such a threshold before touching any lane.
func TestWorktreePruneRefusesSubMinuteOrphanThreshold(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	if _, err := r.pruneOrphanedOp(t, 0.0001); err == nil {
		t.Fatal("a sub-minute orphan threshold was accepted")
	}
	// A supported threshold still runs the prune.
	if _, err := r.pruneOrphanedOp(t, 7); err != nil {
		t.Fatalf("a supported orphan threshold was refused: %v", err)
	}
}

// An ordinary within-bound foreground shell completes inline and discards
// its delayed job — the ephemeral branch of the complete-or-handle settle —
// and that discard must release the evidence hold the job's start took, or
// every small foreground command leaks a ticking hold for the rest of the
// session, holding a retained child's lane falsely active.
func TestDiscardedShellResultReleasesEvidenceHold(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	args, err := json.Marshal(map[string]any{"command": "true"})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	fgCall := func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_fg_true", Name: "shell", Arguments: args, Type: "function"})
	}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "inline-discard-lane", "dlg_orphanactive000006", "034orphanactive0006", fgCall)
	second := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "run a small command", 10_000)
	if second.result.Err != nil || second.commit == nil {
		t.Fatalf("the small-command send: %+v", second.result)
	}
	plans, err := second.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge the small-command turn: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("apply the small-command turn's acknowledgment: %v", err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained", fixture.childID)
	}
	child := sub.sess
	child.laneEvidenceMu.Lock()
	jobs, stop := child.laneEvidenceJobs, child.laneEvidenceStop
	child.laneEvidenceMu.Unlock()
	if jobs != 0 || stop != nil {
		t.Fatalf("an inline-discarded shell left its evidence hold behind: jobs=%d stop=%v", jobs, stop != nil)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked {
		t.Fatalf("the retained child's lane lost its lock: %q", reason)
	}
}

// The accepted orphan threshold must sit above the whole refresh cadence —
// the floor plus the tick interval and margin — or a prune armed at the
// a live tree's evidence as long-idle.
func TestOrphanThresholdMinimumCoversRefreshCadence(t *testing.T) {
	t.Parallel()
	// The minimum is not just the floor: a tick observing a transcript aged
	// just inside the floor skips it, and the refresh lands on the next
	// tick, so the accepted minimum must clear the floor by more than a
	// full tick.
	floorPlusTick := (activityEvidenceRefreshFloor + delegateActivityPublishInterval).Hours() / 24
	if minOrphanedDelegateDays <= floorPlusTick {
		t.Fatalf("the accepted minimum %.6g days does not exceed the refresh floor plus one tick %.6g days", minOrphanedDelegateDays, floorPlusTick)
	}

	// The cadence delivers its side of the bound: a live tree's evidence,
	// aged past the floor, is refreshed within the next ticks — never older
	// than the cadence allows.
	r := newWorktreeRepo(t)
	fixture, root, _ := spawnRetainedDelegateLane(t, r, "cadence-lane", "dlg_orphanactive000007", "034orphanactive0007")
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained", fixture.childID)
	}
	child := sub.sess
	child.managedJobStarted()
	defer child.managedJobFinished()
	transcript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	aged := time.Now().Add(-(activityEvidenceRefreshFloor + 2*delegateActivityPublishInterval))
	if err := os.Chtimes(transcript, aged, aged); err != nil {
		t.Fatalf("age the parent's transcript past the floor: %v", err)
	}
	// TRIPWIRE: the hold's ticker fires every second, so the first refresh past the floor lands within a couple of ticks; 10s only fires on a genuine hang.
	waitForCondition(t, 10*time.Second, "evidence aged past the floor to be refreshed by the next tick", func() bool {
		info, err := os.Stat(transcript)
		return err == nil && info.ModTime().After(aged)
	})
}

// A shell whose start event persists locally but fails to forward is
// removed by commitDelayedShell itself — the one running-set exit the
// discard path does not cover — and that removal must release the evidence
// hold the job's start took.
func TestFailedShellStartForwardReleasesEvidenceHold(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	fixture, root, _ := spawnRetainedDelegateLane(t, r, "failed-forward-lane", "dlg_orphanactive000008", "034orphanactive0008")
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained", fixture.childID)
	}
	child := sub.sess
	jm := child.jobManager
	// Take the job manager out of the delegate shell-work accounting (its
	// settled lease cannot admit new shell work) and rig the start event's
	// forward to fail while its local durable write still succeeds.
	jm.mu.Lock()
	jm.delegateController = nil
	jm.forward = func(jobstore.Event) error { return errors.New("forward unavailable") }
	jm.mu.Unlock()
	run, err := jm.newDelayedShell(shellArgs{Command: "true", Background: true})
	if err != nil {
		t.Fatalf("start the delayed shell: %v", err)
	}
	if err := jm.commitDelayedShell(run); !errors.Is(err, errDelayedShellStartForwardFailed) {
		t.Fatalf("the rigged forward did not fail the commit: %v", err)
	}
	child.laneEvidenceMu.Lock()
	jobs, stop := child.laneEvidenceJobs, child.laneEvidenceStop
	child.laneEvidenceMu.Unlock()
	if jobs != 0 || stop != nil {
		t.Fatalf("a failed start forward left its evidence hold behind: jobs=%d stop=%v", jobs, stop != nil)
	}
}
