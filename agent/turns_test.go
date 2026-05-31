package agent_test

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/llm"
)

func TestTurn_HasTimestamp(t *testing.T) {
	before := time.Now().UTC()
	turn := agent.NewTurn(agent.TurnUserInput, llm.User("hello"))
	after := time.Now().UTC()

	if turn.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	if turn.Timestamp.Before(before) || turn.Timestamp.After(after) {
		t.Fatalf("timestamp %v not between %v and %v", turn.Timestamp, before, after)
	}
}

func TestTurnKind_CheckpointAndSummary(t *testing.T) {
	if agent.TurnCheckpoint != "CHECKPOINT" {
		t.Fatalf("TurnCheckpoint = %q, want CHECKPOINT", agent.TurnCheckpoint)
	}
	if agent.TurnSummary != "SUMMARY" {
		t.Fatalf("TurnSummary = %q, want SUMMARY", agent.TurnSummary)
	}
}
