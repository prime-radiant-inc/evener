//go:build serffuzz

package goal

import (
	"html"
	"strings"
	"testing"
	"time"
)

// FuzzGoalLifecycleProgram drives the complete in-memory goal lifecycle,
// including automatic no-progress blocking, exactly-once terminal reporting,
// clearing, and continuation prompt rendering.
func FuzzGoalLifecycleProgram(f *testing.F) {
	f.Add("finish <task>", "stalled")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, objective, reason string) {
		start := time.Unix(1_700_000_000, 0).UTC()
		store := NewStore()
		assertFuzzGoalAbsent(t, store, "new")
		if store.SetTerminal(StatusComplete, reason, start) {
			t.Fatal("SetTerminal succeeded without a goal")
		}
		if _, active := store.RecordContinuation(false, start); active {
			t.Fatal("RecordContinuation stayed active without a goal")
		}
		if _, reported := store.TakeTerminalReport(); reported {
			t.Fatal("TakeTerminalReport reported without a goal")
		}

		store.Set(objective, start)
		if _, reported := store.TakeTerminalReport(); reported {
			t.Fatal("active goal emitted a terminal report")
		}
		if snap, active := store.RecordContinuation(true, start.Add(time.Second)); !active || snap.Status != StatusActive || snap.NoProgressStreak != 0 || snap.Iterations != 1 {
			t.Fatalf("progress continuation = %+v active=%v", snap, active)
		}
		objective0, status0, reason0, iterations0, streak0, progressed0, created0, updated0, ok := store.PersistSnapshot()
		if !ok {
			t.Fatal("active goal did not persist")
		}
		restored := NewStore()
		restored.Restore(objective0, status0, reason0, iterations0, streak0, progressed0, created0, updated0)
		if snap, ok := restored.Snapshot(); !ok || snap.Objective != objective || snap.Status != StatusActive || snap.Iterations != 1 || snap.NoProgressStreak != 0 {
			t.Fatalf("restored active snapshot = %+v ok=%v", snap, ok)
		}

		neverProgressed := NewStore()
		neverProgressed.Set(objective, start)
		if snap, active := neverProgressed.RecordContinuation(false, start.Add(time.Second)); !active || snap.NoProgressStreak != 1 {
			t.Fatalf("never-progress continuation = %+v active=%v", snap, active)
		}
		for i := 0; i < NoProgressLimit; i++ {
			snap, active := store.RecordContinuation(false, start.Add(time.Duration(i+2)*time.Second))
			wantActive := i < NoProgressLimit-1
			if active != wantActive {
				t.Fatalf("no-progress turn %d active=%v, want %v", i, active, wantActive)
			}
			if !wantActive && (snap.Status != StatusBlocked || snap.StopReason != "no progress") {
				t.Fatalf("no-progress terminal snapshot = %+v", snap)
			}
		}
		if snap, active := store.RecordContinuation(true, start.Add(10*time.Second)); active || snap.Status != StatusBlocked {
			t.Fatalf("terminal continuation changed state: %+v active=%v", snap, active)
		}
		if store.SetTerminal(StatusComplete, reason, start.Add(11*time.Second)) {
			t.Fatal("SetTerminal replaced an auto-blocked goal")
		}
		terminal, reported := store.TakeTerminalReport()
		if !reported || terminal.Status != StatusBlocked || terminal.StopReason != "no progress" {
			t.Fatalf("terminal report = %+v reported=%v", terminal, reported)
		}
		if _, reported := store.TakeTerminalReport(); reported {
			t.Fatal("terminal report emitted twice")
		}

		store.Clear()
		assertFuzzGoalAbsent(t, store, "cleared")

		completed := NewStore()
		completed.Set(objective, start)
		if !completed.SetTerminal(StatusComplete, reason, start.Add(time.Second)) {
			t.Fatal("SetTerminal did not complete an active goal")
		}
		if snap, reported := completed.TakeTerminalReport(); !reported || snap.Status != StatusComplete || snap.StopReason != reason {
			t.Fatalf("complete report = %+v reported=%v", snap, reported)
		}

		prompt := Render(objective)
		wantObjective := "<objective>" + html.EscapeString(objective) + "</objective>"
		if !strings.Contains(prompt, wantObjective) || strings.Contains(prompt, "{{objective}}") {
			t.Fatalf("Render(%q) did not safely substitute objective", objective)
		}
	})
}

func assertFuzzGoalAbsent(t *testing.T, store *Store, phase string) {
	t.Helper()
	if snap, ok := store.Snapshot(); ok || snap != (Snapshot{}) {
		t.Fatalf("%s Snapshot = %+v ok=%v, want no goal", phase, snap, ok)
	}
	if objective, status, reason, iterations, streak, progressed, created, updated, ok := store.PersistSnapshot(); ok || objective != "" || status != "" || reason != "" || iterations != 0 || streak != 0 || progressed || !created.IsZero() || !updated.IsZero() {
		t.Fatalf("%s PersistSnapshot returned goal data", phase)
	}
}
