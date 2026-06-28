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

	if turn.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	// Belt-and-suspenders: tolerate ±1 s for coarse-resolution or NTP-adjusted clocks
	// while still verifying NewTurn records an approximately-current timestamp.
	if d := turn.Timestamp.Sub(before); d < -time.Second || d > time.Second {
		t.Fatalf("timestamp %v not within 1s of start %v (delta %v)", turn.Timestamp, before, d)
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
