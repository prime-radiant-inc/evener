package transcript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

func statusEntry(kind schema.TurnKind, turnID string, span schema.TurnSpanKind, status schema.TurnCompletionStatus) Entry {
	turn := schema.Turn{Kind: kind, TurnID: turnID, TurnKind: span, Format: schema.TurnFormatIdentity}
	if kind == schema.TurnCompletion {
		turn.Completion = &schema.TurnCompletionInfo{Status: status}
	}
	return Entry{Kind: "entry", Turn: turn}
}

func TestExecutionTurnsAndWhichAreOpen(t *testing.T) {
	entries := []Entry{
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnUserInput}}, // legacy: ignored
		statusEntry(schema.TurnUserInput, "turn_m1", schema.TurnSpanExecution, ""),
		statusEntry(schema.TurnCompletion, "turn_m1", "", schema.TurnCompleted),
		statusEntry(schema.TurnUserInput, "t_open", schema.TurnSpanExecution, ""),
		statusEntry(schema.TurnHookCompleted, "t_gap", schema.TurnSpanGap, ""),
		statusEntry(schema.TurnUserInput, "turn_m2", schema.TurnSpanExecution, ""),
		statusEntry(schema.TurnCompletion, "turn_m2", "", schema.TurnFailed),
		statusEntry(schema.TurnReopen, "turn_m2", "", ""),
		statusEntry(schema.TurnUserInput, "turn_m3", schema.TurnSpanExecution, ""),
		statusEntry(schema.TurnCompletion, "turn_m3", "", schema.TurnInterrupted),
		statusEntry(schema.TurnReopen, "turn_m3", "", ""),
		statusEntry(schema.TurnCompletion, "turn_m3", "", schema.TurnCompleted),
	}
	executions := ExecutionTurns(entries)
	want := map[string]bool{"turn_m1": false, "t_open": true, "turn_m2": true, "turn_m3": false}
	if !reflect.DeepEqual(executions, want) {
		t.Fatalf("execution turns = %v, want %v", executions, want)
	}
}

// A fold's rewrite re-appends a persisted pair as a copy carrying
// OriginalOrdinal, its first entry restamped with the turn's TurnKind exactly
// as the original was (session.go's markLastPairOrdinalLocked). A copy of an
// already-completed turn's opening entry must not resurrect it as open: the
// copy is not a new start, and nothing after it in the file completes it
// again.
func TestExecutionTurnsIgnoresAFoldsCopyOfACompletedTurn(t *testing.T) {
	completedCopy := statusEntry(schema.TurnUserInput, "turn_m1", schema.TurnSpanExecution, "")
	ordinal := uint64(0)
	completedCopy.Turn.OriginalOrdinal = &ordinal
	entries := []Entry{
		statusEntry(schema.TurnUserInput, "turn_m1", schema.TurnSpanExecution, ""),
		statusEntry(schema.TurnCompletion, "turn_m1", "", schema.TurnCompleted),
		statusEntry(schema.TurnUserInput, "turn_m2", schema.TurnSpanExecution, ""),
		// A fold running mid-turn_m2 re-appends turn_m1's still-unpruned
		// opening pair after the fold's markers.
		completedCopy,
	}
	executions := ExecutionTurns(entries)
	want := map[string]bool{"turn_m1": false, "turn_m2": true}
	if !reflect.DeepEqual(executions, want) {
		t.Fatalf("execution turns = %v, want %v (a fold copy resurrected turn_m1 as open)", executions, want)
	}
}
