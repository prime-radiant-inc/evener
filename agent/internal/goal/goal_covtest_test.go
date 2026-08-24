package goal

import (
	"testing"
	"time"
)

func TestCovTakeTerminalReport(t *testing.T) {
	s := NewStore()

	if snap, ok := s.TakeTerminalReport(); ok || snap != (Snapshot{}) {
		t.Fatalf("empty store TakeTerminalReport() = (%+v, %v), want zero snapshot and false", snap, ok)
	}

	now := time.Unix(123, 0).UTC()
	s.Set("test goal", now)
	if snap, ok := s.TakeTerminalReport(); ok || snap != (Snapshot{}) {
		t.Fatalf("active goal TakeTerminalReport() = (%+v, %v), want zero snapshot and false", snap, ok)
	}

	if !s.SetTerminal(StatusComplete, "done", now.Add(time.Minute)) {
		t.Fatal("SetTerminal() = false, want true")
	}
	want := Snapshot{Objective: "test goal", Status: StatusComplete, StopReason: "done"}
	snap, ok := s.TakeTerminalReport()
	if !ok || snap != want {
		t.Fatalf("first terminal TakeTerminalReport() = (%+v, %v), want (%+v, true)", snap, ok, want)
	}

	if snap, ok := s.TakeTerminalReport(); ok || snap != (Snapshot{}) {
		t.Fatalf("second terminal TakeTerminalReport() = (%+v, %v), want zero snapshot and false", snap, ok)
	}
}

func TestCovRecordContinuationWithoutGoal(t *testing.T) {
	snap, active := NewStore().RecordContinuation(true, time.Unix(123, 0).UTC())
	if active || snap != (Snapshot{}) {
		t.Fatalf("RecordContinuation() without goal = (%+v, %v), want zero snapshot and false", snap, active)
	}
}
