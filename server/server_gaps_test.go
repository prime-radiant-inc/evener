package server

import (
	"testing"
)

// TestIncrementTurns covers IncrementTurns.
func TestIncrementTurns(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.IncrementTurns()
	s.IncrementTurns()
	s.mu.RLock()
	turns := s.status.Turns
	s.mu.RUnlock()
	if turns != 2 {
		t.Fatalf("turns = %d, want 2", turns)
	}
}

// TestSetProcessingTurn covers SetProcessingTurn.
func TestSetProcessingTurn(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetProcessingTurn("turn-1")
	s.mu.RLock()
	processing := s.processing
	activeTurnID := s.appActiveTurnID
	s.mu.RUnlock()
	if !processing {
		t.Fatal("processing should be true")
	}
	if activeTurnID != "turn-1" {
		t.Fatalf("appActiveTurnID = %q, want turn-1", activeTurnID)
	}
}

// TestSubmitClientMutationStart covers SubmitClientMutationStart. It should
// not block even if the input channel is full.
func TestSubmitClientMutationStart(t *testing.T) {
	s := NewServer(ServerConfig{})
	// Should not panic or block.
	s.SubmitClientMutationStart("session-1")
}
