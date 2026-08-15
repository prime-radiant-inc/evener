package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/worktree"
)

func TestStableDelegateWorktree_LiveGuardUsesStableDelegateState(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	aggregate := delegateAggregateSnapshot(t, r.s.delegateController, id)

	handles := r.s.liveWorkUnder(lanePath)
	wantHandle := aggregate.Descriptor.ChildSessionID + subagentRetainedIdleLabel
	if !reflect.DeepEqual(handles, []string{wantHandle}) {
		t.Fatalf("stable live-work handles = %v, want [%q] without a delegate JobRecord; aggregate=%#v", handles, wantHandle, aggregate)
	}
	ids, ok := r.s.retainedIdleDelegateIDs(handles)
	if !ok || !reflect.DeepEqual(ids, []string{id}) {
		t.Fatalf("stable retained-idle resolution = (%v, %t), want ([%s], true)", ids, ok, id)
	}
}

func TestStableDelegateWorktree_RootCloseCleansEligibleScratch(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)

	r.s.Close()

	if laneWorktreePresent(lanePath) {
		t.Fatalf("root close retained eligible stable isolation lane %s", lanePath)
	}
	aggregate := delegateAggregateSnapshot(t, r.s.delegateController, id)
	if aggregate.Resumable || aggregate.Phase != delegatestore.PhaseClosed || aggregate.NotResumableReason != stableWorktreeDisposalReason {
		t.Fatalf("root-close aggregate = %#v, want closed with %q", aggregate, stableWorktreeDisposalReason)
	}
}

func TestStableDelegateWorktree_ExplicitDisposalPreservesDirtyAndD0Checks(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		r := newWorktreeRepo(t)
		id, lanePath, _ := r.seedStableIsolationLane(t)
		if err := os.WriteFile(filepath.Join(lanePath, "dirty"), []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
			t.Fatalf("dirty stable disposal error = %v, want dirty refusal", err)
		}
		assertStableWorktreeResumable(t, r.s.delegateController, id, true)
		if !laneWorktreePresent(lanePath) {
			t.Fatal("dirty refusal destroyed stable lane")
		}
	})

	t.Run("unmerged", func(t *testing.T) {
		r := newWorktreeRepo(t)
		id, lanePath, _ := r.seedStableIsolationLane(t)
		commitStableLaneChange(t, lanePath)

		if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err == nil || !strings.Contains(err.Error(), "unmerged commit") {
			t.Fatalf("unmerged stable disposal error = %v, want D0 refusal", err)
		}
		assertStableWorktreeResumable(t, r.s.delegateController, id, true)
		if !laneWorktreePresent(lanePath) {
			t.Fatal("D0 refusal destroyed stable lane")
		}
	})
}

func TestStableDelegateWorktree_ForcePreservesLockProvenanceAndEvidence(t *testing.T) {
	t.Run("foreign lock still refuses", func(t *testing.T) {
		r := newWorktreeRepo(t)
		id, lanePath, _ := r.seedStableIsolationLane(t)
		wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
		wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "foreign-owner", lanePath)

		if _, err := r.s.worktreeDispose(context.Background(), id, true, true); err == nil || !strings.Contains(err.Error(), "locked by another owner") {
			t.Fatalf("forced foreign-lock disposal error = %v, want provenance refusal", err)
		}
		assertStableWorktreeResumable(t, r.s.delegateController, id, true)
		if !laneWorktreePresent(lanePath) {
			t.Fatal("force bypassed foreign lock provenance")
		}
	})

	t.Run("owned changed lane closes with evidence", func(t *testing.T) {
		r := newWorktreeRepo(t)
		id, lanePath, _ := r.seedStableIsolationLane(t)
		commitStableLaneChange(t, lanePath)

		result, err := r.s.worktreeDispose(context.Background(), id, true, false)
		if err != nil {
			t.Fatalf("forced stable disposal: %v", err)
		}
		if result.DelegateID != id || result.LanePath != lanePath || result.Branch != id {
			t.Fatalf("forced stable disposal evidence = %#v", result)
		}
		if laneWorktreePresent(lanePath) {
			t.Fatal("forced owned stable lane was not removed")
		}
		assertStableWorktreeResumable(t, r.s.delegateController, id, false)
	})
}

func TestStableDelegateWorktree_ResumabilityClosureFsyncPrecedesDestruction(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	closureVisible := false
	r.s.worktreeDisposeBeforeRemove = func(string) {
		events, err := r.s.delegateController.store.Load()
		if err != nil {
			t.Fatalf("load stable store at destruction boundary: %v", err)
		}
		for _, event := range events {
			if event.DelegateID == id && event.ResumabilityClosed != nil && event.ResumabilityClosed.Reason == stableWorktreeDisposalReason {
				closureVisible = true
			}
		}
	}

	if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err != nil {
		t.Fatalf("stable disposal: %v", err)
	}
	if !closureVisible {
		t.Fatal("worktree destruction began before the resumability closure was durably readable")
	}
	if laneWorktreePresent(lanePath) {
		t.Fatal("stable disposal left collectible lane")
	}
}

func TestStableDelegateWorktree_ClosureAppendFailureDestroysNothing(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	before := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	if err := r.s.delegateController.store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err == nil || !strings.Contains(err.Error(), "store is closed") {
		t.Fatalf("closure append failure = %v, want durable-store error", err)
	}
	if after := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain"); after != before {
		t.Fatalf("append failure changed worktree registry:\n before %q\n after  %q", before, after)
	}
	assertStableWorktreeResumable(t, r.s.delegateController, id, true)
	if !laneWorktreePresent(lanePath) || !r.branchExists(t, id) {
		t.Fatal("append failure destroyed the lane or branch")
	}
}

func TestStableDelegateWorktree_CleanupFailureReportsRetainedResidueWithoutReopen(t *testing.T) {
	r := newWorktreeRepo(t)
	id, lanePath, _ := r.seedStableIsolationLane(t)
	r.s.worktreeDisposeBeforeRemove = func(path string) {
		if err := os.WriteFile(filepath.Join(path, "late-dirty"), []byte("retain"), 0o600); err != nil {
			t.Fatalf("inject late dirty write: %v", err)
		}
	}

	result, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("late cleanup failure: %v", err)
	}
	if !strings.Contains(result.Message, "retained residue") {
		t.Fatalf("cleanup failure result = %#v, want retained-residue evidence", result)
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("late dirty residue was destroyed")
	}
	aggregate := delegateAggregateSnapshot(t, r.s.delegateController, id)
	if aggregate.Resumable || aggregate.NotResumableReason != stableWorktreeDisposalReason {
		t.Fatalf("physical cleanup failure reopened stable aggregate: %#v", aggregate)
	}
	if err := worktree.DeleteSidecar(metaDirForLane(lanePath), id); err != nil {
		t.Fatalf("remove retained sidecar: %v", err)
	}
	retry, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil {
		t.Fatalf("retry after sidecar loss: %v", err)
	}
	if !retry.AlreadyDisposed || !strings.Contains(retry.Message, "retained residue") {
		t.Fatalf("retry after sidecar loss = %#v, want honest retained-residue evidence", retry)
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("retry after sidecar loss destroyed retained residue")
	}
}

func TestStableDelegateWorktree_DisposalAndRestartAreIdempotent(t *testing.T) {
	r := newWorktreeRepo(t)
	id, _, _ := r.seedStableIsolationLane(t)

	if _, err := r.s.worktreeDispose(context.Background(), id, false, false); err != nil {
		t.Fatalf("first disposal: %v", err)
	}
	second, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil || !second.AlreadyDisposed {
		t.Fatalf("same-process repeat = (%#v, %v), want already disposed", second, err)
	}

	events, err := r.s.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restarted := reopenStableWorktreeController(t, r.s, events)
	r.s.delegateController = restarted
	third, err := r.s.worktreeDispose(context.Background(), id, false, false)
	if err != nil || !third.AlreadyDisposed {
		t.Fatalf("restart repeat = (%#v, %v), want already disposed", third, err)
	}
}

func TestStableDelegateWorktree_SandboxRestoreUsesDescriptorNotLegacyJob(t *testing.T) {
	descriptorDir := t.TempDir()
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.WorkingDir = descriptorDir
		descriptor.LocalEnvPolicy = "default"
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
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
		t.Fatalf("stable descriptor restore without delegate JobRecord: %v", err)
	}
	if !restored {
		t.Fatal("stable descriptor restore unexpectedly reused a resident child")
	}
	defer func() {
		sub.sess.discardRestoredCandidate()
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()
	if got := sub.sess.currentEnv().WorkingDirectory(); got != descriptorDir {
		t.Fatalf("restored working directory = %q, want stable descriptor %q", got, descriptorDir)
	}
}

func (r *wtRepo) seedStableIsolationLane(t *testing.T) (delegateID, lanePath, baseSHA string) {
	t.Helper()
	delegateID = r.s.delegateController.newDelegateID()
	lanePath, _, baseSHA, _, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("create stable delegate worktree: %v", err)
	}
	descriptor := delegatestore.Descriptor{
		ChildSessionID:   "child-" + delegateID,
		TranscriptRef:    encodeRef("", "child-"+delegateID),
		OwnerSessionID:   r.s.id,
		VisibleSessionID: r.s.id,
		Task:             "stable worktree fixture",
		AgentType:        "default",
		ToolNameCeiling:  []string{"communicate"},
		WorkingDir:       lanePath,
		LocalEnvPolicy:   "default",
		Isolation:        "worktree",
		Resumable:        true,
	}
	lease := delegateLease{delegateID: delegateID, generation: 1}
	r.s.delegateController.mu.Lock()
	_, err = r.s.delegateController.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: delegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		},
		delegateControllerRunStartedEvent(delegateID, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
	)
	if err == nil {
		r.s.delegateController.live[delegateID] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease:  lease,
			cancel: func() {},
			ready:  true,
		}}
		r.s.delegateController.turnsInUse++
	}
	r.s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatalf("seed stable delegate: %v", err)
	}
	plans, err := r.s.delegateController.FinishGeneration(lease, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "fixture complete",
		endedAt: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("finish stable delegate: %v", err)
	}
	for _, plan := range plans.deliveries {
		token, admitted, deliveryErr := r.s.delegateController.BeginDelivery(plan)
		if deliveryErr != nil || !admitted {
			t.Fatalf("begin fixture delivery: admitted=%t err=%v", admitted, deliveryErr)
		}
		if _, deliveryErr := r.s.delegateController.CompleteDelivery(token, true); deliveryErr != nil {
			t.Fatalf("acknowledge fixture delivery: %v", deliveryErr)
		}
	}
	return delegateID, lanePath, baseSHA
}

func assertStableWorktreeResumable(t *testing.T, controller *delegateTreeController, id string, want bool) {
	t.Helper()
	aggregate := delegateAggregateSnapshot(t, controller, id)
	if aggregate.Resumable != want {
		t.Fatalf("delegate %s resumable = %t, want %t (reason %q)", id, aggregate.Resumable, want, aggregate.NotResumableReason)
	}
	if !want && aggregate.NotResumableReason != stableWorktreeDisposalReason {
		t.Fatalf("delegate %s close reason = %q, want %q", id, aggregate.NotResumableReason, stableWorktreeDisposalReason)
	}
}

func commitStableLaneChange(t *testing.T, lanePath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(lanePath, "change"), []byte("committed change"), 0o600); err != nil {
		t.Fatal(err)
	}
	wtGit(t, lanePath, "add", "change")
	wtGit(t, lanePath, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "lane change")
}

func reopenStableWorktreeController(t *testing.T, root *Session, events []delegatestore.Event) *delegateTreeController {
	t.Helper()
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].Seq = 0
	}
	if _, _, err := store.AppendBatch(state, events); err != nil {
		t.Fatal(err)
	}
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootRuntime:   root,
		rootSessionID: root.id,
		stateDir:      root.stateDir,
		worktreeRoot:  filepath.Join(root.stateDir, "worktrees"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}
