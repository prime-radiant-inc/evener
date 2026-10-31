package agent

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	if sub := root.subagents.get(fixture.childID); sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The end state the orphan arm and the manual unlock leave.
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)

	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
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
	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
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

	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
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
	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
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

	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
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
	delegateSess     *Session // the lane-owning delegate's runtime, which drives the child's wake
	sharedSub        *subagent
	sharedDelegateID string
	finish           func(llm.Request) llm.Response
}

// spawnSharedDescendantLane builds that tree: a cold fixture delegate with an
// isolated lane, restored and sent once through a turn that spawns exactly
// one non-isolated child sharing the lane and then finishes, leaving both the
// delegate and its child retained. The fixture lives in the repo harness's
// state dir, so a harness-session prune reads the same parent evidence the
// orphan arm reads. notificationTurn, when given, scripts the retained
// child's next notification turn up front — the fake adapter serves its steps
// by a cursor that survives reassignment, so a step appended after the cursor
// would never be served.
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
	// delegate's follow-up turn finishes, and the retained child's next
	// notification turn answers the scripted step (or the adapter's bare-text
	// fallback when none is given).
	fixture.adapter.steps = []func(llm.Request) llm.Response{spawn, finish, finish, notify}
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

	lane.fixture.adapter.steps = []func(llm.Request) llm.Response{lane.finish}
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
func spawnRetainedDelegateLane(t *testing.T, r *wtRepo, laneName, standInDlg, standInSID string, secondTurn func(llm.Request) llm.Response) (coldDelegateFixture, *Session, string) {
	t.Helper()
	var lanePath string
	fixture := newColdDelegateFixtureConfiguredInState(t, "", r.stateDir, func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		d.ToolNameCeiling = []string{"communicate", "shell"}
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
	// Both turns are scripted up front: the fake adapter serves its steps by
	// a cursor that survives reassignment, so a later slice replacement after
	// the cursor would never be served.
	// A tool-calling second turn answers its result with a final response, so
	// the trailing step covers the follow-up round; a second turn that ends on
	// its own response never consumes it.
	fixture.adapter.steps = []func(llm.Request) llm.Response{finish, secondTurn, finish}
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
	case <-time.After(10 * time.Second):
		t.Fatal("the long turn never finished after release")
	}
}

// A retained child's notification turn runs without opening a generation, so
// its ongoing activity reports arrive on the finished generation's lease and
// ReportActivityPhase rejects them — the rejection is the fence that keeps a
// settled generation's clocks untouched, and it stands. The report is still
// the turn's own activity: the marker parent's evidence must refresh through
// it anyway, or a foreign prune — whose live-work view cannot see the turn —
// judges the tree inactive past the operator's threshold and collects the
// clean, unchanged checkout mid-notification.
func TestWorktreePrune_OrphanArmSparesLaneWhileRetainedChildNotifies(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var wedgeOnce sync.Once
	wedge := func(llm.Request) llm.Response {
		wedgeOnce.Do(func() { close(entered) })
		<-release
		return agenttest.FinalResponse("done")
	}
	lane := spawnSharedDescendantLane(t, r, "active-notify-lane", "dlg_waketest0000008", "034placeholder000000012", wedge)
	parentTranscript := schema.SessionTranscriptPath(lane.fixture.stateDir, lane.fixture.meta.ID)

	// The retained child's notification turn wedges inside its first model
	// call: the tree is alive, the child runs a turn, and the lane's marker
	// parent stays idle.
	driveDone := make(chan struct{})
	go func() {
		defer close(driveDone)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if lane.delegateSess.driveSubagentNotificationTurn(lane.sharedSub) {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	select {
	case <-entered:
	case <-time.After(12 * time.Second):
		t.Fatal("the retained child's notification turn never started")
	}

	// The parent has now been idle past the operator's threshold: the wake
	// refreshed its evidence once at launch, and nothing since.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}

	// The running turn reports activity through the closure bound to its
	// finished generation — the retained child's notification turns open no
	// generation of their own — and the report is rejected as a stale lease.
	// The rejection is not the whole story: the turn is real activity.
	lane.sharedSub.sess.mu.Lock()
	activity := lane.sharedSub.sess.cfg.spawn.parentJobActivity
	lane.sharedSub.sess.mu.Unlock()
	if activity == nil {
		t.Fatal("the retained child carries no parent activity binding")
	}
	activity(lane.sharedDelegateID, jobPhaseToolRunning)

	// The rejected report is the parent's activity evidence now.
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

	// The notification turn finishes cleanly once released.
	close(release)
	select {
	case <-driveDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the notification turn never finished after release")
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
	case <-time.After(10 * time.Second):
		t.Fatal("the silent turn never finished after release")
	}
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
		lane.fixture.adapter.steps = []func(llm.Request) llm.Response{lane.finish}
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

// A child-only wake is still parent activity. A notification drive runs the
// child's turn on the parent's own machinery and never touches the parent's
// transcript — the child's session writes the turn, and the parent runs no
// turn of its own — so the inactivity evidence the arm reads stays stale
// while the parent is demonstrably alive. An automatic launch therefore
// records its activity in that evidence before it validates the lane: a real
// notification drive interleaved between the prune's eligibility check and
// its lock claim leaves the release-time recheck seeing a live parent, and
// the lane is spared with the running child's lock intact.
func TestWorktreePrune_OrphanArmSparesLaneWhenWakeInterleavesRelease(t *testing.T) {
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
	fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
	first := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "first turn", 10_000)
	if first.result.Err != nil {
		t.Fatalf("first send: %v", first.result.Err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("delegate %s is not retained after its run", fixture.childID)
	}
	// The parent session is long-idle: its transcript has not been touched
	// since the fixture created it, which is what the orphan arm reads.
	parentTranscript := schema.SessionTranscriptPath(fixture.stateDir, fixture.meta.ID)
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(parentTranscript, stale, stale); err != nil {
		t.Fatalf("backdate the parent's transcript: %v", err)
	}
	// The wake interleaves with the release: the seam fires after the
	// eligibility plan, before the claim, and a real notification drive runs
	// there — the child's turn, launched by the parent's own machinery, with
	// no parent turn to touch the transcript.
	driveLaunched := make(chan bool)
	wakeLaunched := false
	r.s.worktreeBeforeStrandedMarkerRelease = func(p string) {
		if p != lanePath {
			return
		}
		go func() {
			fixture.adapter.steps = []func(llm.Request) llm.Response{finish}
			driveLaunched <- root.driveSubagentNotificationTurn(sub)
		}()
		wakeLaunched = <-driveLaunched
	}

	out, err := r.pruneOrphanedOp(t, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !wakeLaunched {
		t.Fatalf("the notification wake never launched its turn: %+v", out)
	}
	if findPruneEntry(t, pruneEntries(t, out, "removed"), "orphan-wake-interleaved") != nil {
		t.Fatalf("a lane with a running child was collected: %+v", out)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "unlocked"), "orphan-wake-interleaved"); e != nil {
		t.Fatalf("a lane with a running child was reported unlocked: %+v", e)
	}
	if _, locked, reason := r.laneLocked(t, lanePath); !locked {
		t.Errorf("the running child's lock was not restored: %q", reason)
	}
}
