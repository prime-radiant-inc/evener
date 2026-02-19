package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestTurn_HasTimestamp(t *testing.T) {
	before := time.Now().UTC()
	turn := NewTurn(TurnUserInput, llm.User("hello"))
	after := time.Now().UTC()

	if turn.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if turn.Timestamp.Before(before) || turn.Timestamp.After(after) {
		t.Fatalf("timestamp %v not between %v and %v", turn.Timestamp, before, after)
	}
}

func TestTurnKind_CheckpointAndSummary(t *testing.T) {
	if TurnCheckpoint != "CHECKPOINT" {
		t.Fatalf("TurnCheckpoint = %q, want CHECKPOINT", TurnCheckpoint)
	}
	if TurnSummary != "SUMMARY" {
		t.Fatalf("TurnSummary = %q, want SUMMARY", TurnSummary)
	}
}
