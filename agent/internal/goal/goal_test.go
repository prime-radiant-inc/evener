package goal

import (
	"testing"
	"time"
)

// clock returns a fixed time for deterministic tests.
func clock() time.Time { return time.Unix(0, 0).UTC() }

func TestStoreSetGetClear(t *testing.T) {
	s := NewStore()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("empty store should report no goal")
	}
	s.Set("make tests pass", clock())
	snap, ok := s.Snapshot()
	if !ok || snap.Status != StatusActive || snap.Objective != "make tests pass" {
		t.Fatalf("after Set: %+v ok=%v", snap, ok)
	}
	s.Clear()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("after Clear should report no goal")
	}
}

// TestRecordContinuationNoProgressGrace: leading read-only turns accrue toward the
// larger NeverProgressedLimit (not blocked early); a progressed turn resets the
// streak and flips to the tighter NoProgressLimit, after which NoProgressLimit
// consecutive no-progress turns block.
func TestRecordContinuationNoProgressGrace(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	for i := 0; i < NeverProgressedLimit-1; i++ {
		s.RecordContinuation(false /*progressed*/, clock())
		snap, _ := s.Snapshot()
		if snap.Status != StatusActive {
			t.Fatalf("leading turn %d should stay active, got %v", i, snap.Status)
		}
	}
	// G1: progress turn must (a) remain active, (b) reset NoProgressStreak to 0,
	// and (c) increment Iterations.
	snap, active := s.RecordContinuation(true, clock()) // progress: reset + flip to NoProgressLimit
	if !active {
		t.Fatal("progress turn must remain active")
	}
	if snap.Status != StatusActive {
		t.Fatalf("after progress turn: want StatusActive, got %v", snap.Status)
	}
	if snap.NoProgressStreak != 0 {
		t.Fatalf("after progress turn: want NoProgressStreak=0, got %d", snap.NoProgressStreak)
	}
	// G5: NeverProgressedLimit-1 leading turns + 1 progress turn.
	wantIters := NeverProgressedLimit
	if snap.Iterations != wantIters {
		t.Fatalf("after progress turn: want Iterations=%d, got %d", wantIters, snap.Iterations)
	}
	// G4: pin the exact turn at which blocking occurs — must be precisely
	// NoProgressLimit consecutive no-progress turns after the reset.
	for i := 0; i < NoProgressLimit; i++ {
		snap, active := s.RecordContinuation(false, clock())
		wantActive := i < NoProgressLimit-1
		if active != wantActive {
			t.Fatalf("no-progress turn %d: want active=%v, got %v", i, wantActive, active)
		}
		if wantActive && snap.Status != StatusActive {
			t.Fatalf("no-progress turn %d: want StatusActive, got %v", i, snap.Status)
		}
		if !wantActive && (snap.Status != StatusBlocked || snap.StopReason != "no progress") {
			t.Fatalf("no-progress turn %d: want blocked/no-progress, got %v/%q", i, snap.Status, snap.StopReason)
		}
	}
}

// TestNeverProgressedBlocks pins the fix for the breaker hole: a goal that never
// makes a mutating tool call must still stop (at NeverProgressedLimit), not run
// forever (the /par Critical B1).
func TestNeverProgressedBlocks(t *testing.T) {
	s := NewStore()
	s.Set("summarize the architecture", clock())
	for i := 0; i < NeverProgressedLimit-1; i++ {
		if _, active := s.RecordContinuation(false, clock()); !active {
			t.Fatalf("turn %d: never-progressed goal blocked too early", i)
		}
	}
	if _, active := s.RecordContinuation(false, clock()); active {
		t.Fatal("never-progressed goal must block at NeverProgressedLimit")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("want blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
	// G5: Iterations must equal NeverProgressedLimit after exactly that many turns.
	if snap.Iterations != NeverProgressedLimit {
		t.Fatalf("want Iterations=%d, got %d", NeverProgressedLimit, snap.Iterations)
	}
}

func TestSetTerminal(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	if !s.SetTerminal(StatusComplete, "", clock()) {
		t.Fatal("SetTerminal on active should succeed")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusComplete {
		t.Fatalf("want complete, got %v", snap.Status)
	}
	// Second call on a non-active goal is a no-op returning false.
	if s.SetTerminal(StatusBlocked, "x", clock()) {
		t.Fatal("SetTerminal on non-active should be no-op")
	}
	// G3: verify status and stop reason are unchanged after the no-op call.
	snap, _ = s.Snapshot()
	if snap.Status != StatusComplete {
		t.Fatalf("after no-op SetTerminal: want status=complete, got %v", snap.Status)
	}
	if snap.StopReason != "" {
		t.Fatalf("after no-op SetTerminal: want StopReason empty, got %q", snap.StopReason)
	}
}

// TestPersistSnapshotRestoreRoundTrip verifies that PersistSnapshot captures all
// fields and Restore reconstitutes them faithfully, including madeProgressOnce.
// The key behavioral invariant: a restored goal with prior progress must use
// NoProgressLimit (not the larger NeverProgressedLimit).
// G6: covers the previously untested PersistSnapshot/Restore path.
func TestPersistSnapshotRestoreRoundTrip(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	// One progress turn so madeProgressOnce=true, NoProgressStreak=0, Iterations=1.
	s.RecordContinuation(true, clock())

	obj, status, stopReason, iters, streak, madeProg, created, updated, ok := s.PersistSnapshot()
	if !ok {
		t.Fatal("PersistSnapshot: expected ok=true")
	}
	if obj != "obj" || status != string(StatusActive) || stopReason != "" {
		t.Fatalf("PersistSnapshot: unexpected values: obj=%q status=%q stopReason=%q", obj, status, stopReason)
	}
	if iters != 1 || streak != 0 || !madeProg {
		t.Fatalf("PersistSnapshot: unexpected counters: iters=%d streak=%d madeProgressOnce=%v", iters, streak, madeProg)
	}

	// Restore into a fresh store and verify the limit regime is NoProgressLimit
	// (not NeverProgressedLimit), proving madeProgressOnce survived the round-trip.
	s2 := NewStore()
	s2.Restore(obj, status, stopReason, iters, streak, madeProg, created, updated)

	for i := 0; i < NoProgressLimit-1; i++ {
		if _, active := s2.RecordContinuation(false, clock()); !active {
			t.Fatalf("restored goal: no-progress turn %d blocked too early (want NoProgressLimit=%d)", i, NoProgressLimit)
		}
	}
	if _, active := s2.RecordContinuation(false, clock()); active {
		t.Fatal("restored goal: must block at NoProgressLimit after prior progress (not NeverProgressedLimit)")
	}
	snap, _ := s2.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("restored goal: want blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
	if snap.Objective != "obj" {
		t.Fatalf("restored goal: objective changed to %q", snap.Objective)
	}
}
