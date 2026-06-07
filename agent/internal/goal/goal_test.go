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
	s.RecordContinuation(true, clock()) // progress: reset + flip to NoProgressLimit
	for i := 0; i < NoProgressLimit; i++ {
		s.RecordContinuation(false, clock())
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("expected blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
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
}
