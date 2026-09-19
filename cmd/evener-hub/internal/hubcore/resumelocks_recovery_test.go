package hubcore

import "testing"

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
	epoch := locks.RecoveryState("stable").Epoch
	locks.RecordResolvedSession("stable", "wrong", epoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("stopping action recorded a target")
	}
	finish.Finish(true)
	locks.RecordResolvedSession("stable", "wrong", epoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("pending recovery recorded a completed target")
	}
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
