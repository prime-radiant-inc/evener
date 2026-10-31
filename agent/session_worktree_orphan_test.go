package agent

import (
	"context"
	"encoding/json"
	"os"
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
	fixture      coldDelegateFixture
	lanePath     string
	delegateSess *Session // the lane-owning delegate's runtime, which drives the child's wake
	sharedSub    *subagent
	finish       func(llm.Request) llm.Response
}

// spawnSharedDescendantLane builds that tree: a cold fixture delegate with an
// isolated lane, restored and sent once through a turn that spawns exactly
// one non-isolated child sharing the lane and then finishes, leaving both the
// delegate and its child retained.
func spawnSharedDescendantLane(t *testing.T, r *wtRepo, laneName, standInDlg, standInSID string) *sharedDescendantLane {
	t.Helper()
	var lanePath string
	fixture := newColdDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Isolation = "worktree"
		d.ToolNameCeiling = []string{"communicate", "delegate"}
		d.DelegationAllowance = 1
		lanePath = r.delegateLane(t, laneName, standInDlg, standInSID)
		d.WorkingDir = lanePath
	})
	// The idle-release machinery claims settled subtree leaves — this tree's
	// shared child included — on a schedule the test cannot see; a claim in
	// flight when the wake runs declines the drive, so the tree the lane
	// contract is pinned on must not release on its own.
	root, err := restoreDelegateResourceBootstrapSessionWithTestConfig(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir, testConfig{
		sandboxProber:              bwrapCapableProber(fixture.workspace),
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
	// The delegate's turn spawns its child, the child's turn finishes, and the
	// delegate's follow-up turn finishes.
	fixture.adapter.steps = []func(llm.Request) llm.Response{spawn, finish, finish}
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
	var sharedChildID string
	shared := 0
	tree := root.delegateController
	tree.mu.Lock()
	for _, agg := range tree.durable {
		if agg.Descriptor.ParentDelegateID == fixture.delegateID {
			shared++
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
		fixture:      fixture,
		lanePath:     lanePath,
		delegateSess: delegateSub.sess,
		sharedSub:    sharedSub,
		finish:       finish,
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
	wtGit(t, r.mainRoot, "worktree", "unlock", lane.lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", worktree.FormatSessionMarker(foreignSID), lane.lanePath)

	lane.fixture.adapter.steps = []func(llm.Request) llm.Response{lane.finish}
	if lane.delegateSess.driveSubagentNotificationTurn(lane.sharedSub) {
		t.Fatal("shared descendant's wake launched into a lane held by another session")
	}

	_, locked, reason := r.laneLocked(t, lane.lanePath)
	if !locked {
		t.Fatal("the foreign lock was displaced")
	}
	if m, ok := worktree.ParseMarker(reason); !ok || m.SessionID != foreignSID {
		t.Fatalf("foreign lock = %q, want the other session's marker", reason)
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
		t.Fatalf("notification drive skipped the idle shared child (lane locked=%v reason=%q, sub closed=%v running=%v driving=%v)",
			locked, reason, closed, running, driving)
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
