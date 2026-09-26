package hubcore

import (
	"errors"
	"testing"
)

func TestExplicitResumePreservesNewerAliasRecovery(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "during exit", true: "after exit"}[complete], func(t *testing.T) {
			locks := NewResumeLocks()
			finishOld := locks.BeginForceStop([]string{"A", "B"})
			if err := locks.PersistForceStop([]string{"A", "B"}, "B"); err != nil {
				t.Fatal(err)
			}
			finishOld.Finish(true)
			epochA := locks.RecoveryState("A").Epoch
			finishNew := locks.BeginForceStop([]string{"B", "C"})
			if err := locks.PersistForceStop([]string{"B", "C"}, "C"); err != nil {
				t.Fatal(err)
			}
			if complete {
				finishNew.Finish(true)
			}
			beforeB := locks.RecoveryState("B")
			beforeC := locks.RecoveryState("C")
			if err := locks.ExplicitResumeCompleted("A", epochA); err != nil {
				t.Fatal(err)
			}
			if locks.RecoveryState("A").ResumeRequired {
				t.Fatal("explicitly resumed alias remains fenced")
			}
			if after := locks.RecoveryState("B"); after.ResumeRequired != beforeB.ResumeRequired || after.Stopping != beforeB.Stopping {
				t.Fatalf("older resume changed newer B recovery: before=%+v after=%+v", beforeB, after)
			}
			if after := locks.RecoveryState("C"); after.ResumeRequired != beforeC.ResumeRequired || after.Stopping != beforeC.Stopping {
				t.Fatalf("older resume changed newer C recovery: before=%+v after=%+v", beforeC, after)
			}
			if !complete {
				finishNew.Finish(true)
			}
		})
	}
}

func TestResolvedSessionMappingRequiresCompletedCurrentEpoch(t *testing.T) {
	locks := NewResumeLocks()
	finish := locks.BeginForceStop([]string{"stable", "current"})
	if err := locks.PersistForceStop([]string{"stable", "current"}, "current"); err != nil {
		t.Fatal(err)
	}
	fenceEpoch := locks.RecoveryState("stable").Epoch
	locks.RecordResolvedSession("stable", "wrong", fenceEpoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("stopping action recorded a target")
	}
	finish.Finish(true)
	// The stop's Finish minted its epoch advance, so the fence-era snapshot is
	// stale now; a resume is admitted only after the stop finishes and
	// snapshots the post-fence epoch.
	locks.RecordResolvedSession("stable", "wrong", fenceEpoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("pending recovery recorded a completed target")
	}
	epoch := locks.RecoveryState("stable").Epoch
	if err := locks.ExplicitResumeCompleted("stable", epoch); err != nil {
		t.Fatal(err)
	}
	locks.RecordResolvedSession("stable", "current", epoch)
	if locks.ResolvedSessionID("current") != "current" {
		t.Fatal("completed target was not shared across aliases")
	}
	newer := locks.BeginForceStop([]string{"stable", "next"})
	if err := locks.PersistForceStop([]string{"stable", "next"}, "next"); err != nil {
		t.Fatal(err)
	}
	newer.Finish(true)
	locks.RecordResolvedSession("stable", "current", epoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("stale completion replaced newer ownership")
	}
	if !locks.RecoveryState("stable").ResumeRequired {
		t.Fatal("target record cleared newer recovery")
	}
}

// TestRejectForceStopRestoresConnectionSequence is the Medium RoboRev reported
// against the refused force-stop fence's follow-through: BeginForceStop and
// its finish.Finish(false) release each write the fenced aliases' connection-level
// recovery sequence, and RejectForceStop restored only the epochs. A
// connection established before the fence then saw the alias as stale and was
// refused — "requires Resume on a fresh connection" — even though the refusal
// canceled nothing.
func TestRejectForceStopRestoresConnectionSequence(t *testing.T) {
	locks := NewResumeLocks()
	connection := locks.RecoverySequence()
	finish := locks.BeginForceStop([]string{"A"})
	finish.Reject()
	finish.Finish(false)
	state := locks.RecoveryState("A")
	if state.LastRecoverySequence > connection {
		t.Fatalf("refused force stop left the connection-level sequence advanced: alias sequence=%d, connection captured %d", state.LastRecoverySequence, connection)
	}
	if state.Epoch != 0 || state.Stopping != 0 {
		t.Fatalf("refused force stop left the fence applied: %+v", state)
	}
}

// TestRejectForceStopSequenceRollbackKeepsNewerFenceAdvanced pins the
// nested-fence safety of the same rollback: only the aliases the refused fence
// still owns roll back. An alias a newer fence has since advanced keeps the
// newer value, so a refused outer fence cannot un-stale a connection the
// newer, still-held fence must keep refusing.
func TestRejectForceStopSequenceRollbackKeepsNewerFenceAdvanced(t *testing.T) {
	locks := NewResumeLocks()
	finishOld := locks.BeginForceStop([]string{"A", "B"})
	finishNew := locks.BeginForceStop([]string{"B", "C"})
	sequenceNew := locks.RecoverySequence()
	finishOld.Reject()
	finishOld.Finish(false)
	if got := locks.RecoveryState("A").LastRecoverySequence; got != 0 {
		t.Fatalf("refused fence left the unshared alias's sequence advanced: got %d, want 0", got)
	}
	if got := locks.RecoveryState("B").LastRecoverySequence; got != sequenceNew {
		t.Fatalf("refused outer fence rolled back an alias the newer fence advanced: got %d, want %d", got, sequenceNew)
	}
	finishNew.Finish(false)
	for _, id := range []string{"A", "B", "C"} {
		if state := locks.RecoveryState(id); state.Stopping != 0 {
			t.Fatalf("alias %s keeps a held fence: %+v", id, state)
		}
	}
}

// TestRejectForceStopSameAliasRefusalsRollBackEachOwnFence is the Medium
// RoboRev reported against RejectForceStop's fence identification: the
// refusal identified the target fence by alias-set equality, so with two
// concurrent force stops over identical aliases the older request's refusal
// marked the NEWER fence rejected while its own release still advanced the
// sequence, leaving stale connection/recovery state. Each caller must reject
// its own fence, and both refusals must stay admission-neutral whichever one
// completes first.
func TestRejectForceStopSameAliasRefusalsRollBackEachOwnFence(t *testing.T) {
	for _, order := range []string{"older completes first", "newer completes first"} {
		t.Run(order, func(t *testing.T) {
			locks := NewResumeLocks()
			connection := locks.RecoverySequence()
			fenceOlder := locks.BeginForceStop([]string{"A"})
			fenceNewer := locks.BeginForceStop([]string{"A"})
			refuse := func(fence *ForceStopFence) {
				fence.Reject()
				fence.Finish(false)
			}
			if order == "older completes first" {
				refuse(fenceOlder)
				refuse(fenceNewer)
			} else {
				refuse(fenceNewer)
				refuse(fenceOlder)
			}
			state := locks.RecoveryState("A")
			if state.LastRecoverySequence != connection {
				t.Fatalf("refused same-alias force stops left the connection-level sequence advanced: alias sequence=%d, connection captured %d", state.LastRecoverySequence, connection)
			}
			if state.Epoch != 0 || state.Stopping != 0 {
				t.Fatalf("refused same-alias force stops left a fence applied: %+v", state)
			}
		})
	}
}

// TestRejectForceStopOverlappingFencesRestoreTruePreFenceSequence is the
// Medium RoboRev reported against the overlapping-fence rollback: when
// BeginForceStop([A,B]) is followed by BeginForceStop([B,C]), the newer fence
// saves the older fence's written value as B's previous state, so rejecting
// the older fence and then the newer one restored B to a value the
// already-rolled-back older fence had written instead of the true pre-fence
// value. Both stops refused and canceled nothing, so every shared alias must
// end at its true pre-fence sequence.
func TestRejectForceStopOverlappingFencesRestoreTruePreFenceSequence(t *testing.T) {
	locks := NewResumeLocks()
	connection := locks.RecoverySequence()
	fenceOlder := locks.BeginForceStop([]string{"A", "B"})
	fenceNewer := locks.BeginForceStop([]string{"B", "C"})
	fenceOlder.Reject()
	fenceOlder.Finish(false)
	fenceNewer.Reject()
	fenceNewer.Finish(false)
	for _, id := range []string{"A", "B", "C"} {
		state := locks.RecoveryState(id)
		if state.LastRecoverySequence != connection {
			t.Fatalf("refused overlapping force stops left alias %s's connection-level sequence at %d, want the pre-fence %d", id, state.LastRecoverySequence, connection)
		}
		if state.Epoch != 0 || state.Stopping != 0 {
			t.Fatalf("refused overlapping force stops left alias %s fenced: %+v", id, state)
		}
	}
}

// TestForceStopRejectKeepsConfirmedStoppedInvalidation is the Medium RoboRev
// reported against the refused force-stop fence's epoch rollback: Reject
// unconditionally decremented each fenced alias's Epoch without recording the
// value the fence started from, so the rollback also erased an intervening
// advance by another actor — a confirmed-stopped no-op's
// InvalidateResumeAdmission, which advances epochs without installing a fence.
// A registration that had snapshotted the fence-advanced epoch and parked on
// the no-op's held alias token was then admitted on that stale pre-decision
// snapshot after shutdown had already reported the session stopped.
func TestForceStopRejectKeepsConfirmedStoppedInvalidation(t *testing.T) {
	locks := NewResumeLocks()
	// The confirmed-stopped no-op holds the alias token across its decision
	// and success return, exactly like checkConfirmedStoppedWithoutClaim.
	token := locks.For("A")
	token.Lock()
	// A concurrent force stop installs its fence: Epoch 0→1, Stopping 0→1.
	fence := locks.BeginForceStop([]string{"A"})
	// A resume registration snapshots the fence-advanced epoch and parks on
	// the alias token the no-op holds.
	captured := locks.RecoveryState("A").Epoch
	ctx := t.Context()
	registration := make(chan error, 1)
	go func() {
		_, err := locks.RegisterResume(ctx, "A", []string{"A"}, map[string]uint64{"A": captured})
		registration <- err
	}()
	// The no-op reports the session stopped and invalidates admission while
	// it still holds the alias token: Epoch 1→2 with no fence of its own.
	locks.InvalidateResumeAdmission([]string{"A"})
	// The concurrent force stop is refused — it canceled nothing — and rolls
	// its fence back before the no-op releases the token.
	fence.Reject()
	fence.Finish(false)
	// The parked registration acquires the released token and must re-admit
	// on a post-decision snapshot, not launch on the pre-invalidation one.
	token.Unlock()
	if err := <-registration; !errors.Is(err, ErrResumeInvalidated) {
		t.Fatalf("registration admitted on the pre-invalidation epoch after the refused fence rolled back the no-op's invalidation: %v", err)
	}
}

// TestForceStopRejectEpochAncestryKeepsInvalidationAdvanced pins the
// invalidation-survival invariant under the monotonic-epoch mechanics: fences
// publish no admission epoch at begin, so a refusal has nothing to roll back
// and can never erase an invalidation published between two fences — the
// confirmed-stopped no-op's advance is the only one the alias ever records,
// and it stands whichever order the overlapping refusals complete in.
func TestForceStopRejectEpochAncestryKeepsInvalidationAdvanced(t *testing.T) {
	locks := NewResumeLocks()
	// The older fence holds its admission fence but publishes no epoch.
	fenceOlder := locks.BeginForceStop([]string{"A"})
	// A confirmed-stopped no-op publishes its decision between the fences:
	// Epoch 0→1 with no fence of its own.
	locks.InvalidateResumeAdmission([]string{"A"})
	// The newer fence overlaps the invalidation's publication.
	fenceNewer := locks.BeginForceStop([]string{"A"})
	// The older refusal cancels nothing and publishes nothing: the
	// invalidation's advance is untouched.
	fenceOlder.Reject()
	fenceOlder.Finish(false)
	if got := locks.RecoveryState("A").Epoch; got != 1 {
		t.Fatalf("older fence's refusal changed the epoch the invalidation published: got %d, want 1", got)
	}
	// The newer refusal is equally neutral: the invalidation published
	// between the fences survives both refusals.
	fenceNewer.Reject()
	fenceNewer.Finish(false)
	state := locks.RecoveryState("A")
	if state.Epoch != 1 {
		t.Fatalf("refused fences erased the invalidation published between them: epoch %d, want 1", state.Epoch)
	}
	if state.Stopping != 0 {
		t.Fatalf("refused fences left the alias fenced: %+v", state)
	}
}

// TestForceStopFinishMintsEpochOnce is the Medium RoboRev reported as the
// admission-epoch ABA: under the begin-advance/reject-rollback pair a waiter
// could snapshot the epoch a fence wrote, watch that fence reject back to the
// prior value, and then match again when a later fence re-advanced to the
// same value and changed the world — the stale snapshot admitted an action
// the later stop should have invalidated. With the advance minted only at an
// unrejected Finish, a rejected fence publishes nothing, a completed stop
// mints a value no earlier snapshot holds, and every pre-publication snapshot
// stays stale.
func TestForceStopFinishMintsEpochOnce(t *testing.T) {
	locks := NewResumeLocks()
	// A resume admitted before any fence snapshots the standing epoch; a
	// waiter arriving during F1's hold snapshots the same value, because the
	// fence publishes nothing at begin.
	admitted := locks.RecoveryState("A").Epoch
	f1 := locks.BeginForceStop([]string{"A"})
	duringFence := locks.RecoveryState("A").Epoch
	// F1 is refused: it canceled nothing, and publishes nothing.
	f1.Reject()
	f1.Finish(false)
	if got := locks.RecoveryState("A").Epoch; got != admitted {
		t.Fatalf("refused fence changed the admission epoch: got %d, want %d", got, admitted)
	}
	// F2 completes a real stop: its advance is minted exactly once.
	f2 := locks.BeginForceStop([]string{"A"})
	if err := locks.PersistForceStop([]string{"A"}, "A"); err != nil {
		t.Fatal(err)
	}
	f2.Finish(true)
	if got := locks.RecoveryState("A").Epoch; got != admitted+1 {
		t.Fatalf("completed stop did not mint exactly one advance: got %d, want %d", got, admitted+1)
	}
	// The during-F1 snapshot can never be reissued: a registration holding it
	// is invalidated, and a stale completion cannot clear the newer recovery.
	if _, err := locks.RegisterResume(t.Context(), "A", []string{"A"}, map[string]uint64{"A": duringFence}); !errors.Is(err, ErrResumeInvalidated) {
		t.Fatalf("during-fence snapshot admitted after the world changed: %v", err)
	}
	if err := locks.ExplicitResumeCompleted("A", duringFence); err != nil {
		t.Fatal(err)
	}
	if !locks.RecoveryState("A").ResumeRequired {
		t.Fatal("stale completion cleared the newer recovery")
	}
}

// TestForceStopFenceFinishIsIdempotent is the Low RoboRev reported on the
// fence release: Finish records finished but never checks it, while Reject
// guards on rejected || finished, so a second Finish would decrement Stopping
// below zero and advance the connection-level recovery sequence again. The
// release must be idempotent the way the rejection already is.
func TestForceStopFenceFinishIsIdempotent(t *testing.T) {
	locks := NewResumeLocks()
	finish := locks.BeginForceStop([]string{"A"})
	finish.Finish(false)
	first := locks.RecoveryState("A")
	firstSequence := locks.RecoverySequence()
	finish.Finish(false)
	state := locks.RecoveryState("A")
	if state.Stopping != first.Stopping {
		t.Fatalf("second Finish changed Stopping: after first=%d, after second=%d", first.Stopping, state.Stopping)
	}
	if got := locks.RecoverySequence(); got != firstSequence {
		t.Fatalf("second Finish advanced the connection-level sequence: got %d, want %d", got, firstSequence)
	}
	if state.LastRecoverySequence != first.LastRecoverySequence {
		t.Fatalf("second Finish rewrote the fenced alias's sequence: after first=%d, after second=%d", first.LastRecoverySequence, state.LastRecoverySequence)
	}
}
