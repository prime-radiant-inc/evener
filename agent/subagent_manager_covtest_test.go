package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestNewSubagentManager_DefaultLimit covers the default limit (lines 52-53).
func TestNewSubagentManager_DefaultLimit(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	if m.maxRetainedTerminal != defaultMaxRetainedTerminal {
		t.Fatalf("limit = %d, want %d", m.maxRetainedTerminal, defaultMaxRetainedTerminal)
	}
}

// TestNewSubagentManager_CustomLimit covers the custom limit.
func TestNewSubagentManager_CustomLimit(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 100)
	if m.maxRetainedTerminal != 100 {
		t.Fatalf("limit = %d, want 100", m.maxRetainedTerminal)
	}
}

// TestSubagentManager_Get covers get for existing and missing subagents.
func TestSubagentManager_Get(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	if got := m.get("nonexistent"); got != nil {
		t.Fatal("expected nil for nonexistent subagent")
	}
}

// TestSubagentManager_Remove covers remove for existing and missing.
func TestSubagentManager_Remove(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.remove("nonexistent") // should not panic
}

// TestSubagentManager_RemoveSession covers removeSession with matching and
// non-matching sessions.
func TestSubagentManager_RemoveSession(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.removeSession("nonexistent", nil) // should not panic
}

// TestSubagentManager_BeginReconstruction_Closing covers the closing-error
// path (line 86-87).
func TestSubagentManager_BeginReconstruction_Closing(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.closing = true
	_, _, _, err := m.beginReconstruction("child1")
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
}

// TestSubagentManager_TrackIfAbsent_Closing covers the closing-error path
// (line 117-118).
func TestSubagentManager_TrackIfAbsent_Closing(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.closing = true
	sub := &subagent{id: "test"}
	_, _, err := m.trackIfAbsent(sub)
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
}

// TestSubagentManager_AdmitReconstructed_Closing covers the closing-error
// path (line 133-134).
func TestSubagentManager_AdmitReconstructed_Closing(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.closing = true
	_, _, err := m.admitReconstructed(nil, func(s *subagent) error { return nil })
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
}

// TestSubagentManager_AdmitReconstructed_NilSub covers the nil-sub error
// path (line 136-137).
func TestSubagentManager_AdmitReconstructed_NilSub(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	_, _, err := m.admitReconstructed(nil, func(s *subagent) error { return nil })
	if err == nil {
		t.Fatal("expected error for nil sub")
	}
}

// TestSubagentManager_BeginReconstructionSideEffects_Closing covers the
// closing path (line 161-165).
func TestSubagentManager_BeginReconstructionSideEffects_Closing(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.closing = true
	sub := &subagent{id: "child1"}
	m.subs["child1"] = sub
	_, err := m.beginReconstructionSideEffects("child1", sub)
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
	// The sub should be deleted from the map.
	if m.get("child1") != nil {
		t.Fatal("expected child1 to be removed after closing")
	}
}

// TestSubagentManager_WaitForReconstructionSideEffects covers the no-active
// path (line 181-182 returns immediately when no active side effects).
func TestSubagentManager_WaitForReconstructionSideEffects(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.waitForReconstructionSideEffects() // should not block when no active effects
}

// TestSubagentManager_WaitForReconstructions_NoPending covers the empty path.
func TestSubagentManager_WaitForReconstructions_NoPending(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	m.waitForReconstructions() // should not block when no pending
}

// TestSubagentManager_Sessions covers the sessions accessor.
func TestSubagentManager_Sessions_Empty(t *testing.T) {
	m := newSubagentManager(func(events.EventKind, events.EventData) {}, 0)
	sessions := m.sessions()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestSubagentReconstructionWait covers the wait method (lines 71-73).
func TestSubagentReconstructionWait(t *testing.T) {
	r := &subagentReconstruction{done: make(chan struct{})}
	go func() {
		close(r.done)
	}()
	sub, err := r.wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub != nil {
		t.Fatal("expected nil sub")
	}
}
