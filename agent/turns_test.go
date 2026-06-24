package agent_test

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestTurn_HasTimestamp(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC()
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("hello"))
	after := time.Now().UTC()

	if turn.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if turn.Timestamp.Before(before) || turn.Timestamp.After(after) {
		t.Fatalf("timestamp %v not between %v and %v", turn.Timestamp, before, after)
	}
}

func TestTurnKind_CheckpointAndSummary(t *testing.T) {
	t.Parallel()
	if schema.TurnCheckpoint != "CHECKPOINT" {
		t.Fatalf("TurnCheckpoint = %q, want CHECKPOINT", schema.TurnCheckpoint)
	}
	if schema.TurnSummary != "SUMMARY" {
		t.Fatalf("TurnSummary = %q, want SUMMARY", schema.TurnSummary)
	}
}
