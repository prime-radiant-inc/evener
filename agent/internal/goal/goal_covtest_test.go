package goal

import (
	"testing"
	"time"
)

// TestCovTakeTerminalReport covers TakeTerminalReport (goal.go lines 112-120).
func TestCovTakeTerminalReport(t *testing.T) {
	s := NewStore()

	// No goal → false.
	snap, ok := s.TakeTerminalReport()
	if ok || snap.Status != "" {
		t.Fatalf("no goal: snap=%+v ok=%v", snap, ok)
	}

	// Active goal → false.
	now := time.Now()
	s.Set("test goal", now)
	snap, ok = s.TakeTerminalReport()
	if ok {
		t.Fatalf("active goal: ok=%v", ok)
	}

	// SetTerminal to make the goal non-active, then TakeTerminalReport → true (first time).
	s.SetTerminal(StatusComplete, "done", now)
	snap, ok = s.TakeTerminalReport()
	if !ok {
		t.Fatal("terminated goal should return true on first TakeTerminalReport")
	}
	if snap.Status != StatusComplete {
		t.Fatalf("snap.Status = %q", snap.Status)
	}

	// Second call → false (already reported).
	_, ok = s.TakeTerminalReport()
	if ok {
		t.Fatal("second TakeTerminalReport should return false")
	}
}

// TestCovRecordContinuation covers RecordContinuation (goal.go lines 129+).
func TestCovRecordContinuation(t *testing.T) {
	s := NewStore()

	// No goal → returns empty and false.
	snap, active := s.RecordContinuation(true, time.Now())
	if active {
		t.Fatal("no goal should not be active")
	}
	if snap.Status != "" {
		t.Fatalf("no goal snap = %+v", snap)
	}
}
