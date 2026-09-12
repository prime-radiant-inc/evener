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
			finishOld(true)
			epochA := locks.RecoveryState("A").Epoch
			finishNew := locks.BeginForceStop([]string{"B", "C"})
			if err := locks.PersistForceStop([]string{"B", "C"}, "C"); err != nil {
				t.Fatal(err)
			}
			if complete {
				finishNew(true)
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
				finishNew(true)
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
	finish(true)
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
	newer(true)
	locks.RecordResolvedSession("stable", "current", epoch)
	if locks.ResolvedSessionID("stable") != "" {
		t.Fatal("stale completion replaced newer ownership")
	}
	if !locks.RecoveryState("stable").ResumeRequired {
		t.Fatal("target record cleared newer recovery")
	}
}
