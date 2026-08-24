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

// TestSetPromoteQueuedAsSteerFunc covers the setter.
func TestSetPromoteQueuedAsSteerFunc(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	fn := func(index int, id string) error {
		called = true
		if index != 1 || id != "test" {
			t.Fatalf("fn called with (%d, %q), want (1, test)", index, id)
		}
		return nil
	}
	s.SetPromoteQueuedAsSteerFunc(fn)
	s.mu.RLock()
	got := s.promoteSteerFunc
	s.mu.RUnlock()
	if got == nil {
		t.Fatal("promoteSteerFunc not set")
	}
	if err := got(1, "test"); err != nil {
		t.Fatalf("calling fn: %v", err)
	}
	if !called {
		t.Fatal("fn was not called")
	}
}

// TestSetCancelQueuedFunc covers the setter.
func TestSetCancelQueuedFunc(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	fn := func(index int, id string) (string, int, error) {
		called = true
		if index != 2 || id != "entry" {
			t.Fatalf("fn called with (%d, %q), want (2, entry)", index, id)
		}
		return "removed text", 3, nil
	}
	s.SetCancelQueuedFunc(fn)
	s.mu.RLock()
	got := s.cancelQueuedFunc
	s.mu.RUnlock()
	if got == nil {
		t.Fatal("cancelQueuedFunc not set")
	}
	text, images, err := got(2, "entry")
	if err != nil {
		t.Fatalf("calling fn: %v", err)
	}
	if text != "removed text" || images != 3 {
		t.Fatalf("fn returned (%q, %d), want (removed text, 3)", text, images)
	}
	if !called {
		t.Fatal("fn was not called")
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

// TestCloneServerBoolNonNil covers the non-nil path.
func TestCloneServerBoolNonNil(t *testing.T) {
	v := true
	clone := cloneServerBool(&v)
	if clone == nil || *clone != true {
		t.Fatalf("cloneServerBool(&true) = %v, want &true", clone)
	}
	*clone = false
	if v != true {
		t.Fatalf("original was mutated: v = %v, want true", v)
	}
}

// TestCloneServerBoolNil covers the nil path.
func TestCloneServerBoolNil(t *testing.T) {
	if clone := cloneServerBool(nil); clone != nil {
		t.Fatalf("cloneServerBool(nil) = %v, want nil", clone)
	}
}

// TestCloneServerInt64NonNil covers the non-nil path.
func TestCloneServerInt64NonNil(t *testing.T) {
	v := int64(42)
	clone := cloneServerInt64(&v)
	if clone == nil || *clone != 42 {
		t.Fatalf("cloneServerInt64(&42) = %v, want &42", clone)
	}
	*clone = 99
	if v != 42 {
		t.Fatalf("original was mutated: v = %d, want 42", v)
	}
}

// TestCloneServerInt64Nil covers the nil path.
func TestCloneServerInt64Nil(t *testing.T) {
	if clone := cloneServerInt64(nil); clone != nil {
		t.Fatalf("cloneServerInt64(nil) = %v, want nil", clone)
	}
}
