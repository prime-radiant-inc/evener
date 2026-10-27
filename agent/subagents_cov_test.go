package agent

import (
	"testing"
)

// TestTrySetDisposeGate covers trySetDisposeGate:
// returns false when running or driving, true and sets gate when quiescent.
func TestTrySetDisposeGate(t *testing.T) {
	t.Parallel()
	sub := &subagent{id: "sub_1"}
	// Quiescent subagent -> gate set, returns true.
	if !sub.trySetDisposeGate() {
		t.Fatal("quiescent subagent should set gate")
	}
	if !sub.disposeGated {
		t.Fatal("disposeGated should be true after trySetDisposeGate")
	}
	// Running subagent -> gate refused.
	sub2 := &subagent{id: "sub_2", running: true}
	if sub2.trySetDisposeGate() {
		t.Fatal("running subagent should not set gate")
	}
	if sub2.disposeGated {
		t.Fatal("disposeGated should not be set on running subagent")
	}
	// Driving subagent -> gate refused.
	sub3 := &subagent{id: "sub_3", driving: true}
	if sub3.trySetDisposeGate() {
		t.Fatal("driving subagent should not set gate")
	}
}

// TestClearDisposeGate covers clearDisposeGate:
// reverses the dispose gate set by trySetDisposeGate.
func TestClearDisposeGate(t *testing.T) {
	t.Parallel()
	sub := &subagent{id: "sub_1", disposeGated: true}
	sub.clearDisposeGate()
	if sub.disposeGated {
		t.Fatal("disposeGated should be false after clearDisposeGate")
	}
	// Clearing an already-cleared gate is a no-op.
	sub.clearDisposeGate()
	if sub.disposeGated {
		t.Fatal("disposeGated should still be false")
	}
}

// TestNoteParentJobActivity covers noteParentJobActivity: nil session and nil callback guards, and the
// happy path.
func TestNoteParentJobActivity(t *testing.T) {
	t.Parallel()
	// Nil session.
	var s *Session
	s.noteParentJobActivity("phase") // should not panic

	// Session without parentJobActivity callback.
	s2 := newTestSession(t)
	s2.noteParentJobActivity("phase") // should not panic

	// Session with parentJobActivity callback and parentDelegateID.
	called := false
	s3 := newTestSession(t)
	s3.cfg.spawn.parentDelegateID = "dlg_parent"
	s3.cfg.spawn.parentJobActivity = func(id, phase string) {
		if id != "dlg_parent" || phase != "running" {
			t.Errorf("parentJobActivity called with id=%q phase=%q", id, phase)
		}
		called = true
	}
	s3.noteParentJobActivity("running")
	if !called {
		t.Fatal("parentJobActivity callback should have been called")
	}

	// Callback set but no parentDelegateID -> no call.
	called = false
	s4 := newTestSession(t)
	s4.cfg.spawn.parentJobActivity = func(id, phase string) {
		called = true
	}
	s4.noteParentJobActivity("running")
	if called {
		t.Fatal("parentJobActivity should not be called without parentDelegateID")
	}
}

// TestMarkSalvagedTurnPersistedAndHasSalvageFromFinalRound covers
// markSalvagedTurnPersisted and hasSalvageFromFinalRound.
func TestMarkSalvagedTurnPersistedAndHasSalvageFromFinalRound(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	// Fresh session: no salvage.
	if s.hasSalvageFromFinalRound() {
		t.Fatal("fresh session should not have salvage")
	}
	// Simulate a salvage in round 1.
	s.mu.Lock()
	s.totalRounds = 1
	s.mu.Unlock()
	s.markSalvagedTurnPersisted()
	if !s.hasSalvageFromFinalRound() {
		t.Fatal("should have salvage from final round after marking in current round")
	}
	// Advance to round 2: salvage is stale.
	s.mu.Lock()
	s.totalRounds = 2
	s.mu.Unlock()
	if s.hasSalvageFromFinalRound() {
		t.Fatal("should not have salvage from final round after advancing rounds")
	}
}
