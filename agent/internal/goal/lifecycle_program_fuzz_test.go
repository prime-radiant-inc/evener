//go:build evenerfuzz

package goal

import (
	"html"
	"reflect"
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
		if _, active := store.RecordContinuation(foldStallOutcome(), false, start); active {
			t.Fatal("RecordContinuation stayed active without a goal")
		}
		if _, reported := store.TakeTerminalReport(); reported {
			t.Fatal("TakeTerminalReport reported without a goal")
		}

		store.Set(objective, start)
		if _, reported := store.TakeTerminalReport(); reported {
			t.Fatal("active goal emitted a terminal report")
		}
		if snap, active := store.RecordContinuation(TurnOutcome{ActionFingerprint: "fuzz", ObservationClass: "ok", ObservationHash: "h", StateDigest: "d", Mutated: true}, true, start.Add(time.Second)); !active || snap.Status != StatusActive || snap.NoProgressStreak != 0 || snap.Iterations != 1 {
			t.Fatalf("progress continuation = %+v active=%v", snap, active)
		}
		persisted, ok := store.PersistSnapshot()
		if !ok {
			t.Fatal("active goal did not persist")
		}
		restored := NewStore()
		restored.RestoreSnapshot(persisted)
		if snap, ok := restored.Snapshot(); !ok || snap.Objective != objective || snap.Status != StatusActive || snap.Iterations != 1 || snap.NoProgressStreak != 0 {
			t.Fatalf("restored active snapshot = %+v ok=%v", snap, ok)
		}

		neverProgressed := NewStore()
		neverProgressed.Set(objective, start)
		// First fold establishes history (a first-seen hash reads as
		// advancement); the identical second fold is the true repeat.
		if snap, active := neverProgressed.RecordContinuation(foldStallOutcome(), false, start.Add(time.Second)); !active || snap.NoProgressStreak != 0 {
			t.Fatalf("history-establishing continuation = %+v active=%v", snap, active)
		}
		if snap, active := neverProgressed.RecordContinuation(foldStallOutcome(), false, start.Add(2*time.Second)); !active || snap.NoProgressStreak != 1 {
			t.Fatalf("never-progress continuation = %+v active=%v", snap, active)
		}
		// Stall graduation under the ledger (spec §§1, 4, 6) on a fresh
		// store: K-1 identical turns accrue below the K=6 fresh-tier trip,
		// the K-th nudges (still active), and the next identical turn
		// blocks with "no progress" (mirrors
		// TestRecordContinuationNoProgressGrace).
		stalled := NewStore()
		stalled.Set(objective, start)
		for i := 0; i < RepetitionThresholdFresh-1; i++ {
			snap, active := stalled.RecordContinuation(foldStallOutcome(), false, start.Add(time.Duration(i+2)*time.Second))
			if !active || snap.Status != StatusActive {
				t.Fatalf("leading stall turn %d = %+v active=%v, want active", i, snap, active)
			}
		}
		snap, active := stalled.RecordContinuation(foldStallOutcome(), false, start.Add(time.Duration(RepetitionThresholdFresh+1)*time.Second))
		if !active || snap.Status != StatusActive {
			t.Fatalf("nudge turn = %+v active=%v, want the stage trip (still active)", snap, active)
		}
		snap, active = stalled.RecordContinuation(foldStallOutcome(), false, start.Add(time.Duration(RepetitionThresholdFresh+2)*time.Second))
		if active || snap.Status != StatusBlocked || snap.StopReason != "no progress" {
			t.Fatalf("post-nudge turn = %+v active=%v, want blocked/no-progress", snap, active)
		}
		if snap, active := stalled.RecordContinuation(TurnOutcome{ActionFingerprint: "fuzz", ObservationClass: "ok", ObservationHash: "h", StateDigest: "d", Mutated: true}, true, start.Add(10*time.Second)); active || snap.Status != StatusBlocked {
			t.Fatalf("terminal continuation changed state: %+v active=%v", snap, active)
		}
		if stalled.SetTerminal(StatusComplete, reason, start.Add(11*time.Second)) {
			t.Fatal("SetTerminal replaced an auto-blocked goal")
		}
		terminal, reported := stalled.TakeTerminalReport()
		if !reported || terminal.Status != StatusBlocked || terminal.StopReason != "no progress" {
			t.Fatalf("terminal report = %+v reported=%v", terminal, reported)
		}
		if _, reported := stalled.TakeTerminalReport(); reported {
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
	if persisted, ok := store.PersistSnapshot(); ok || !reflect.DeepEqual(persisted, PersistedGoal{}) {
		t.Fatalf("%s PersistSnapshot returned goal data: %+v", phase, persisted)
	}
}
