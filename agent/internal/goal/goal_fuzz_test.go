//go:build serffuzz

package goal

import (
	"testing"
	"time"
)

func FuzzGoalStoreTerminalPersistence(f *testing.F) {
	f.Add("finish the task", "complete", []byte{1, 0, 2})
	f.Add("investigate", "blocked", []byte{0, 0, 1, 2})
	f.Add("", "", []byte{2, 1, 0})

	f.Fuzz(func(t *testing.T, objective, reason string, operations []byte) {
		start := time.Unix(1_700_000_000, 0).UTC()
		store := NewStore()
		store.Set(objective, start)

		// Keep the pre-terminal run below both no-progress limits so SetTerminal
		// is guaranteed to drive the terminal transition for this artifact.
		preTurns := 1 + len(operations)%2
		for i := 0; i < preTurns; i++ {
			var op byte
			if i < len(operations) {
				op = operations[i]
			}
			snap, active := store.RecordContinuation(op&1 != 0, start.Add(time.Duration(i+1)*time.Second))
			if !active || snap.Status != StatusActive {
				t.Fatalf("pre-terminal continuation %d unexpectedly stopped: %+v active=%v", i, snap, active)
			}
		}

		persisted := captureFuzzGoal(store)
		restored := NewStore()
		restoreFuzzGoal(restored, persisted)
		assertFuzzGoalPersistence(t, "active restore", captureFuzzGoal(restored), persisted)
		store = restored

		terminal := StatusComplete
		if len(operations) > 0 && operations[0]&2 != 0 {
			terminal = StatusBlocked
		}
		terminalAt := start.Add(10 * time.Second)
		if !store.SetTerminal(terminal, reason, terminalAt) {
			t.Fatal("SetTerminal did not transition an active restored goal")
		}
		assertFuzzTerminalGoal(t, store, terminal, reason)

		postTerminal := operations
		if len(postTerminal) == 0 {
			postTerminal = []byte{0}
		}
		if len(postTerminal) > 32 {
			postTerminal = postTerminal[:32]
		}
		for i, op := range postTerminal {
			now := terminalAt.Add(time.Duration(i+1) * time.Second)
			switch op % 3 {
			case 0:
				snap, active := store.RecordContinuation(op&0x80 != 0, now)
				if active || snap.Status != terminal {
					t.Fatalf("terminal continuation reversed state: %+v active=%v", snap, active)
				}
			case 1:
				other := StatusBlocked
				if terminal == StatusBlocked {
					other = StatusComplete
				}
				if store.SetTerminal(other, "must not replace terminal reason", now) {
					t.Fatalf("SetTerminal changed terminal %q goal to %q", terminal, other)
				}
			case 2:
				before := captureFuzzGoal(store)
				replaced := NewStore()
				restoreFuzzGoal(replaced, before)
				assertFuzzGoalPersistence(t, "terminal restore", captureFuzzGoal(replaced), before)
				store = replaced
			}
			assertFuzzTerminalGoal(t, store, terminal, reason)
		}
	})
}

type fuzzGoalPersistence struct {
	objective        string
	status           string
	stopReason       string
	iterations       int
	noProgressStreak int
	madeProgressOnce bool
	created          time.Time
	updated          time.Time
	ok               bool
}

func captureFuzzGoal(store *Store) fuzzGoalPersistence {
	objective, status, stopReason, iterations, noProgressStreak, madeProgressOnce, created, updated, ok := store.PersistSnapshot()
	return fuzzGoalPersistence{
		objective:        objective,
		status:           status,
		stopReason:       stopReason,
		iterations:       iterations,
		noProgressStreak: noProgressStreak,
		madeProgressOnce: madeProgressOnce,
		created:          created,
		updated:          updated,
		ok:               ok,
	}
}

func restoreFuzzGoal(store *Store, persisted fuzzGoalPersistence) {
	store.Restore(
		persisted.objective,
		persisted.status,
		persisted.stopReason,
		persisted.iterations,
		persisted.noProgressStreak,
		persisted.madeProgressOnce,
		persisted.created,
		persisted.updated,
	)
}

func assertFuzzGoalPersistence(t *testing.T, phase string, got, want fuzzGoalPersistence) {
	t.Helper()
	if got != want {
		t.Fatalf("%s persistence = %+v, want %+v", phase, got, want)
	}
}

func assertFuzzTerminalGoal(t *testing.T, store *Store, terminal Status, reason string) {
	t.Helper()
	snap, ok := store.Snapshot()
	if !ok || snap.Status != terminal || snap.StopReason != reason {
		t.Fatalf("terminal snapshot = %+v ok=%v, want status=%q reason=%q", snap, ok, terminal, reason)
	}
}
