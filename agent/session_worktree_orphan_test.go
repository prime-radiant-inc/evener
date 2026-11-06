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
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
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
		t.Fatalf("the retained child's report was never presented (lane locked=%v reason=%q, delegate sub closed=%v running=%v driving=%v finalizing=%v disposeGated=%v attentionDriveRefused=%v; retirement mutation=%v, lane validation=%v, drive slot=%v, driver closing=%v; pending attention=%d (%v), child pending=%d (%v), child run=%v/%v/%v, deferred deliveries=%v flush=%v, gates: stop=%v fatal=%v drainAbandoned=%v drainGrace=%v committedSendStart=%v)",
			locked, reason, closed, running, driving, finalizing, disposeGated, attentionRefused, mutErr, revalErr, driveSlotOK, driverClosing,
			len(pending), pendErr, len(childPending), childPendErr, childRunning, childClosed, childFinalizing, deferred, lastFlushErr, stopGated, fatalGated, drainAbandoned, drainGrace, committedStart)
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

// A presentation start that consumes a retained child's report owes the
// delegate that presentation no matter what its own launch does: the consume
// marker acceptDelegateAttention writes is irreversible by design (it fences
// concurrent double presentation), so a start that fails after it landed must
// re-deliver the same content, or the report is silently lost — the fold
// reads the row as resolved forever and no retry can even see what it owes.
// driveUntil loops the root's attention drive over the lane's delegate with
// the seeding idiom the attention mechanism tests share: a delivery the
// spawn deferred past its last drain cut is pumped while the delegate is
// idle, the drive runs, and the loop exits when stop closes or its own
// budget passes. It returns the last flush error, for the failure dumps
// that name a stranded delivery.
func (l *sharedDescendantLane) driveUntil(stop <-chan struct{}) func() error {
	var flushMu sync.Mutex
	var flushErr error
	flush := func() {
		l.delegateSub.mu.Lock()
		busy := l.delegateSub.running || l.delegateSub.driving || l.delegateSub.finalizing
		l.delegateSub.mu.Unlock()
		if busy {
			return
		}
		if err := l.delegateSess.flushPendingDelegateDeliveries(); err != nil {
			flushMu.Lock()
			flushErr = err
			flushMu.Unlock()
		}
	}
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			flush()
			l.root.driveDelegateAttention(l.delegateSub)
			time.Sleep(200 * time.Millisecond)
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	return func() error {
		flushMu.Lock()
		defer flushMu.Unlock()
		return flushErr
	}
}

// The scripted failure is the lane's: the launch revalidates the isolation
// lane and refuses while another session's lock holds it, the same refusal a
// live tree can hit under a handoff or a foreign prune. Every failure between
// the accept and the presented turn strands identically, so the contract the
// test pins is the recovery, not the refusal's cause: after the failed start,
// the report still reaches the lane-owning delegate's attention turn.
func TestDelegateAttentionFailedStartReopensConsumedReport(t *testing.T) {
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
	lane := spawnSharedDescendantLane(t, r, "reopen-failed-start", "dlg_reopentest00001", "034reopenplaceholder01", wedge)
	// The wedge parks a model call until release: close it from cleanup so a
	// fatal anywhere below cannot leak the parked turn past test end.
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	// The restored runtime's notify edge can drive the presentation during the
	// spawn's tail, before this test takes control. Disarm it so the drive
	// under test is exactly the one this test scripts: nil keeps every later
	// notify a no-op and leaves the test loop below the only driver.
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored runtime carries the same wired edge, and its
	// pending notification turn answers the same scripted adapter step the
	// delegate's presentation is waiting on. A turn that launches on its own
	// would consume the wedge step and leave the recovery presenting to the
	// adapter's fallback, so that edge is disarmed for the same reason.
	lane.sharedSub.sess.SetNotifyFunc(nil)

	// Hold the lane for a foreign session before the first presentation can
	// launch: the launch's lane revalidation refuses on the foreign lock, the
	// same precondition TestDelegateWakeRefusesForeignHeldInheritedLane pins
	// for a notification drive.
	const foreignSID = "034foreignholder0001"
	adoptForeignLane(t, r, lane.lanePath, foreignSID)

	// Drive the presentation. The drive accepts the report — the consume
	// marker lands — and its launch then refuses on the foreign lane. The
	// drive loop's flush idiom pumps a delivery the spawn deferred past its
	// last drain cut, so a deferred report is appended deterministically.
	lane.driveUntil(entered)
	// The refusal is the scenario's proof of fire: the launch stamps the
	// drain-abandoned mark exactly when it refuses the lane.
	// TRIPWIRE: the refusal stamps within a couple of drive passes of the
	// adoption — the flush above makes the report presentable on the first
	// pass however its delivery was classified — so the bound only fires on a
	// genuine hang, absorbing a loaded shard's spawn tail and git retries.
	waitForCondition(t, 30*time.Second, "the presentation start to refuse the foreign-held lane", func() bool {
		return lane.root.childDrainAbandoned(lane.delegateSess.id)
	})

	// Give the lane back: an unlocked lane is re-locked under the tree's own
	// marker at the next launch, so nothing but the stranded report can keep
	// the presentation from running now.
	wtGit(t, r.mainRoot, "worktree", "unlock", lane.lanePath)

	// The recovery needs its own driver window: the first loop's budget was
	// set before the refusal wait, so a slow refusal under load leaves that
	// loop little or no time to re-drive after the unlock — the exact load
	// shape this test pins. This second driver runs to its own fresh budget,
	// so the refusal's duration cannot starve the recovery.
	recoveryFlush := lane.driveUntil(entered)

	// The recovery window has its own budget: a slow refusal under load must
	// not eat into it, which is the load-flake shape this test pins.
	recoveryDeadline := time.Now().Add(30 * time.Second)
	select {
	case <-entered:
	case <-time.After(time.Until(recoveryDeadline)):
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		_, locked, reason := r.laneLocked(t, lane.lanePath)
		lastFlushErr := recoveryFlush()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		t.Fatalf("the failed start lost the retained child's report (pending attention=%d (%v); lane locked=%v reason=%q; deferred deliveries=%v flush=%v)",
			len(pending), pendErr, locked, reason, deferred, lastFlushErr)
	}
	if got, _ := wedgedSession.Load().(string); got != lane.delegateSess.id {
		t.Fatalf("the reopened presentation ran on session %q, want the lane-owning delegate %q", got, lane.delegateSess.id)
	}

	// The wedged turn settles cleanly once released.
	releaseOnce.Do(func() { close(release) })
	// TRIPWIRE: the released turn settles within seconds once the model call
	// returns; 30s only fires on a genuine hang, not a loaded shard's
	// scheduling delay between the release and the settle.
	waitForCondition(t, 30*time.Second, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
}

// A deliberate stop that cancels an in-flight attention presentation owns the
// report's teardown, not the settle tail: the stop's discard machinery
// enumerated the attention ids it knew at plan time, so a reopen appending a
// fresh row lands past that enumeration: it strands on the stopped child when
// the stop has already completed — waking paced retries for a deliberately
// stopped delegate and presenting on restart — and when the stop's
// completion-discard catches it late, the row still transiently exists and
// the discard resolves an id the stop never knew. The presentation here parks
// in its first model request, the stop cancels it, and the released turn
// settles through the cancelRequested conversion (context.Canceled) — exactly
// the settle the reopen hook sees, which must leave the consumed report to
// the stop instead of appending a reopened row at all.
func TestDelegateAttentionStopMidPresentationDoesNotReopenReport(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var wedgeOnce sync.Once
	wedge := func(req llm.Request) llm.Response {
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "stop-mid-presentation", "dlg_stopmidpresnt01", "034stopmidpresnt01", wedge)
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored notify edge can launch its own pending
	// notification turn, which would consume the scripted wedge step this
	// test's presentation parks in; disarm it so the test loop below is the
	// only driver.
	lane.sharedSub.sess.SetNotifyFunc(nil)

	// Drive the presentation and let its first model request park; the loop's
	// flush idiom pumps a delivery the spawn deferred past its last drain cut.
	flushErr := lane.driveUntil(entered)
	select {
	case <-entered:
	// TRIPWIRE: the loop's drives launch the parked presentation within a
	// couple of 200ms passes; the bound only fires on a genuine hang, not a
	// loaded shard's scheduling delay.
	case <-time.After(30 * time.Second):
		lastFlushErr := flushErr()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		t.Fatalf("the presentation never reached its model request (pending attention=%d (%v); deferred deliveries=%v flush=%v)",
			len(pending), pendErr, deferred, lastFlushErr)
	}

	// The parent deliberately stops the delegate mid-presentation, the same
	// production stop a user or the tree's close drives.
	_, cancelPlan, _, err := lane.root.delegateController.StopSubtree(rootDelegateActor(lane.root.delegateRootSessionID), lane.fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	// The parked request finishes once released; the settle converts its
	// clean end to context.Canceled because the stop asked for the cancel.
	releaseOnce.Do(func() { close(release) })
	// TRIPWIRE: the cancelled presentation settles within seconds of the
	// wedge opening; the bound only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the stopped presentation turn to settle", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
	// The settle tail — the reopen decision included — runs before the run's
	// done channel closes; waiting it out keeps the pending read below from
	// racing the hook. A stopped delegate never resumes, so the channel read
	// under the lock is this run's own.
	lane.delegateSub.mu.Lock()
	runDone := lane.delegateSub.done
	lane.delegateSub.mu.Unlock()
	select {
	case <-runDone:
	// TRIPWIRE: the settle tail — the reopen decision included — finishes
	// right after the turn settles; the bound only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the stopped presentation run never completed its settle tail")
	}
	// The stop owned the finish: the owner's row gate proves the generation
	// settled as deliberately stopped, so the hook below ran for a stop.
	// TRIPWIRE: the stopped generation reaches the owner's row gate within
	// seconds of the settle; the bound only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the stopped generation to reach the owner's row gate", func() bool {
		return lane.root.childStopGated(lane.delegateSess.id)
	})

	// The guard's contract: the stop owns the report's teardown, so the fold
	// must never gain a reopened row for it — neither a pending one nor one
	// the stop's own late-discard machinery resolved afterwards. The reopened
	// id is the observable: without the guard the settle hook appends it under
	// the failed generation's key, and whether that row then strands or is
	// discarded depends only on which side of the stop's completion the append
	// lands — the id is in the fold either way.
	fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
	if foldErr != nil {
		t.Fatalf("read the delegate's attention fold after the stop: %v", foldErr)
	}
	for id := range fold.resolutions {
		if strings.Contains(id, "/reopened/") {
			t.Fatalf("a deliberately stopped delegate gained a reopened attention row (id=%q disposition=%q)",
				id, fold.resolutions[id])
		}
	}
	for _, id := range fold.pendingIDs() {
		if strings.Contains(id, "/reopened/") {
			t.Fatalf("a deliberately stopped delegate kept a reopened pending row (id=%q)", id)
		}
	}
}

// A busy CommitStart released the reservation, so nothing retries the start
// the consume marker already paid for: the busy arm must reopen the report
// exactly as a refused launch does, or a commit that loses its race strands
// the report with no reservation and no pending row left. The commit-fault
// seam wedges the first two commits with errDelegateTargetBusy
// deterministically — the same class CI's flake hit, where a start consumed
// the report and then its commit failed — and the second refusal chains: the
// retry's reopened row fails unjournaled at the same generation, so the
// re-delivery nests one reopen identity deeper and its own retry must still
// claim that generation against both ancestor consume markers.
func TestDelegateAttentionBusyCommitReopensConsumedReport(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var wedgeOnce sync.Once
	var wedgedSession atomic.Value
	wedge := func(req llm.Request) llm.Response {
		wedgedSession.Store(req.SessionID)
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "busy-commit-reopen", "dlg_busycommit00001", "034busycommit00001", wedge)
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored notify edge can launch its own pending
	// notification turn, which would consume the scripted wedge step this
	// test's recovery parks in; disarm it so the test loop below is the only
	// driver.
	lane.sharedSub.sess.SetNotifyFunc(nil)

	// Refuse the first two attention commits as busy: the second refusal
	// chains — the first's reopened row is consumed by the retry, which fails
	// unjournaled at the same generation, so the second reopen re-delivers
	// under the grandchild identity X/reopened/g/reopened/g and the retry of
	// THAT must still be able to claim the generation both ancestors pin.
	var faults atomic.Int32
	updateSessionTestConfig(lane.root, func(cfg *testConfig) {
		cfg.delegateAttentionCommitStartFault = func() error {
			if faults.Add(1) <= 2 {
				return errDelegateTargetBusy
			}
			return nil
		}
	})

	// The chained busy drives consume the report and reopen it twice; the
	// third drive presents the twice-re-delivered row; the loop's flush idiom
	// pumps a delivery the spawn deferred past its last drain cut.
	flushErr := lane.driveUntil(entered)
	select {
	case <-entered:
	// TRIPWIRE: the third drive presents the twice-reopened row within a
	// couple of loop passes; the bound only fires on a genuine hang, not a
	// loaded shard's scheduling delay.
	case <-time.After(30 * time.Second):
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		lastFlushErr := flushErr()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		t.Fatalf("the busy commit lost the retained child's report (pending attention=%d (%v); deferred deliveries=%v flush=%v)",
			len(pending), pendErr, deferred, lastFlushErr)
	}
	if got, _ := wedgedSession.Load().(string); got != lane.delegateSess.id {
		t.Fatalf("the reopened presentation ran on session %q, want the lane-owning delegate %q", got, lane.delegateSess.id)
	}

	// The wedged turn settles cleanly once released.
	releaseOnce.Do(func() { close(release) })
	// TRIPWIRE: the released turn settles within seconds once the model call
	// returns; the bound only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
}

// A busy CommitStart's reopen supersedes the report's resume claim, so the
// claim the failed start left on the recycled generation binds nothing: a
// second pending row must be able to take that generation, or the stale
// claim refuses every later accept and strands both rows — the failed accept
// leaks its reservation, and the drive pinned to that reservation retries the
// same refusal forever, so neither the second row nor the reopened report is
// ever presented. The second row pends before any drive, and the commit-fault
// seam refuses busy until TWO DISTINCT base rows claim one generation — the
// scenario's own completion — so whichever row the spawn's delivery lands
// first, the first committed generation is the takeover's, with the reopened
// row riding along.
func TestDelegateAttentionSupersededClaimDoesNotBlockSecondPendingRow(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	const secondRowID = "delegate:second-pending-row-owed-reply"
	const secondRowContent = "second-pending-row: which cursor holds the merge base?"
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var wedgeOnce sync.Once
	var wedgedSession atomic.Value
	var sawSecondRow atomic.Bool
	wedge := func(req llm.Request) llm.Response {
		wedgedSession.Store(req.SessionID)
		if requestMessagesContainText(req.Messages, secondRowContent) {
			sawSecondRow.Store(true)
		}
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "superseded-claim-second-row", "dlg_superseded0001", "034superseded0001", wedge)
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored notify edge can launch its own pending
	// notification turn, which would consume the scripted wedge step this
	// test's presentation parks in; disarm it so the test loop below is the
	// only driver.
	lane.sharedSub.sess.SetNotifyFunc(nil)

	// Refuse every attention commit as busy until two distinct base rows
	// claim one generation: the first drive's consume-and-reopen leaves the
	// superseded claim, and the takeover this test pins is the second base row
	// claiming that same recycled generation. Until the report row has landed
	// and been consumed the refusal keeps chaining reopens, so however late
	// the spawn's delivery classified, the first committed generation is
	// exactly the takeover's. The gate is installed before the second row
	// lands: the fixture's own settle tail re-arms the delegate's pending
	// report, and a paced-retry drive that fires into the setup window would
	// otherwise commit ungated and present the report on its own.
	updateSessionTestConfig(lane.root, func(cfg *testConfig) {
		cfg.delegateAttentionCommitStartFault = func() error {
			fold, err := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
			if err != nil {
				return errDelegateTargetBusy
			}
			bases := map[uint64]map[string]bool{}
			for id, gen := range fold.resumeGenerations {
				if gen == 0 {
					continue
				}
				base := stripDelegateAttentionReopenSegments(id)
				if bases[gen] == nil {
					bases[gen] = map[string]bool{}
				}
				bases[gen][base] = true
			}
			for _, claimed := range bases {
				if len(claimed) >= 2 {
					return nil
				}
			}
			return errDelegateTargetBusy
		}
	})

	// The second row lands only once the gate is live: whichever row the
	// first drive consumes — the spawn's report, or this row when the
	// report's delivery has not classified yet — the other is the takeover.
	if _, err := lane.delegateSess.appendDelegateNotificationDurably(secondRowID, secondRowContent); err != nil {
		t.Fatalf("pend the second row: %v", err)
	}

	flushErr := lane.driveUntil(entered)
	select {
	case <-entered:
	// TRIPWIRE: the first committed generation presents within a couple of
	// loop passes of the report row landing; the bound only fires on a
	// genuine hang, not a loaded shard's scheduling delay.
	case <-time.After(30 * time.Second):
		lastFlushErr := flushErr()
		fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
		refusal := "fold unreadable"
		if foldErr == nil {
			claimedGen := uint64(0)
			for _, gen := range fold.resumeGenerations {
				if gen != 0 && gen > claimedGen {
					claimedGen = gen
				}
			}
			refusal = "none"
			for _, id := range fold.pendingIDs() {
				if fold.resumeGenerations[id] != 0 {
					continue
				}
				if err := validateDelegateAttentionResolutions(fold, []string{id}, delegateAttentionConsumed, claimedGen); err != nil {
					refusal = fmt.Sprintf("%s: %v", id, err)
					break
				}
			}
		}
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		t.Fatalf("the superseded claim stranded both rows (pending=%d (%v); claims=%v; unclaimed-row accept=%q; flush=%v)",
			len(pending), pendErr, fold.resumeGenerations, refusal, lastFlushErr)
	}
	if got, _ := wedgedSession.Load().(string); got != lane.delegateSess.id {
		t.Fatalf("the presentation ran on session %q, want the lane-owning delegate %q", got, lane.delegateSess.id)
	}
	if !sawSecondRow.Load() {
		t.Fatal("the presentation's request did not carry the second row's content")
	}

	// The wedged turn settles cleanly once released.
	releaseOnce.Do(func() { close(release) })
	// TRIPWIRE: the released turn settles within seconds once the model call
	// returns; the bound only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})

	// Both rows delivered: the fold holds nothing pending, one generation is
	// claimed by two distinct base rows — the takeover this test pins — and
	// the first-consumed row's reopen resolved after riding the presentation.
	// TRIPWIRE: the turn's markers land within seconds of the settle; the
	// bound only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "both rows to resolve after the presentation", func() bool {
		fold, err := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
		return err == nil && len(fold.pendingIDs()) == 0
	})
	fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
	if foldErr != nil {
		t.Fatalf("read the delegate's attention fold after the presentation: %v", foldErr)
	}
	genBases := map[uint64]map[string]bool{}
	for id, gen := range fold.resumeGenerations {
		if gen == 0 {
			continue
		}
		base := stripDelegateAttentionReopenSegments(id)
		if genBases[gen] == nil {
			genBases[gen] = map[string]bool{}
		}
		genBases[gen][base] = true
	}
	takeover := false
	for _, bases := range genBases {
		if len(bases) >= 2 {
			takeover = true
		}
	}
	if !takeover {
		t.Fatalf("no second base row claimed a recycled generation (claims=%v resolutions=%v)", fold.resumeGenerations, fold.resolutions)
	}
	reopenedResolved := false
	for id, gen := range fold.resumeGenerations {
		if gen == 0 {
			continue
		}
		if _, resolved := fold.resolutions[delegateAttentionReopenID(id, gen)]; resolved {
			reopenedResolved = true
		}
	}
	if !reopenedResolved {
		t.Fatalf("the first-consumed row's reopen never resolved (claims=%v resolutions=%v)", fold.resumeGenerations, fold.resolutions)
	}
}

// A user's stop of an in-flight attention presentation owns the quiet. The
// settle tail still reopens the consumed report — the row stays pending and
// recoverable through a later wake, a restart reconcile or an explicit send —
// but nothing may re-arm the automatic drive: a re-arm would at once start
// the generation the user just cancelled and re-present the report. The
// owner's notify hop is wired the way the daemon runs it, so the arm's
// consequence reaches a committed generation without the test driving it.
func TestDelegateAttentionUserStopsRunWithoutReArmingTheReport(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var wedgeOnce sync.Once
	wedge := func(req llm.Request) llm.Response {
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "user-stop-no-rearm", "dlg_userstop00001", "034userstop00001", wedge)
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored notify edge can launch its own pending
	// notification turn, which would consume the scripted wedge step this
	// test's presentation parks in; disarm it so the test loop below is the
	// only driver.
	lane.sharedSub.sess.SetNotifyFunc(nil)
	// The auto path a re-arm feeds: the controller's paced retry notifies the
	// owner session, whose drive pass selects the armed wake — the same hop
	// the daemon runs, so the arm's consequence is observable without the
	// test's own loop.
	lane.root.SetNotifyFunc(func() { go lane.root.drivePendingDelegateAttention() })

	// The drive loop's flush idiom pumps a delivery the spawn deferred past
	// its last drain cut before each pass.
	flushErr := lane.driveUntil(entered)
	select {
	case <-entered:
	// TRIPWIRE: the first drive presents the report within a couple of loop
	// passes; the bound only fires on a genuine hang, not a loaded shard's
	// scheduling delay.
	case <-time.After(30 * time.Second):
		lastFlushErr := flushErr()
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		t.Fatalf("the presentation never reached its model request (pending attention=%d (%v); flush=%v)",
			len(pending), pendErr, lastFlushErr)
	}

	// The user stops the run while its first model request is still parked:
	// the trigger is latched and nothing has presented. The run's own done
	// channel is captured before the release, because a restarted generation
	// would reset it.
	if got, err := lane.root.StopDelegateRun(lane.fixture.delegateID); err != nil || got != appwire.DelegateStopStopping {
		t.Fatalf("StopDelegateRun = %q, %v; want stopping", got, err)
	}
	lane.delegateSub.mu.Lock()
	runDone := lane.delegateSub.done
	lane.delegateSub.mu.Unlock()
	// The parked request settles through the cancel; the settle tail — the
	// reopen and the re-arm decision included — runs before the run's done
	// channel closes.
	releaseOnce.Do(func() { close(release) })
	select {
	case <-runDone:
	// TRIPWIRE: the cancelled run settles within seconds of the release; the
	// bound only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the user-stopped run never completed its settle tail")
	}

	// The contract, read from the state the settle left: nothing arms the
	// stopped delegate's wake — the controller holds no pending attention
	// entry, and the session owes no arm retry — so no drive can select it
	// and no generation can commit. The settle tail ran before runDone
	// closed, so the re-arm decision is already final here; the owner's
	// wired notify hop would drive anything that armed.
	if lane.root.delegateController.hasPendingDelegateAttention() {
		t.Fatalf("the user's stop left the delegate's attention armed, want nothing armed to drive the cancelled generation")
	}
	if lane.delegateSess.hasPendingDelegateAttentionArmRetry() {
		t.Fatal("the user's stop left a pending attention arm retry, want nothing armed to drive the cancelled generation")
	}

	// The stop owned the quiet, not the loss: the reopen kept the report
	// pending under its fresh identity, recoverable by a later wake, a
	// restart reconcile or an explicit send.
	fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
	if foldErr != nil {
		t.Fatalf("read the delegate's attention fold after the stop: %v", foldErr)
	}
	var reopenedPending bool
	for _, id := range fold.pendingIDs() {
		if strings.Contains(id, "/reopened/") {
			reopenedPending = true
		}
	}
	if !reopenedPending {
		t.Fatalf("the user-stopped run's report was not left pending and recoverable (pending=%v resolutions=%v)", fold.pendingIDs(), fold.resolutions)
	}
}

// A stop admitted between a reopen's teardown guards and its durable append
// owns the row the append then lands: the stop's discard enumerated attention
// by the ids it knew at plan time, and nothing notes the fresh identity, so
// without a re-check the row would survive as pending on a deliberately
// stopped delegate. The seam pins the race — it admits the stop exactly inside
// the guard-to-append window — and a failing attention-transcript opener
// holds that stop in flight for the whole assertion: the stop driver's
// completion cannot collect its evidence, while the reopen's own append and
// the re-check's discard write through the receiver's resident writer. The
// reopen must re-check the teardown predicates after its append and hand the
// fresh row to the stop that admitted mid-window.
func TestDelegateAttentionStopAdmittedDuringReopenResolvesFreshRow(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	lane := spawnSharedDescendantLane(t, r, "stop-during-reopen", "dlg_stopreopen0001", "034stopreopen0001")
	lane.delegateSess.SetNotifyFunc(nil)

	// Hold the stop the seam admits in flight: its driver cannot complete
	// without reading the covered members' attention transcripts, and the
	// failing opener refuses that read until the test restores it for cleanup.
	tree := lane.root.delegateController
	tree.mu.Lock()
	realAttentionOpen := tree.attentionOpen
	tree.attentionOpen = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return nil, nil, errors.New("test: attention transcript unavailable")
	}
	tree.mu.Unlock()
	var restoreOnce sync.Once
	t.Cleanup(func() {
		restoreOnce.Do(func() {
			tree.mu.Lock()
			tree.attentionOpen = realAttentionOpen
			tree.mu.Unlock()
		})
	})

	// Refuse exactly the delegate's first attention commit as busy: its
	// reopen is the one the stop must catch.
	var faults atomic.Int32
	updateSessionTestConfig(lane.root, func(cfg *testConfig) {
		cfg.delegateAttentionCommitStartFault = func() error {
			if faults.Add(1) <= 1 {
				return errDelegateTargetBusy
			}
			return nil
		}
	})

	// Admit the stop exactly inside the reopen's guard-to-append window. The
	// reopen reads its seam on the delegate's own config, and the idle
	// delegate's cancel plan is empty — the admission itself is the event.
	stopAdmitted := make(chan struct{})
	var stopOnce sync.Once
	updateSessionTestConfig(lane.delegateSess, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeReopenAppend = func() {
			stopOnce.Do(func() {
				_, cancelPlan, _, err := lane.root.delegateController.StopSubtree(rootDelegateActor(lane.root.delegateRootSessionID), lane.fixture.delegateID)
				if err != nil {
					t.Errorf("StopSubtree inside the reopen window: %v", err)
					return
				}
				executeDelegateCancelPlan(cancelPlan)
				close(stopAdmitted)
			})
		}
	})

	// Drive the presentation: the busy arm reopens the consumed report, the
	// seam admits the stop mid-window, and the loop's own drives end once the
	// reopened row is no longer pending.
	// Flush a report delivery the spawn deferred past its last drain cut
	// before the first drive: the loop returns on the drive's false, which a
	// stranded deferred row would report as nothing owed (an empty queue is a
	// no-op).
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
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			flush()
			if !lane.root.driveDelegateAttention(lane.delegateSub) {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	select {
	case <-stopAdmitted:
	// TRIPWIRE: the seam admits the stop inside the first reopen window, a
	// couple of drive passes in; the bound only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		flushMu.Lock()
		lastFlushErr := flushErr
		flushMu.Unlock()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs()
		t.Fatalf("the reopen window never admitted the stop (pending attention=%d (%v); deferred deliveries=%v flush=%v)",
			len(pending), pendErr, deferred, lastFlushErr)
	}

	// The re-check's contract: the stop owns the freshly appended row, so
	// nothing pending survives and the fold records the hand-off.
	// TRIPWIRE: the re-check resolves the fresh row at the append that raced
	// the admitted stop, within seconds; the bound only fires on a hang.
	waitForCondition(t, 30*time.Second, "the reopen's fresh row to be resolved for the stop admitted mid-window", func() bool {
		fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
		if foldErr != nil {
			return false
		}
		for _, id := range fold.pendingIDs() {
			if strings.Contains(id, "/reopened/") {
				return false
			}
		}
		return true
	})
	fold, foldErr := readDelegateAttentionFold(lane.delegateSess.TranscriptPath(), lane.delegateSess.id)
	if foldErr != nil {
		t.Fatalf("read the delegate's attention fold: %v", foldErr)
	}
	resolved := false
	for id, disposition := range fold.resolutions {
		if !strings.Contains(id, "/reopened/") {
			continue
		}
		resolved = true
		if disposition != delegateAttentionDiscarded {
			t.Fatalf("the reopened row resolved as %q, want discarded for the admitted stop", disposition)
		}
	}
	if !resolved {
		t.Fatal("the reopen never appended a fresh row for the re-check to resolve")
	}
	// The stop the re-check answered really is the one in flight: the failing
	// opener keeps its driver from completing past this point.
	if !lane.root.delegateController.attentionReopenRefused(lane.fixture.delegateID) {
		t.Fatal("the stop admitted mid-window is no longer in flight")
	}
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
