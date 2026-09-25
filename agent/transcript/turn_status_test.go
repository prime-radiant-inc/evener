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
