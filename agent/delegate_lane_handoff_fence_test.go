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
	<-seamArrived

	commit, _, commitErr := r.s.delegateController.idleDelegateRestoreCommit(nestedID)
	installation, installErr := r.s.delegateController.beginIdleRuntimeInstallation(nestedID)
	if installation != nil {
		installation.release()
	}
	_, _, restoreErr := r.s.restoreColdDelegateAttentionRuntime(nestedID)

	close(seamRelease)
	disposeErr := <-disposeDone

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

	if !c.beginSubtreeLaneHandoff(parentID) {
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
	if reservation, err := c.ReserveStart(rootDelegateActor("root-session"), parentID); !errors.Is(err, errDelegateTargetBusy) {
		if reservation != nil {
			c.AbortStart(reservation)
		}
		t.Errorf("target start under the handoff = %v, want errDelegateTargetBusy", err)
	}
	if reservation, err := c.reserveOwedAttentionStart(nestedID, "attention-sentinel", 1, nil); !errors.Is(err, errDelegateTargetBusy) {
		if reservation != nil {
			c.AbortStart(reservation)
		}
		t.Errorf("nested owed attention start under the handoff = %v, want errDelegateTargetBusy", err)
	}

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
	if c.beginSubtreeLaneHandoff(parentID) {
		c.endSubtreeLaneHandoff(parentID)
		t.Fatal("handoff admitted while a descendant restore window was open")
	}
	c.endLaneRestore(nestedID)
	if !c.beginSubtreeLaneHandoff(parentID) {
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
	if c.beginSubtreeLaneHandoff(parentID) {
		c.endSubtreeLaneHandoff(parentID)
		t.Fatal("handoff admitted while a wake pass held the nested runtime")
	}
	c.releaseAttentionRestoreHold(nestedID)
	if !c.beginSubtreeLaneHandoff(parentID) {
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
	if !c.beginSubtreeLaneHandoff(parentID) {
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
	r.s.delegateRestoreAfterCommit = func(delegateID string) {
		if delegateID != nestedID {
			return
		}
		close(afterCommit)
		<-releaseRestore
	}
	restoreDone := make(chan error, 1)
	go func() {
		_, _, err := r.s.restoreColdDelegateAttentionRuntime(nestedID)
		restoreDone <- err
	}()
	<-afterCommit

	if _, err := r.s.worktreeDispose(context.Background(), parentID, false, false); err == nil {
		t.Fatal("dispose ran while a descendant cold restore was in flight")
	}
	if !laneWorktreePresent(lanePath) {
		t.Fatal("refused handoff removed the lane")
	}
	assertStableWorktreeResumable(t, r.s.delegateController, parentID, true)

	close(releaseRestore)
	select {
	case <-restoreDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: fixture rendezvous normally takes milliseconds; this only bounds a deadlock.
		t.Fatal("cold restore did not finish after its window was released")
	}
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
	if c.beginSubtreeLaneHandoff(parentID) {
		c.endSubtreeLaneHandoff(parentID)
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
	if c.beginSubtreeLaneHandoff(parentID) {
		c.endSubtreeLaneHandoff(parentID)
		t.Fatal("handoff admitted with a create reservation under the subtree in flight")
	}

	c.mu.Lock()
	delete(c.reservations, 2)
	c.mu.Unlock()
	if !c.beginSubtreeLaneHandoff(parentID) {
		t.Fatal("handoff refused after the subtree reservations cleared")
	}
	c.endSubtreeLaneHandoff(parentID)
}
