package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	"primeradiant.com/evener/internal/testwait"
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
	requireSessionLivelock(t)
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
	requireSessionLivelock(t)
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
	// The refused revive leaves its committed start unlaunched: fail it as
	// the send that owns it does, so the root's Close has no run to wait on.
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))

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
	settleSendDelivery(t, root, first)
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
	settleSendDelivery(t, root, second)

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
	// The refused revive leaves its committed start unlaunched: fail it as
	// the send that owns it does, so the root's Close has no run to wait on.
	_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
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
	settleSendDelivery(t, root, first)
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
	settleSendDelivery(t, root, first)
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
	fixture  coldDelegateFixture
	lanePath string
	// announceHold is the channel this lane's fixture took from the
	// announce-hold registry: every finalize tail in this tree parks on it
	// until the test closes it.
	announceHold chan struct{}
	// announceRelease closes the taken hold exactly once, from the test body
	// or the fixture's own cleanup, whichever runs first: the fixture
	// registers that cleanup after root.Close and before it installs the
	// announce hook, so a fatal after the hook is installed cannot leave a
	// parked finalize tail blocked past test end. Nothing parks on the hold
	// before the hook is installed.
	announceRelease  func()
	root             *Session  // the fixture's root, which owns the lane-owning delegate
	delegateSub      *subagent // the lane-owning delegate, as root sees it
	delegateSess     *Session  // the lane-owning delegate's runtime, which drives the child's wake
	sharedSub        *subagent
	sharedDelegateID string
	finish           func(llm.Request) llm.Response
	// wake and its notifier are the lane's shared drive edge: the notify
	// callbacks startDrive installs signal it, and every loop on the lane
	// waits on it, so a finalize drain that captures and restores the callback
	// restores the same edge every loop reads.
	wake   chan struct{}
	signal func()
	// testDone closes with the test's own context: a drive worker ends on it,
	// so a failed wait cannot leave a worker driving past test end.
	testDone <-chan struct{}
	// classifications records every delegateDeliveryClassified fire in this
	// tree, so a tripwire can name whether the retained child's report reached
	// the delivery admission path at all and with which deferral.
	classifications *delegateDeliveryClassificationLog
}

// reportAttentionID is the retained child's first report as the attention row
// every scripted-presentation test expects pending.
func (l *sharedDescendantLane) reportAttentionID() string {
	return delegateAttentionID(delegateDeliveryID(l.sharedDelegateID, 1))
}

// delegateAttentionDiagnostics is the controller-owned state a scripted
// tripwire prints. The in-memory delivery queue a tripwire reads separately
// is not the durable head: the report can wait in the journal while the
// session queue is empty, and the reverse; durable is keyed by delegate id,
// so this reads the lane-owning delegate's own aggregate and the retained
// child's, not either child session id.
type delegateAttentionDiagnostics struct {
	delegatePhase          string
	delegateGen            uint64
	delegateOpen           bool
	delegateDurable        []string
	childPhase             string
	childGen               uint64
	childOpen              bool
	childPrepared          bool
	childDurable           []string
	reportDeliveryID       string
	claimPresent           bool
	claimWaiter            bool
	claimHeld              bool
	claimStartRefused      bool
	receiptPresent         bool
	receiptRetryable       bool
	receiptInline          bool
	ownDeliveryID          string
	ownClaimPresent        bool
	ownClaimHeld           bool
	ownClaimStartRefused   bool
	ownReceiptPresent      bool
	ownReceiptRetryable    bool
	ownReceiptInline       bool
	blockingStartWork      bool
	hasReservation         bool
	needsAttention         bool
	notedWakes             []string
	ownerLivePresent       bool
	ownerRuntimeSet        bool
	ownerRuntimeIsDelegate bool
	ownerBinding           bool
	receiverType           string
}

func (l *sharedDescendantLane) attentionDiagnostics() delegateAttentionDiagnostics {
	controller := l.root.delegateController
	controller.mu.Lock()
	defer controller.mu.Unlock()
	diag := delegateAttentionDiagnostics{
		reportDeliveryID: delegateDeliveryID(l.sharedDelegateID, 1),
		ownDeliveryID:    delegateDeliveryID(l.fixture.delegateID, 1),
	}
	if agg := controller.durable[l.fixture.delegateID]; agg != nil {
		diag.delegatePhase, diag.delegateGen, diag.delegateOpen = string(agg.Phase), agg.Generation, agg.CurrentRunOpen
		for _, pending := range agg.PendingDeliveries {
			diag.delegateDurable = append(diag.delegateDurable, pending.DeliveryID)
		}
		diag.needsAttention = agg.NeedsAttention
	}
	if agg := controller.durable[l.sharedDelegateID]; agg != nil {
		diag.childPhase, diag.childGen, diag.childOpen, diag.childPrepared = string(agg.Phase), agg.Generation, agg.CurrentRunOpen, agg.PreparedTerminal != nil
		for _, pending := range agg.PendingDeliveries {
			diag.childDurable = append(diag.childDurable, pending.DeliveryID)
		}
	}
	if claim := controller.deliveryClaims[diag.reportDeliveryID]; claim != nil {
		diag.claimPresent, diag.claimWaiter, diag.claimHeld, diag.claimStartRefused = true, claim.waiter != nil, claim.held, claim.startRefused
	}
	if receipt := controller.deliveryReceiptLocked(diag.reportDeliveryID); receipt != nil {
		diag.receiptPresent, diag.receiptRetryable, diag.receiptInline = true, receipt.retryable, receipt.inline
	}
	if claim := controller.deliveryClaims[diag.ownDeliveryID]; claim != nil {
		diag.ownClaimPresent, diag.ownClaimHeld, diag.ownClaimStartRefused = true, claim.held, claim.startRefused
	}
	if receipt := controller.deliveryReceiptLocked(diag.ownDeliveryID); receipt != nil {
		diag.ownReceiptPresent, diag.ownReceiptRetryable, diag.ownReceiptInline = true, receipt.retryable, receipt.inline
	}
	diag.blockingStartWork = controller.hasDeliveryWorkForOwnerBlockingStartLocked(l.fixture.delegateID)
	diag.hasReservation = controller.hasAttentionStartReservationLocked(l.fixture.delegateID)
	for wakeID := range controller.delegateAttentionWakeIDsLocked(l.fixture.delegateID) {
		diag.notedWakes = append(diag.notedWakes, wakeID)
	}
	slices.Sort(diag.notedWakes)
	ownerLive := controller.live[l.fixture.delegateID]
	diag.ownerLivePresent = ownerLive != nil
	diag.ownerRuntimeSet = ownerLive != nil && ownerLive.runtime != nil
	diag.ownerRuntimeIsDelegate = ownerLive != nil && ownerLive.runtime == l.delegateSess
	diag.ownerBinding = ownerLive != nil && ownerLive.binding != nil
	diag.receiverType = fmt.Sprintf("%T", controller.deliveryReceiverLocked(l.fixture.delegateID))
	return diag
}

// attentionScriptEvidence returns the scripted adapter's request log and the
// delegate's live attention fold. The adapter serves every session in the
// fixture from one FIFO, so the request log names which session's turn
// consumed each step; the fold's resolutions and resume generations name the
// writer of any consume marker (a generation-0 resolution is the coverage
// consume of a settled round that presented the row).
func (l *sharedDescendantLane) attentionScriptEvidence() (requestLog []string, liveFold string) {
	for i, req := range l.fixture.adapter.Requests() {
		requestLog = append(requestLog, fmt.Sprintf("%d:%s", i+1, req.SessionID))
	}
	fold, foldErr := readDelegateAttentionFold(l.delegateSess.TranscriptPath(), l.delegateSess.id)
	if foldErr != nil {
		return requestLog, fmt.Sprintf("%v", foldErr)
	}
	return requestLog, fmt.Sprintf("%v order=%v resumeGenerations=%v", fold.resolutions, fold.order, fold.resumeGenerations)
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

// The announce-hold registry seeds the loaded-runner interleave for the
// shared-descendant fixtures: a test registers a hold under its lane name
// before the fixture call, the fixture parks every pre-announce finalize tail
// on the hold it takes, and the test releases the hold its fixture took — the
// fixture's first send comes back while the lane-owning delegate's
// finalization drain is still parked on the retained child, so the release
// replays the shape where the child's tail completes only after the send has
// returned. Repeated copies of one test share a lane name, so the registry
// pools one channel per registration and a fixture takes exactly one; the
// lane records which one it took and the test releases that one, so a copy
// never releases a channel another copy's fixture parked on.
var sharedDescendantAnnounceHoldsMu sync.Mutex
var sharedDescendantAnnounceHolds = make(map[string][]chan struct{})

func holdSharedDescendantAnnounce(laneName string) {
	hold := make(chan struct{})
	sharedDescendantAnnounceHoldsMu.Lock()
	sharedDescendantAnnounceHolds[laneName] = append(sharedDescendantAnnounceHolds[laneName], hold)
	sharedDescendantAnnounceHoldsMu.Unlock()
}

func takeSharedDescendantAnnounceHold(laneName string) chan struct{} {
	sharedDescendantAnnounceHoldsMu.Lock()
	defer sharedDescendantAnnounceHoldsMu.Unlock()
	holds := sharedDescendantAnnounceHolds[laneName]
	if len(holds) == 0 {
		return nil
	}
	hold := holds[0]
	sharedDescendantAnnounceHolds[laneName] = holds[1:]
	return hold
}

func quietWorktreeAttentionResponse(llm.Request) llm.Response {
	return quietYieldResponse("ack-worktree-attention", `{"message":"waiting on work","next":"log_quietly_and_end_turn"}`)
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
	// A registered announce hold parks every finalize tail in the tree just
	// before its announce, until the test releases it. The retained child's
	// announce is always the first to arrive: the delegate's own announce is
	// sequenced behind its finalization drain, which waits for the child's
	// run to return.
	var takenAnnounceHold chan struct{}
	var releaseAnnounceHold func()
	if hold := takeSharedDescendantAnnounceHold(laneName); hold != nil {
		takenAnnounceHold = hold
		var releaseOnce sync.Once
		releaseAnnounceHold = func() { releaseOnce.Do(func() { close(hold) }) }
	}
	// A scripted presentation step requires a registered announce hold. The
	// delegate's spawn turn consumes the scripted steps before the
	// presentation, and without the hold the retained child's report delivery
	// can be classified deferred while that turn is still running, pump at the
	// next request build, and ride the spawn turn's own tool-result request —
	// the coverage consume then resolves the report and the scripted step the
	// test waits on never runs. Refuse the fixture instead of leaving that to
	// load: holdSharedDescendantAnnounce must register the hold before the
	// spawn, and the test releases it once the spawn turn has closed.
	if len(notificationTurn) > 0 && notificationTurn[0] != nil && takenAnnounceHold == nil {
		signalSharedDescendantRefusal(t)
		t.Fatalf("a scripted presentation step for lane %q requires a registered announce hold: call holdSharedDescendantAnnounce(%q) before spawning, or drop the scripted step for the bare-text fallback", laneName, laneName)
	}
	var lanePath string
	fixture := newColdDelegateFixtureConfiguredInState(t, "", r.stateDir, func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		d.ToolNameCeiling = []string{"communicate", "delegate"}
		d.DelegationAllowance = 1
		lanePath = r.delegateLane(t, laneName, standInDlg, standInSID)
		d.WorkingDir = lanePath
	})
	// The tree must not release on its own (the idle-release machinery
	// claims settled subtree leaves on a schedule the test cannot see) and
	// must not depend on the runner having a sandbox wrapper: the wake's
	// lane validation runs git through the delegate session's own
	// environment, and the CI runners have no bwrap to wrap it with — a
	// capable-prober fixture would exit 127 on every git call there. Its
	// tests also script every presentation drive themselves, so the
	// restored runtime's parent-drive notify edge is suppressed: that edge
	// is installed inside the first send below, before any test-side
	// disarm can reach it, and an edge-fired drive in the window after the
	// send returns would present against the tree-held lane and steal the
	// scripted adapter step — the loaded-runner shape the failed-start
	// test seeds and pins.
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir, testConfig{
		sandboxProber:                      unsandboxedProber(fixture.workspace),
		disableDelegateIdleRelease:         true,
		disableRestoredDelegateNotifyDrive: true,
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
	// tailParked closes when the first finalize tail, the retained child's,
	// parks on the announce hold. drainParked closes when the delegate's
	// finalization drain parks after that, waiting on the held child.
	tailParked, drainParked := make(chan struct{}), make(chan struct{})
	if takenAnnounceHold != nil {
		// Register after root.Close, before the first send below can fatal:
		// cleanups run last-in-first-out, so the hold releases before the
		// root tears down and waits on the finalize tails parked on it. The
		// once makes the test's own release race the cleanup safely. Nothing
		// parks on the hold before the hook below is installed.
		t.Cleanup(releaseAnnounceHold)
		var parkedOnce sync.Once
		root.cfg.testOnly.subagentBeforeGenerationAnnounced = func(*subagent) {
			parkedOnce.Do(func() { close(tailParked) })
			<-takenAnnounceHold
		}
		var drainParkedOnce sync.Once
		root.cfg.testOnly.jobTreeDrainParked = func(s *Session) {
			if s.id != fixture.childID {
				return
			}
			select {
			case <-tailParked:
				drainParkedOnce.Do(func() { close(drainParked) })
			default:
			}
		}
	}
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
	notify := notificationTurn
	if len(notify) == 0 {
		notify = []func(llm.Request) llm.Response{quietWorktreeAttentionResponse}
	}
	// The delegate's turn spawns its child, the child's turn finishes, the
	// delegate's follow-up turn finishes, and the delegate's attention turn
	// presenting the retained child's report answers the scripted step (or
	// quietly yields when none is given).
	fixture.adapter.setSteps(append([]func(llm.Request) llm.Response{spawn, finish, finish}, notify...))
	// A held tree's generation cannot finish until the test releases the
	// hold, so the send's inline wait can only time out: take the shortest
	// one, and wait instead for the retained child's tail to park on the hold
	// and then for the delegate's finalization drain to park behind it.
	firstWaitMS := 10_000
	if takenAnnounceHold != nil {
		firstWaitMS = 1
	}
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", firstWaitMS)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	settleSendDelivery(t, root, first)
	delegateSub := root.subagents.get(fixture.childID)
	if delegateSub == nil || delegateSub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	if takenAnnounceHold != nil {
		waitForTestSignal(t, tailParked, "the retained child's finalize tail to park on the announce hold")
		waitForTestSignal(t, drainParked, "the delegate's finalization drain to park behind the held child")
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
	wake, signal := newDrainWake()
	return &sharedDescendantLane{
		fixture:          fixture,
		lanePath:         lanePath,
		announceHold:     takenAnnounceHold,
		announceRelease:  releaseAnnounceHold,
		root:             root,
		delegateSub:      delegateSub,
		delegateSess:     delegateSub.sess,
		sharedSub:        sharedSub,
		sharedDelegateID: sharedDelegateID,
		finish:           finish,
		wake:             wake,
		signal:           signal,
		testDone:         t.Context().Done(),
		classifications:  classifications,
	}
}

// sharedDescendantUnheldPresentationHelperEnv marks the re-exec that exercises
// the fixture's unheld-step refusal. sharedDescendantRefusalSignalEnv names
// the file the fixture writes before it refuses, so the parent can assert the
// refusal without matching diagnostic text.
const sharedDescendantUnheldPresentationHelperEnv = "EVENER_SHARED_DESCENDANT_UNHELD_PRESENTATION_HELPER"
const sharedDescendantRefusalSignalEnv = "EVENER_SHARED_DESCENDANT_REFUSAL_SIGNAL"

// signalSharedDescendantRefusal writes the regression child's structured
// refusal signal. It is a no-op outside that child, which is the only process
// the signal environment is set for.
func signalSharedDescendantRefusal(t *testing.T) {
	t.Helper()
	path := os.Getenv(sharedDescendantRefusalSignalEnv)
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte("refused\n"), 0o600); err != nil {
		t.Fatalf("write announce-hold refusal signal: %v", err)
	}
}

// Regression for #5717: a scripted presentation step with no announce hold
// let the retained child's report be consumed inside the delegate's spawn
// window. The fixture refuses the step with a t.Fatalf, so assert that refusal
// from a re-exec: the helper process must fail and write the refusal signal.
func TestSharedDescendantLaneRefusesUnheldScriptedPresentation(t *testing.T) {
	t.Parallel()
	if os.Getenv(sharedDescendantUnheldPresentationHelperEnv) == "1" {
		// The guard fires before spawn reads the repo, so the refusal path
		// needs none; a removed guard writes no signal, so the parent fails.
		spawnSharedDescendantLane(t, nil, "unheld-scripted-presentation", "dlg_unheldpresent01", "034unheldpresent01", func(llm.Request) llm.Response {
			return agenttest.FinalResponse("done")
		})
		t.Fatal("spawnSharedDescendantLane accepted a scripted presentation step without an announce hold")
	}
	signalPath := filepath.Join(t.TempDir(), "announce-hold-refusal")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSharedDescendantLaneRefusesUnheldScriptedPresentation$", "-test.v")
	cmd.Env = append(os.Environ(), sharedDescendantUnheldPresentationHelperEnv+"=1", sharedDescendantRefusalSignalEnv+"="+signalPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the unheld scripted presentation was accepted; want the fixture's announce-hold refusal\n%s", out)
	}
	if _, statErr := os.Stat(signalPath); statErr != nil {
		t.Fatalf("the helper process failed without the announce-hold refusal signal (%v); want the fixture's refusal\n%s", statErr, out)
	}
}

// adoptForeignLane installs a foreign session's lock on the lane, contending
// with the tree's own wake machinery: a background wake for the shared child
// revalidates the lane at any moment and re-takes a just-released lane under
// the tree's own marker — the behavior under test — so the adoption retries
// until the foreign lock is the one that holds.
func adoptForeignLane(t *testing.T, r *wtRepo, lanePath, foreignSID string) {
	t.Helper()
	// The adoption is a state, not a command's success: a background wake
	// re-takes any gap, so retry until the lock that reads back is the foreign
	// one.
	testwait.UntilSeen(t, "the lane's foreign lock to hold", func() (bool, string) {
		if _, locked, _ := r.laneLocked(t, lanePath); locked {
			_, _ = runWorktreeGit(r.mainRoot, "worktree", "unlock", lanePath)
		}
		_, lockErr := runWorktreeGit(r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath)
		_, locked, reason := r.laneLocked(t, lanePath)
		if lockErr == nil && locked {
			if m, ok := worktree.ParseMarker(reason); ok && m.SessionID == foreignSID {
				return true, ""
			}
		}
		return false, reason
	})
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

// captureDue reports whether a polling wait has reached the last second of its
// testwait budget, when the failure testwait is about to print is fixed. A cond
// whose failure text shells out to git, or reads the whole adapter script, runs
// that capture only then rather than on every poll.
func captureDue(t *testing.T, start time.Time) bool {
	t.Helper()
	return testwait.Budget(t)-time.Since(start) <= time.Second
}

// awaitActivityPublishFloor waits out the activity coalescer's window on the
// delegate's row: a report inside it advances the activity clock but publishes
// nothing, so its lane-evidence refresh rides the next publication. Waiting
// here establishes only that a later report is publication-eligible; it does
// not prove the freshness a test then observes is that report's, because the
// evidence ticker can refresh the same mtime.
func awaitActivityPublishFloor(t *testing.T, controller *delegateTreeController, delegateID string) {
	t.Helper()
	testwait.UntilSeen(t, "the delegate's activity publication floor to clear", func() (bool, string) {
		controller.mu.Lock()
		var published time.Time
		if live := controller.live[delegateID]; live != nil {
			published = live.activityPublishedAt
		}
		elapsed := controller.now().Sub(published)
		controller.mu.Unlock()
		return elapsed >= delegateActivityPublishInterval, elapsed.Round(time.Millisecond).String()
	})
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
	awaitActivityPublishFloor(t, root.delegateController, fixture.delegateID)
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
	// Seed the announce hold the scripted presentation step requires — the
	// window it closes and the coverage race it prevents are stated at
	// spawnSharedDescendantLane's refusal.
	holdSharedDescendantAnnounce("active-notify-lane")
	lane := spawnSharedDescendantLane(t, r, "active-notify-lane", "dlg_waketest0000008", "034placeholder000000012", wedge)
	lane.releaseSeededAnnounceHold(t)
	parentTranscript := schema.SessionTranscriptPath(lane.fixture.stateDir, lane.fixture.meta.ID)

	// The retained child's report reaches its parent as a durable attention
	// row, and the parent's attention turn — the turn that presents the
	// report — wedges inside its first model call. The tree is alive, the
	// parent runs a turn, and the lane's own marker parent stays idle. The
	// drive retries that parent's wake-edge on each wake the lane's notify
	// callbacks signal, because the owed attention arms asynchronously: an
	// attempt that reads it once can miss it, and exiting on the first
	// attempt would strand the wedge turn on the owner's slow quiet-watch
	// retry. Those retries end when the presentation turn enters or the test
	// ends, not with the tripwire below. The loop drives the same attention
	// primitive the owner's wake-edge uses, so the scripted step is the
	// presentation turn itself and never an empty notification drive. The
	// report can be classified while the root is processing and deferred to
	// the lane-owning delegate's delivery queue, where only the background
	// retry would pump it: each drive pass flushes that queue while the
	// delegate is idle, so a deferred report is appended deterministically
	// and an empty queue is a no-op.
	flushErr := lane.driveUntil(entered)
	select {
	case <-entered:
	// TRIPWIRE: the armed attention turns over within seconds of the send
	// settling; this bound only fires on a genuine hang. It covers a shard
	// loaded with the rest of this package's parallel tests, where spawn and
	// the arm both stretch.
	case <-time.After(30 * time.Second):
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
		lastFlushErr := flushErr()
		deferred := lane.delegateSess.hasPendingDelegateDeliveries()
		lane.sharedSub.mu.Lock()
		childRunning, childClosed, childFinalizing := lane.sharedSub.running, lane.sharedSub.closed, lane.sharedSub.finalizing
		lane.sharedSub.mu.Unlock()
		stopGated := lane.root.childStopGated(lane.delegateSess.id)
		fatalGated := lane.root.childFatalRunGated(lane.delegateSess.id)
		drainAbandoned := lane.root.childDrainAbandoned(lane.delegateSess.id)
		drainGrace := lane.root.childDrainGracePending(lane.delegateSess.id)
		committedStart := lane.root.childCommittedSendStart(lane.delegateSess.id)
		diag := lane.attentionDiagnostics()
		// The parked presentation holds the scripted adapter's mutex while it
		// waits on release; the request log below needs that mutex, so unblock
		// the step first or the timeout diagnostic deadlocks instead of failing.
		releaseOnce.Do(func() { close(release) })
		requestLog, liveResolutions := lane.attentionScriptEvidence()
		classifications := lane.classifications.snapshot()
		sharedSessionID := lane.sharedSub.sess.id
		tracked := lane.delegateSess.subagents.get(sharedSessionID) != nil
		lane.sharedSub.mu.Lock()
		childStatus, childTurns, childEndEmitted := lane.sharedSub.status, lane.sharedSub.turnsUsed, lane.sharedSub.endEmitted
		lane.sharedSub.mu.Unlock()
		rootPending := lane.root.hasPendingDelegateDeliveries()
		t.Fatalf("the retained child's report was never presented (lane locked=%v reason=%q, delegate sub closed=%v running=%v driving=%v finalizing=%v disposeGated=%v attentionDriveRefused=%v; retirement mutation=%v, lane validation=%v, drive slot=%v, driver closing=%v; pending attention=%d (%v), child pending=%d (%v), child run=%v/%v/%v, deferred deliveries=%v rootPending=%v flush=%v, gates: stop=%v fatal=%v drainAbandoned=%v drainGrace=%v committedSendStart=%v; durable: delegate phase=%s gen=%d open=%v pending=%v; child phase=%s gen=%d open=%v prepared=%v pending=%v; report %s claim=%v(waiter=%v held=%v startRefused=%v) receipt=%v(retryable=%v inline=%v); owner live=%v runtimeSet=%v runtimeIsDelegate=%v binding=%v receiver=%s; classified=%v; child sub session=%s status=%v turns=%d endEmitted=%v tracked=%v; requests=%v; live fold: %s)",
			locked, reason, closed, running, driving, finalizing, disposeGated, attentionRefused, mutErr, revalErr, driveSlotOK, driverClosing,
			len(pending), pendErr, len(childPending), childPendErr, childRunning, childClosed, childFinalizing, deferred, rootPending, lastFlushErr, stopGated, fatalGated, drainAbandoned, drainGrace, committedStart,
			diag.delegatePhase, diag.delegateGen, diag.delegateOpen, diag.delegateDurable,
			diag.childPhase, diag.childGen, diag.childOpen, diag.childPrepared, diag.childDurable,
			diag.reportDeliveryID, diag.claimPresent, diag.claimWaiter, diag.claimHeld, diag.claimStartRefused, diag.receiptPresent, diag.receiptRetryable, diag.receiptInline,
			diag.ownerLivePresent, diag.ownerRuntimeSet, diag.ownerRuntimeIsDelegate, diag.ownerBinding, diag.receiverType,
			classifications, sharedSessionID, childStatus, childTurns, childEndEmitted, tracked, requestLog, liveResolutions)
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
	releaseOnce.Do(func() { close(release) })
	testwait.Until(t, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
}

// releaseSeededAnnounceHold ends the announce window a scripted presentation
// step requires and waits out the lane-owning delegate's own finalize tail.
// The held announce parked the retained child's report delivery with the
// child's finalize tail, so releasing it lands the report as a pending row
// while the delegate is past its spawn turn — the report can then only
// present through the scripted step. Waiting the delegate's tail out, re-arm
// included, keeps a later scripted presentation from racing the row's
// landing; the report must be pending by then, so the test's own drive is the
// only thing that can present it.
func (l *sharedDescendantLane) releaseSeededAnnounceHold(t *testing.T) {
	t.Helper()
	if l.announceRelease == nil {
		t.Fatalf("no announce hold was taken for this lane: register one with holdSharedDescendantAnnounce before the spawn")
	}
	l.announceRelease()
	l.delegateSub.mu.Lock()
	done := l.delegateSub.done
	l.delegateSub.mu.Unlock()
	select {
	case <-done:
	// TRIPWIRE: the tail finishes within a couple of drain passes of the
	// release; the bound only fires on a genuine hang, absorbing a loaded
	// shard's stretched finalize tails and git retries.
	case <-time.After(15 * time.Second):
		t.Fatalf("the delegate's finalize tail never finished after the seeded announce release")
	}
	// The delivery cannot land inside a fixture-window run, so the retained
	// child's first report must be the one pending row here; nothing below
	// races the row's landing. A different pending row, or none, would mean
	// the report was consumed before the test's own drive — the #5717 shape.
	reportRowID := l.reportAttentionID()
	pending, pendErr := l.delegateSess.pendingDelegateAttentionIDs()
	if pendErr != nil || len(pending) != 1 || pending[0] != reportRowID {
		t.Fatalf("the retained child's report is not the one pending row after the seeded window: pending=%v want %q (%v)", pending, reportRowID, pendErr)
	}
}

// driveUntil starts a drive worker in the seeding idiom the attention
// mechanism tests share and returns its last flush error, for the failure dumps
// that name a stranded delivery. See startDrive.
func (l *sharedDescendantLane) driveUntil(stop <-chan struct{}) func() error {
	flush, _ := l.startDrive(stop)
	return flush
}

// startDrive starts one drive worker and returns the flush-error reader plus a
// channel that closes when the worker exits. The worker drives once up front
// for rows already presentable — in the worker, so startDrive does not run
// that pass inline — then on each wake edge the lane's notify callbacks signal:
// the daemon reaches a presentation on its wake edge, not on a cadence, and a
// refused drive arms the root's paced retry. A callback the test itself wired
// is its own driver and is left alone. stop outranks a
// buffered wake: once the caller closes it, no new pass starts. A pass already
// admitted when stop closes can still be in flight, so a caller that must not
// drive a runtime it has moved past joins the returned done channel. The worker
// ends on the test's own context too, so a failed wait cannot leave it driving
// past test end.
func (l *sharedDescendantLane) startDrive(stop <-chan struct{}) (func() error, <-chan struct{}) {
	var flushMu sync.Mutex
	var flushErr error
	record := func(err error) {
		if err == nil {
			return
		}
		flushMu.Lock()
		flushErr = err
		flushMu.Unlock()
	}
	// The seeding idiom's pass: a delivery the spawn deferred past its last
	// drain cut is pumped while the delegate is idle, then the drive runs.
	drive := func() {
		l.delegateSub.mu.Lock()
		busy := l.delegateSub.running || l.delegateSub.driving || l.delegateSub.finalizing
		l.delegateSub.mu.Unlock()
		if !busy {
			record(l.delegateSess.flushPendingDelegateDeliveries())
		}
		l.root.driveDelegateAttention(l.delegateSub)
	}
	for _, sess := range []*Session{l.delegateSess, l.root} {
		sess.mu.Lock()
		unwired := sess.notifyFunc == nil
		sess.mu.Unlock()
		if unwired {
			sess.SetNotifyFunc(l.signal)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopped := func() bool {
			return channelClosed(stop) || channelClosed(l.testDone)
		}
		// The first pass rides the worker so a caller blocked in a drive hook
		// cannot outrun the timeout it starts after this call returns. A wake
		// wired to another driver can close stop first; then the test has
		// moved on and this pass must not flush or drive.
		if stopped() {
			return
		}
		drive()
		for {
			if stopped() {
				return
			}
			select {
			case <-stop:
				return
			case <-l.testDone:
				return
			case <-l.wake:
			}
			if stopped() {
				return
			}
			drive()
		}
	}()
	read := func() error {
		flushMu.Lock()
		defer flushMu.Unlock()
		return flushErr
	}
	return read, done
}

// A cold delegate's scheduled restore revalidates its lane before any launch
// runs, so a foreign-held lane refuses the restore itself: no launch refusal
// stamps the child and no resident runtime retries into the launch-side quiet.
// The owed row must still leave the drain's quiescence scans while it stays
// pending for the paced retry, or a one-shot drain waits on a lane another
// session holds forever. Once the lane is given back the restore revalidates
// it, ends the quiet, and presents the report.
func TestColdDelegateAttentionHeldLaneLeavesDrainQuiescence(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		lanePath = r.delegateLane(t, "cold-held-quiet", "dlg_coldheldquiet01", "034coldheldquiet001")
		d.WorkingDir = lanePath
	})
	const attentionID = "delegate:cold-held-report"
	writer, err := transcript.NewWriter(transcriptPath(fixture.stateDir, fixture.childID), transcript.Header{
		SessionID:       fixture.childID,
		ParentSessionID: fixture.meta.ID,
		Task:            "cold held report",
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		WorkingDir:      fixture.workspace,
		Depth:           1,
	})
	if err != nil {
		t.Fatalf("create cold delegate transcript: %v", err)
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("cold held report"))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		t.Fatalf("append cold delegate attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close cold delegate transcript: %v", err)
	}

	const foreignSID = "034coldforeignholder001"
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lanePath)

	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir,
		testConfig{sandboxProber: unsandboxedProber(fixture.workspace)})
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	if !root.delegateController.owesDelegateAttention(fixture.delegateID) {
		t.Fatal("the cold delegate's report is not owed before the drive")
	}
	if !root.delegateController.hasDeliverableDelegateAttention() {
		t.Fatal("the cold delegate's report is not deliverable before the refusal")
	}

	entered := make(chan struct{})
	var enteredOnce sync.Once
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		if req.SessionID == fixture.childID {
			enteredOnce.Do(func() { close(entered) })
		}
		return agenttest.FinalResponse("done")
	}})
	quietCleared := make(chan bool, 4)
	root.cfg.testOnly.afterDelegateAttentionRestore = func(delegateID string, _ *subagent) {
		if delegateID != fixture.delegateID {
			return
		}
		select {
		case quietCleared <- root.delegateController.hasDeliverableDelegateAttention():
		default:
		}
	}

	root.drivePendingDelegateAttention()
	if !root.delegateController.owesDelegateAttention(fixture.delegateID) {
		t.Fatal("a foreign-held lane dropped the cold delegate's owed report instead of keeping it for the paced retry")
	}
	if root.delegateController.hasDeliverableDelegateAttention() {
		// Error, not Fatal: the unlock below releases the lane so the deferred
		// Close can finish, and the failure still names the regression.
		t.Error("a cold delegate's foreign-held lane still counts as deliverable work: the drain cannot give up")
	}
	if _, locked, reason := r.laneLocked(t, lanePath); !locked || !strings.Contains(reason, foreignSID) {
		t.Fatalf("the refusal touched the foreign lock: locked=%v reason=%q", locked, reason)
	}

	// Give the lane back: the next restore revalidates it, ends the quiet, and
	// presents the report.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	root.drivePendingDelegateAttention()
	select {
	case deliverable := <-quietCleared:
		if !deliverable {
			t.Fatal("a successful restore left the owed report quiet: the drain would ignore fresh attention")
		}
	default:
		t.Fatal("the unlocked lane's restore never reached the attention drive")
	}
	select {
	case <-entered:
	// TRIPWIRE: the scripted in-process adapter answers as soon as the restored
	// run reaches it; 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the restored cold delegate never presented its owed report")
	}
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
	// The parked-tail pin (the busy-commit test's form): unpinned, the spawn
	// can leave its interrupted presentation owed, and the first drive pass
	// admits that owed start as a no-model-call generation whose
	// finalization cleanup consumes the report row ahead of this test's own
	// drive — the loaded-runner interleave that leaves the wedge turn with
	// no row to present. The parked announce keeps the delivery outside
	// every fixture-window run, so the loop below presents the row as a real
	// model call after the tail closes.
	holdSharedDescendantAnnounce("stop-mid-presentation")
	lane := spawnSharedDescendantLane(t, r, "stop-mid-presentation", "dlg_stopmidpresnt01", "034stopmidpresnt01", wedge)
	lane.releaseSeededAnnounceHold(t)
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
	// TRIPWIRE: the worker's initial pass launches the parked presentation,
	// and its notification wakes re-drive after; the bound only fires on a
	// genuine hang, not a loaded shard's scheduling delay.
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
	testwait.Until(t, "the stopped presentation turn to settle", func() bool {
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
	testwait.Until(t, "the stopped generation to reach the owner's row gate", func() bool {
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
	// Seed the loaded-runner interleave this test pins: hold the retained
	// child's announce until the fixture has returned. The fixture's send
	// comes back while the delegate's finalization drain is still parked on
	// the child, so the release lands the child's report delivery inside
	// this test's window instead of the fixture's — the shape a loaded
	// runner produces by stretching the parked tails, and the shape whose
	// race dropped CI runs: the scenario's budgets below must start from the
	// row's arrival, not the fixture's return.
	holdSharedDescendantAnnounce("busy-commit-reopen")
	lane := spawnSharedDescendantLane(t, r, "busy-commit-reopen", "dlg_busycommit00001", "034busycommit00001", wedge)
	lane.releaseSeededAnnounceHold(t)
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
	testwait.Until(t, "the delegate's attention turn to finish after release", func() bool {
		lane.delegateSub.mu.Lock()
		running := lane.delegateSub.running
		lane.delegateSub.mu.Unlock()
		return !running
	})
}

// A user's stop of an in-flight attention presentation owns the quiet. The
// settle tail still reopens the consumed report — the row stays pending and
// recoverable through a wake from a new event — a later attention row, a
// restart reconcile or an explicit send — never the armed wake the stopped
// generation's commit left behind. Nothing may re-arm the automatic drive: a
// re-arm would at once start
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
	// Seed the loaded-runner interleave this test pins: hold the retained
	// child's announce until the fixture has returned. The fixture's send
	// comes back while the delegate's finalization drain is still parked on
	// the child, so the release lands the child's report delivery inside
	// this test's window instead of the fixture's — the shape whose race
	// dropped a CI run: the scenario's budgets below must start from the
	// row's arrival, not the fixture's return.
	holdSharedDescendantAnnounce("user-stop-no-rearm")
	lane := spawnSharedDescendantLane(t, r, "user-stop-no-rearm", "dlg_userstop00001", "034userstop00001", wedge)
	lane.delegateSess.SetNotifyFunc(nil)
	// The shared child's restored notify edge can launch its own pending
	// notification turn, which would consume the scripted wedge step this
	// test's presentation parks in; disarm it so the test loop below is the
	// only driver.
	lane.sharedSub.sess.SetNotifyFunc(nil)
	lane.announceRelease()
	// The seeded window's end is the delegate's own finalize tail closing its
	// run: the tail re-arms whatever attention survived the window at its
	// very end, so the wait must cover the re-arm too. The retained child's
	// report becomes a pending row inside this window, and waiting the tail
	// out is what keeps the wait below from racing the row's landing.
	lane.delegateSub.mu.Lock()
	delegateDone := lane.delegateSub.done
	lane.delegateSub.mu.Unlock()
	select {
	case <-delegateDone:
	// TRIPWIRE: the tail finishes within a couple of drain passes of the
	// release; 15s only fires on a genuine hang, absorbing a loaded shard's
	// stretched finalize tails and git retries.
	case <-time.After(15 * time.Second):
		t.Fatalf("the delegate's finalize tail never finished after the seeded announce release")
	}
	if pending, pendErr := lane.delegateSess.pendingDelegateAttentionIDs(); pendErr != nil || len(pending) == 0 {
		t.Fatalf("the retained child's report is not pending after the seeded window: pending=%v (%v)", pending, pendErr)
	}
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

	// The stop owned the quiet, not the loss: the unpresented trigger stays
	// durably queued under its own identity with a hold stamped on it, so a
	// later wake, a restart, or an explicit send can present it without the
	// stopped generation re-running.
	rows := pendingAttentionRowsForTest(t, lane.root.delegateController, lane.fixture.delegateID)
	if len(rows) != 1 || !strings.HasPrefix(rows[0].AttentionID, "delegate:") || !strings.HasSuffix(rows[0].AttentionID, "/delivery/1") || rows[0].HeldThrough == 0 {
		t.Fatalf("the user-stopped run's report was not left pending and held quiet: rows=%#v", rows)
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
	testwait.Until(t, "the silent shell call to start", func() bool {
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
	testwait.Until(t, "the silent tool call to keep the lane owner's evidence fresh", func() bool {
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
	testwait.Until(t, "the coordinator to finish while the descendant works", func() bool {
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
	// The coordinator's own turn has ended, and with it its evidence ticker:
	// only the finalization drain can keep the evidence fresh from here.
	coordinatorSub := root.subagents.get(fixture.childID)
	if coordinatorSub == nil {
		t.Fatalf("the coordinator %s is not retained", fixture.childID)
	}
	testwait.Until(t, "the coordinator's finalization drain to take over", func() bool {
		coordinatorSub.mu.Lock()
		defer coordinatorSub.mu.Unlock()
		return coordinatorSub.finalizing
	})
	// The coordinator sets finalizing before it starts its drain ticker and
	// sets the drain hold. Waiting for the hold orders the backdate below
	// after the drain exists; either the coordinator's or a descendant's
	// ticker can still refresh the root transcript afterwards.
	testwait.Until(t, "the coordinator's finalization drain hold", func() bool {
		return coordinatorSub.sess.drainHeld.Load()
	})

	// The lane's marker parent — the root — has now been idle past the
	// operator's threshold: nothing of its own runs while the tree waits on
	// the descendant.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The finalization drain is active delegate work: its wait must keep
	// the evidence fresh.
	testwait.Until(t, "the drain wait to keep the lane owner's evidence fresh", func() bool {
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
		fmt.Fprintf(&b, "adapter requests=%d cursor=%d: ", len(fixture.adapter.requests), fixture.adapter.cursor())
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
	settleStart := time.Now()
	testwait.UntilSeen(t, "the coordinator to settle after its report", func() (bool, string) {
		delegateSub.mu.Lock()
		running, driving, finalizing := delegateSub.running, delegateSub.driving, delegateSub.finalizing
		delegateSub.mu.Unlock()
		if !running && !driving && !finalizing {
			return true, ""
		}
		if captureDue(t, settleStart) {
			return false, dumpState()
		}
		return false, ""
	})
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
	testwait.Until(t, "the descendant to settle after its quick turn", func() bool {
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
	// retryable — the timer re-fires — so the wait re-drives until the
	// woken turn's shell appears. The woken turn blocks inside a silent
	// foreground shell, its activity the tree's only life now.
	shellStart := time.Now()
	testwait.UntilSeen(t, "the woken descendant's shell to start", func() (bool, string) {
		root.drivePendingDelegateAttention()
		if _, err := os.Stat(enteredB); err == nil {
			return true, ""
		}
		if !captureDue(t, shellStart) {
			return false, ""
		}
		descendantSub.mu.Lock()
		dRunning, dDriving, dFinalizing, dClosed := descendantSub.running, descendantSub.driving, descendantSub.finalizing, descendantSub.closed
		descendantSub.mu.Unlock()
		fixture.adapter.mu.Lock()
		requestCount, cursor := len(fixture.adapter.requests), fixture.adapter.cursor()
		fixture.adapter.mu.Unlock()
		_, subLocked, subReason := r.laneLocked(t, lanePath)
		return false, fmt.Sprintf("sub running=%v driving=%v finalizing=%v closed=%v, adapter requests=%d cursor=%d, coordinator lane locked=%v reason=%q",
			dRunning, dDriving, dFinalizing, dClosed, requestCount, cursor, subLocked, subReason)
	})

	// The launch already recorded this wake in the ancestors' evidence;
	// the silence it must outlast is the rest of the turn, so the ongoing
	// refresh alone must keep every isolated ancestor fresh from here on.
	stale = time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript again: %v", err)
	}

	// The woken descendant's activity is the tree's life now: it must keep
	// the settled coordinator's lane evidence fresh.
	testwait.Until(t, "the woken descendant to keep the coordinator's lane evidence fresh", func() bool {
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
	testwait.Until(t, "the woken turn to finish after release", func() bool {
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
	// launch for a reason that clears on its own. The wait re-drives until
	// the launch takes: the outcome assertions below cannot be satisfied
	// without one, and a decline that never clears fails with the state that
	// caused it. The lane-validation probe is not replayed for that report:
	// it refreshes the parent's evidence, the very signal asserted below.
	launchStart := time.Now()
	testwait.UntilSeen(t, "the shared child's notification drive to launch", func() (bool, string) {
		lane.fixture.adapter.setSteps([]func(llm.Request) llm.Response{lane.finish})
		if lane.delegateSess.driveSubagentNotificationTurn(lane.sharedSub) {
			return true, ""
		}
		if !captureDue(t, launchStart) {
			return false, ""
		}
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
		driveSlot, driveSlotOK := lane.delegateSess.reserveDriveSlot()
		if driveSlotOK {
			driveSlot.release()
		}
		lane.delegateSess.mu.Lock()
		driverClosing := lane.delegateSess.closingOrClosedLocked()
		lane.delegateSess.mu.Unlock()
		return false, fmt.Sprintf("lane locked=%v reason=%q, sub closed=%v running=%v driving=%v; retirement mutation=%v, drive slot=%v, driver closing=%v",
			locked, reason, closed, running, driving, mutErr, driveSlotOK, driverClosing)
	})

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
	requireSessionLivelock(t)
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
	settleSendDelivery(t, root, first)
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
	testwait.Until(t, "the wake's drive turn to finish", func() bool {
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
	testwait.Until(t, "the settled background job to keep the lane owner's evidence fresh", func() bool {
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
	report := func(llm.Request) llm.Response {
		return quietYieldResponse("refused-drive-report", `{"message":"done"}`)
	}
	fixture, root, lanePath := spawnRetainedDelegateLane(t, r, "refused-drive-lane", "dlg_orphanactive000005", "034orphanactive0005", bgCall, report)

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
	testwait.Until(t, "the background job to start and its turn to finish", func() bool {
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
	testwait.Until(t, "the finished job to arm its completion attention", func() bool {
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
	// aged past the floor, is refreshed on the next tick — never older than
	// the cadence allows. The child's evidence ticker runs on a fake clock,
	// so only one tick interval of fake time passes before the refresh.
	r := newWorktreeRepo(t)
	fixture, root, _ := spawnRetainedDelegateLane(t, r, "cadence-lane", "dlg_orphanactive000007", "034orphanactive0007")
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained", fixture.childID)
	}
	child := sub.sess
	// The clock is swapped only once the child's run and its detached namer
	// have settled, so nothing reads it concurrently.
	waitForStableSupervisionRun(t, root, fixture.childID)
	clk := agenttest.NewFakeClockAt(time.Now())
	child.clock = clk
	child.managedJobStarted()
	defer child.managedJobFinished()
	clk.BlockUntil(1)
	transcript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	aged := time.Now().Add(-(activityEvidenceRefreshFloor + 2*delegateActivityPublishInterval))
	if err := os.Chtimes(transcript, aged, aged); err != nil {
		t.Fatalf("age the parent's transcript past the floor: %v", err)
	}
	clk.Advance(delegateActivityPublishInterval)
	testwait.Until(t, "evidence aged past the floor to be refreshed by the next tick", func() bool {
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
