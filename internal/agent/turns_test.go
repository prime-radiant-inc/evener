package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
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
