package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// seedNestedIdleDelegate seeds an idle, resumable delegate under parentID.
// workingDir is the parent's lane for a non-isolated child, which is the shape
// that shares its parent's lane.
func seedNestedIdleDelegate(t *testing.T, c *delegateTreeController, id, parentID, workingDir string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateCreated,
		DelegateID: id,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   "child-" + id,
			TranscriptRef:    encodeRef("", "child-"+id),
			ParentDelegateID: parentID,
			OwnerSessionID:   c.rootSessionID,
			Task:             "nested delegate fixture",
			AgentType:        "default",
			ToolNameCeiling:  []string{"communicate"},
			WorkingDir:       workingDir,
			LocalEnvPolicy:   "default",
			Resumable:        true,
		}},
	})
	if err != nil {
		t.Fatalf("seed nested delegate %s: %v", id, err)
	}
}

// assertBusyReservation reports a refused start admission and releases a
// reservation that unexpectedly succeeded. Coverage is unchanged: every caller
// still asserts errDelegateTargetBusy.
func assertBusyReservation(t *testing.T, c *delegateTreeController, reservation *delegateStartReservation, err error, what string) {
	t.Helper()
	if reservation != nil {
		c.AbortStart(reservation)
	}
	if !errors.Is(err, errDelegateTargetBusy) {
		t.Errorf("%s = %v, want errDelegateTargetBusy", what, err)
	}
}

// TestDelegateLaneHandoffFencesNestedRestore is the issue #2067 race. The
// parent's isolation lane is being handed off by a real dispose; a nested
// delegate that shares that lane tries to commit and begin installing its cold
// attention restore while the dispose is inside its lane evaluation, after the
// point-in-time quiescence reads. Before the subtree fence both admissions
// succeeded; the lane was then unlocked and removed under the installing
// runtime.
func TestDelegateLaneHandoffFencesNestedRestore(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	parentID, lanePath, _ := r.seedStableIsolationLane(t)
	const nestedID = "dlg_nested_handoff_restore"
	seedNestedIdleDelegate(t, r.s.delegateController, nestedID, parentID, lanePath)

	seamArrived := make(chan struct{})
	seamRelease := make(chan struct{})
	var once sync.Once
	scriptedGitRunner(r.s, func(args []string) (string, error, bool) {
		if scriptedArgs(args, statusArgv(lanePath)...) {
			once.Do(func() {
				close(seamArrived)
				<-seamRelease
			})
		}
		return "", nil, false
	})

	disposeDone := make(chan error, 1)
	go func() {
		_, err := r.s.worktreeDispose(context.Background(), parentID, false, false)
		disposeDone <- err
	}()
	waitForTestSignal(t, seamArrived, "the dispose lane-evaluation seam")

	commit, _, commitErr := r.s.delegateController.idleDelegateRestoreCommit(nestedID)
	installation, installErr := r.s.delegateController.beginIdleRuntimeInstallation(nestedID)
	if installation != nil {
		installation.release()
	}
	_, _, restoreErr := r.s.restoreColdDelegateAttentionRuntime(nestedID)

	close(seamRelease)
	var disposeErr error
	select {
	case disposeErr = <-disposeDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("dispose did not return after the lane-evaluation seam released")
	}

	if !errors.Is(commitErr, errDelegateTargetBusy) {
		t.Errorf("nested restore commit during its parent lane handoff = %v, want errDelegateTargetBusy (commit %+v)", commitErr, commit.lease)
	}
	if !errors.Is(installErr, errDelegateTargetBusy) {
		t.Errorf("nested runtime installation admission during its parent lane handoff = %v, want errDelegateTargetBusy", installErr)
	}
	if !errors.Is(restoreErr, errDelegateTargetBusy) {
		t.Errorf("nested cold attention restore during its parent lane handoff = %v, want errDelegateTargetBusy", restoreErr)
	}
	if disposeErr != nil {
		t.Fatalf("dispose after the fenced restore attempts: %v", disposeErr)
	}
	if laneWorktreePresent(lanePath) {
		t.Error("lane still present after dispose")
	}
	if !r.stableDisposalClosurePresent(t, parentID) {
		t.Error("parent resumability not closed by the completed handoff")
	}
}

// TestDelegateLaneHandoffReleasedAfterAbortedHandoff is the control: a handoff
// that refuses before destroying anything releases the fence, so a legitimate
// descendant cold restore is admitted afterwards and the lane is untouched.
func TestDelegateLaneHandoffReleasedAfterAbortedHandoff(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	parentID, lanePath, _ := r.seedStableIsolationLane(t)
	const nestedID = "dlg_nested_handoff_control"
	seedNestedIdleDelegate(t, r.s.delegateController, nestedID, parentID, lanePath)

	if err := os.WriteFile(filepath.Join(lanePath, "uncommitted"), []byte("keep me"), 0o600); err != nil {
		t.Fatalf("make lane dirty: %v", err)
	}
	if _, err := r.s.worktreeDispose(context.Background(), parentID, false, false); err == nil {
		t.Fatal("dirty lane dispose succeeded, want the refusal that aborts the handoff")
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("aborted handoff removed the lane")
	}
	commit, _, err := r.s.delegateController.idleDelegateRestoreCommit(nestedID)
	if err != nil {
		t.Fatalf("descendant restore after an aborted handoff = %v, want success", err)
	}
	if commit.lease.delegateID != nestedID {
		t.Fatalf("restore commit delegate = %q, want %q", commit.lease.delegateID, nestedID)
	}
	installation, err := r.s.delegateController.beginIdleRuntimeInstallation(nestedID)
	if err != nil {
		t.Fatalf("descendant installation admission after an aborted handoff = %v, want success", err)
	}
	installation.release()
}

// TestDelegateLaneHandoffFenceAdmissions covers the admission points that can
// put a delegate into the handed-off lane, and proves the fence is scoped to
// the subtree rather than the whole controller.
func TestDelegateLaneHandoffFenceAdmissions(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const (
		parentID  = "dlg_handoff_parent"
		nestedID  = "dlg_handoff_nested"
		siblingID = "dlg_handoff_sibling"
	)
	seedDelegateControllerIdle(t, c, parentID, "")
	seedDelegateControllerIdle(t, c, nestedID, parentID)
	seedDelegateControllerIdle(t, c, siblingID, "")

	if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
		t.Fatal("clean idle subtree refused its lane handoff")
	}
	defer c.endSubtreeLaneHandoff(parentID)

	if !c.laneHandoffCoversLocked(parentID) || !c.laneHandoffCoversLocked(nestedID) {
		t.Fatal("handoff does not cover the target and its descendant")
	}
	if c.laneHandoffCoversLocked(siblingID) {
		t.Fatal("handoff covers a delegate outside the subtree")
	}

	if _, _, err := c.idleDelegateRestoreCommit(nestedID); !errors.Is(err, errDelegateTargetBusy) {
		t.Errorf("nested cold restore commit under the handoff = %v, want errDelegateTargetBusy", err)
	}
	if installation, err := c.beginIdleRuntimeInstallation(nestedID); !errors.Is(err, errDelegateTargetBusy) {
		if installation != nil {
			installation.release()
		}
		t.Errorf("nested runtime installation under the handoff = %v, want errDelegateTargetBusy", err)
	}
	if err := c.beginLaneRestore(nestedID); !errors.Is(err, errDelegateTargetBusy) {
		t.Errorf("nested restore window under the handoff = %v, want errDelegateTargetBusy", err)
	}
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), parentID)
	assertBusyReservation(t, c, reservation, err, "target start under the handoff")
	reservation, err = c.reserveOwedAttentionStart(nestedID, "attention-sentinel", 1, nil)
	assertBusyReservation(t, c, reservation, err, "nested owed attention start under the handoff")

	if _, _, err := c.idleDelegateRestoreCommit(siblingID); err != nil {
		t.Errorf("sibling restore commit under another subtree's handoff = %v, want success", err)
	}

	c.endSubtreeLaneHandoff(parentID)
	if commit, _, err := c.idleDelegateRestoreCommit(nestedID); err != nil {
		t.Errorf("nested restore after the handoff released = %v, want success", err)
	} else if commit.lease.delegateID != nestedID {
		t.Errorf("nested restore after release = %q, want %q", commit.lease.delegateID, nestedID)
	}
}

// TestDelegateLaneHandoffRefusesWhileRestoreInFlight covers the other ordering:
// a cold restore window that opened before the handoff makes the handoff refuse,
// so no lane is torn down under an installation that is already running.
func TestDelegateLaneHandoffRefusesWhileRestoreInFlight(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const (
		parentID = "dlg_restore_parent"
		nestedID = "dlg_restore_nested"
	)
	seedDelegateControllerIdle(t, c, parentID, "")
	seedDelegateControllerIdle(t, c, nestedID, parentID)

	if err := c.beginLaneRestore(nestedID); err != nil {
		t.Fatalf("begin restore window: %v", err)
	}
	if admission := c.beginSubtreeLaneHandoff(parentID); admission != laneHandoffBusy {
		if admission == laneHandoffAcquired {
			c.endSubtreeLaneHandoff(parentID)
		}
		t.Fatal("handoff admitted while a descendant restore window was open")
	}
	c.endLaneRestore(nestedID)
	if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
		t.Fatal("handoff refused after the restore window closed")
	}
	c.endSubtreeLaneHandoff(parentID)
}

// TestDelegateLaneHandoffRefusesWhileAttentionRestoreHeld covers the gap after
// a cold restore returns and before its wake pass takes the attention
// reservation: the hold owns the installed runtime there, so the handoff must
// refuse until the pass releases it.
func TestDelegateLaneHandoffRefusesWhileAttentionRestoreHeld(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const (
		parentID = "dlg_hold_parent"
		nestedID = "dlg_hold_nested"
	)
	seedDelegateControllerIdle(t, c, parentID, "")
	seedDelegateControllerIdle(t, c, nestedID, parentID)

	c.holdAttentionRestore(nestedID)
	if admission := c.beginSubtreeLaneHandoff(parentID); admission != laneHandoffBusy {
		if admission == laneHandoffAcquired {
			c.endSubtreeLaneHandoff(parentID)
		}
		t.Fatal("handoff admitted while a wake pass held the nested runtime")
	}
	c.releaseAttentionRestoreHold(nestedID)
	if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
		t.Fatal("handoff refused after the attention restore hold released")
	}
	c.endSubtreeLaneHandoff(parentID)
}

// TestDelegateLaneHandoffRefusesInstallationAttach proves the attach step is
// fenced too, including when the installation admission was taken before the
// handoff began (the span a production caller covers with its restore window).
func TestDelegateLaneHandoffRefusesInstallationAttach(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const (
		parentID = "dlg_attach_parent"
		nestedID = "dlg_attach_nested"
	)
	seedDelegateControllerIdle(t, c, parentID, "")
	seedDelegateControllerIdle(t, c, nestedID, parentID)

	installation, err := c.beginIdleRuntimeInstallation(nestedID)
	if err != nil {
		t.Fatalf("begin idle installation before the handoff: %v", err)
	}
	defer installation.release()
	if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
		t.Fatal("handoff refused despite no tracked subtree work")
	}
	defer c.endSubtreeLaneHandoff(parentID)
	runtime := &Session{id: "child-" + nestedID, owningDelegateID: nestedID, delegateController: c}
	if err := installation.attach(runtime); !errors.Is(err, errDelegateTargetBusy) {
		t.Errorf("attach during the handoff = %v, want errDelegateTargetBusy", err)
	}
}

// TestDelegateLaneHandoffRefusesWhileColdRestoreInFlight drives the real cold
// restore entry point. The seam holds it after its commit and before its owner
// chain is restored; dispose must refuse and leave the lane intact because the
// restore window is open. Releasing the restore makes the retry succeed.
func TestDelegateLaneHandoffRefusesWhileColdRestoreInFlight(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	parentID, lanePath, _ := r.seedStableIsolationLane(t)
	const nestedID = "dlg_nested_restore_inflight"
	seedNestedIdleDelegate(t, r.s.delegateController, nestedID, parentID, lanePath)

	afterCommit := make(chan struct{})
	releaseRestore := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRestore) }) }
	t.Cleanup(release)
	r.s.delegateRestoreAfterCommit = func(delegateID string) {
		if delegateID != nestedID {
			return
		}
		close(afterCommit)
		<-releaseRestore
	}
	restoreDone := make(chan struct{}, 1)
	go func() {
		_, _, err := r.s.restoreColdDelegateAttentionRuntime(nestedID)
		// The fixture has no saved child metadata, so this restore fails after
		// its commit. The subject here is the window: it must hold through that
		// commit and release on return, not whether the restore succeeds.
		t.Logf("cold restore after its window: %v", err)
		restoreDone <- struct{}{}
	}()
	waitForTestSignal(t, afterCommit, "the cold restore commit seam")

	if _, err := r.s.worktreeDispose(context.Background(), parentID, false, false); err == nil {
		release()
		t.Fatal("dispose ran while a descendant cold restore was in flight")
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("refused handoff removed the lane")
	}
	assertStableWorktreeResumable(t, r.s.delegateController, parentID, true)

	release()
	waitForTestSignal(t, restoreDone, "the cold restore after its window released")
	if _, err := r.s.worktreeDispose(context.Background(), parentID, false, false); err != nil {
		t.Fatalf("dispose retry after the restore window closed: %v", err)
	}
	if laneWorktreePresent(lanePath) {
		t.Fatal("lane still present after the retried dispose")
	}
}

// TestDelegateLaneHandoffRefusesSubtreeReservation proves the handoff itself
// refuses while a descendant start reservation is in flight, including a
// create reservation whose new child is not durable (and therefore not yet a
// subtree member) but whose parent is.
func TestDelegateLaneHandoffRefusesSubtreeReservation(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 8, 8)
	const (
		parentID = "dlg_reservation_parent"
		nestedID = "dlg_reservation_nested"
	)
	seedDelegateControllerIdle(t, c, parentID, "")
	seedDelegateControllerIdle(t, c, nestedID, parentID)

	c.mu.Lock()
	c.reservations[1] = &delegateStartRecord{token: 1, delegateID: nestedID}
	c.mu.Unlock()
	if admission := c.beginSubtreeLaneHandoff(parentID); admission != laneHandoffBusy {
		if admission == laneHandoffAcquired {
			c.endSubtreeLaneHandoff(parentID)
		}
		t.Fatal("handoff admitted with a descendant start reservation in flight")
	}

	c.mu.Lock()
	c.reservations[2] = &delegateStartRecord{
		token:      2,
		delegateID: "dlg_reservation_unborn",
		create:     true,
		descriptor: delegatestore.Descriptor{ParentDelegateID: parentID},
	}
	delete(c.reservations, 1)
	c.mu.Unlock()
	if admission := c.beginSubtreeLaneHandoff(parentID); admission != laneHandoffBusy {
		if admission == laneHandoffAcquired {
			c.endSubtreeLaneHandoff(parentID)
		}
		t.Fatal("handoff admitted with a create reservation under the subtree in flight")
	}

	c.mu.Lock()
	delete(c.reservations, 2)
	c.mu.Unlock()
	if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
		t.Fatal("handoff refused after the subtree reservations cleared")
	}
	c.endSubtreeLaneHandoff(parentID)
}

// seedControllerRun makes an existing idle delegate take one running
// generation, without appending a duplicate creation event.
func seedControllerRun(t *testing.T, c *delegateTreeController, delegateID string) delegateLease {
	t.Helper()
	lease := delegateLease{delegateID: delegateID, generation: 1}
	c.mu.Lock()
	_, err := c.appendLocked(delegateControllerRunStartedEvent(delegateID, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()))
	if err == nil {
		c.live[delegateID] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease:  lease,
			cancel: func() {},
			ready:  true,
		}}
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed run for %s: %v", delegateID, err)
	}
	return lease
}

// TestDelegateLaneHandoffAdditionalAdmissions covers the remaining gates from
// the spec's admission table, so removing any one of them fails a test.
func TestDelegateLaneHandoffAdditionalAdmissions(t *testing.T) {
	t.Parallel()
	const (
		parentID = "dlg_gate_parent"
		nestedID = "dlg_gate_nested"
	)
	newFencedPair := func(t *testing.T) *delegateTreeController {
		t.Helper()
		c, _ := newDelegateControllerTestHarness(t, 8, 8)
		seedDelegateControllerIdle(t, c, parentID, "")
		seedDelegateControllerIdle(t, c, nestedID, parentID)
		if c.beginSubtreeLaneHandoff(parentID) != laneHandoffAcquired {
			t.Fatal("clean idle subtree refused its handoff")
		}
		t.Cleanup(func() { c.endSubtreeLaneHandoff(parentID) })
		return c
	}

	t.Run("active generation refuses the handoff", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 8, 8)
		seedDelegateControllerIdle(t, c, parentID, "")
		seedDelegateControllerIdle(t, c, nestedID, parentID)
		seedControllerRun(t, c, nestedID)
		if admission := c.beginSubtreeLaneHandoff(parentID); admission != laneHandoffBusy {
			if admission == laneHandoffAcquired {
				c.endSubtreeLaneHandoff(parentID)
			}
			t.Fatal("handoff admitted with an active generation in the subtree")
		}
	})

	t.Run("create under a fenced parent", func(t *testing.T) {
		c := newFencedPair(t)
		lease := seedControllerRun(t, c, parentID)
		actor := delegateActor{lease: &lease}
		descriptor := delegatestore.Descriptor{Task: "fenced child", AgentType: "default", Resumable: true}
		reservation, err := c.ReserveCreate(actor, descriptor)
		assertBusyReservation(t, c, reservation, err, "create under a fenced parent")
	})

	t.Run("attention start under a handoff", func(t *testing.T) {
		c := newFencedPair(t)
		runtime := &Session{id: "child-" + nestedID, owningDelegateID: nestedID, delegateController: c}
		c.mu.Lock()
		c.live[nestedID] = &delegateLiveState{runtime: runtime}
		c.mu.Unlock()
		reservation, err := c.ReserveAttention(runtime, "attention-sentinel")
		assertBusyReservation(t, c, reservation, err, "attention start under the handoff")
	})

	t.Run("commit start under a handoff", func(t *testing.T) {
		c := newFencedPair(t)
		receipt := &delegateStartReservation{delegateID: nestedID}
		record := &delegateStartRecord{
			receipt:      receipt,
			token:        7,
			delegateID:   nestedID,
			generation:   1,
			capacityKind: delegateTurnCapacity,
			ctx:          context.Background(),
			done:         make(chan struct{}),
		}
		c.mu.Lock()
		c.reservations[record.token] = record
		c.mu.Unlock()
		if _, err := c.commitStart(receipt); !errors.Is(err, errDelegateTargetBusy) {
			t.Errorf("commit under the handoff = %v, want errDelegateTargetBusy", err)
		}
		c.mu.Lock()
		_, leaked := c.reservations[record.token]
		c.mu.Unlock()
		if leaked {
			t.Error("refused commit leaked its reservation")
		}
	})

	t.Run("attach runtime under a handoff", func(t *testing.T) {
		c := newFencedPair(t)
		lease := seedControllerRun(t, c, nestedID)
		if err := c.AttachRuntime(lease, &Session{id: "child-" + nestedID, owningDelegateID: nestedID, delegateController: c}); !errors.Is(err, errDelegateTargetBusy) {
			t.Errorf("runtime attach under the handoff = %v, want errDelegateTargetBusy", err)
		}
	})

	t.Run("cold restore under a closed ancestor", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 8, 8)
		seedNestedIdleDelegate(t, c, parentID, "", "/tmp/fenced-parent-lane")
		c.mu.Lock()
		c.durable[parentID].Descriptor.Isolation = "worktree"
		c.mu.Unlock()
		seedDelegateControllerIdle(t, c, nestedID, parentID)
		owner := &Session{id: "root-session", delegateController: c}
		if _, _, _, err := c.closeStableWorktreeResumability(owner, parentID, "test-close", false); err != nil {
			t.Fatalf("close parent resumability: %v", err)
		}
		if _, _, err := c.idleDelegateRestoreCommit(nestedID); !errors.Is(err, errDelegateTargetBusy) {
			t.Errorf("cold restore under a closed ancestor = %v, want errDelegateTargetBusy", err)
		}
	})
}

// TestDelegateLaneHandoffDisposesNestedLaneUnderParentFence covers the close
// path inside an explicit dispose: tearing down the target's child session runs
// that session's own lane disposal while the target's handoff is held. The
// ancestor fence already excludes admissions into the nested lane, so the
// nested lane must still be disposed rather than retained with its lock held.
func TestDelegateLaneHandoffDisposesNestedLaneUnderParentFence(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	parentID, parentLanePath, _ := r.seedStableIsolationLane(t)

	// Model the target's resident child session, which is what runs the close
	// that disposes its own delegate lanes during the parent's handoff.
	parentSession := newSession(t, withDir(parentLanePath), withConfig(r.s.cfg))
	parentSession.id = "child-" + parentID
	parentSession.stateDir = r.stateDir
	parentSession.owningDelegateID = parentID
	parentSession.delegateController = r.s.delegateController
	parentSession.delegateRootSessionID = r.s.id
	parentSession.ownsDelegateController = false
	parentSession.mu.Lock()
	parentSession.worktreeGitVersionOK = true
	parentSession.mu.Unlock()
	r.s.subagents.mu.Lock()
	r.s.subagents.subs[parentSession.id] = &subagent{sess: parentSession}
	r.s.subagents.mu.Unlock()

	// Create the nested lane from the parent session so its lock marker names
	// that session, exactly as a child spawned by the parent would.
	const nestedID = "dlg_nested_lane_under_dispose"
	c := r.s.delegateController
	nestedPath, _, _, _, _, err := parentSession.createDelegateWorktree(context.Background(), nestedID, "")
	if err != nil {
		t.Fatalf("create nested delegate worktree: %v", err)
	}
	lease := delegateLease{delegateID: nestedID, generation: 1}
	c.mu.Lock()
	_, err = c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: nestedID,
			Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
				ChildSessionID:   "child-" + nestedID,
				TranscriptRef:    encodeRef("", "child-"+nestedID),
				ParentDelegateID: parentID,
				OwnerSessionID:   r.s.id,
				VisibleSessionID: r.s.id,
				Task:             "nested worktree fixture",
				AgentType:        "default",
				ToolNameCeiling:  []string{"communicate"},
				WorkingDir:       nestedPath,
				LocalEnvPolicy:   "default",
				Isolation:        "worktree",
				Resumable:        true,
			}},
		},
		delegateControllerRunStartedEvent(nestedID, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
	)
	if err == nil {
		c.live[nestedID] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease:  lease,
			cancel: func() {},
			ready:  true,
		}}
		c.turnsInUse++
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed nested delegate: %v", err)
	}
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "fixture complete",
		endedAt: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("finish nested delegate: %v", err)
	}
	for _, plan := range plans.deliveries {
		token, admitted, deliveryErr := c.BeginDelivery(plan)
		if deliveryErr != nil || !admitted {
			t.Fatalf("begin nested fixture delivery: admitted=%t err=%v", admitted, deliveryErr)
		}
		if _, deliveryErr := c.CompleteDelivery(token, true); deliveryErr != nil {
			t.Fatalf("acknowledge nested fixture delivery: %v", deliveryErr)
		}
	}
	if !laneWorktreePresent(nestedPath) {
		t.Fatal("nested lane fixture was not created")
	}

	if _, err := r.s.worktreeDispose(context.Background(), parentID, false, false); err != nil {
		t.Fatalf("dispose parent with nested lane: %v", err)
	}
	if laneWorktreePresent(nestedPath) {
		t.Error("nested lane was retained (and left locked) during the parent's handoff")
	}
	if r.branchExists(t, nestedID) {
		t.Error("nested lane branch was retained during the parent's handoff")
	}
	if reg, locked, _ := r.laneLocked(t, nestedPath); reg || locked {
		t.Error("nested lane still registered or locked after the parent's handoff")
	}
}

// TestDelegateLaneHandoffKeepsCloseBudgetLockWhileRestoreInFlight covers the
// budget-exempt close tail: an expired close budget must not unlock a lane while
// a descendant restore window is open, and must unlock it once that window
// closes.
func TestDelegateLaneHandoffKeepsCloseBudgetLockWhileRestoreInFlight(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	parentID, lanePath, _ := r.seedStableIsolationLane(t)
	const nestedID = "dlg_nested_budget_tail"
	seedNestedIdleDelegate(t, r.s.delegateController, nestedID, parentID, lanePath)

	if err := r.s.delegateController.beginLaneRestore(nestedID); err != nil {
		t.Fatalf("open descendant restore window: %v", err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r.s.disposeDelegateLanesAtClose(expired)
	if present, locked, _ := r.laneLocked(t, lanePath); !present || !locked {
		t.Fatalf("close budget tail released the lane while a descendant restore was in flight: present=%t locked=%t", present, locked)
	}

	r.s.delegateController.endLaneRestore(nestedID)
	r.s.disposeDelegateLanesAtClose(expired)
	if present, locked, _ := r.laneLocked(t, lanePath); !present || locked {
		t.Errorf("close budget tail did not unlock the lane after the restore window closed: present=%t locked=%t", present, locked)
	}
}
