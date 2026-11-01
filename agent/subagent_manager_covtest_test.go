package agent

import (
	"errors"
	"testing"
)

// TestNewSubagentManager_DefaultLimit covers the default limit (lines 52-53).
func TestNewSubagentManager_DefaultLimit(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	if m.maxRetainedTerminal != defaultMaxRetainedTerminal {
		t.Fatalf("limit = %d, want %d", m.maxRetainedTerminal, defaultMaxRetainedTerminal)
	}
}

// TestNewSubagentManager_CustomLimit covers the custom limit.
func TestNewSubagentManager_CustomLimit(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(100)
	if m.maxRetainedTerminal != 100 {
		t.Fatalf("limit = %d, want 100", m.maxRetainedTerminal)
	}
}

// TestSubagentManager_Get covers get for existing and missing subagents.
func TestSubagentManager_Get(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	if got := m.get("nonexistent"); got != nil {
		t.Fatal("expected nil for nonexistent subagent")
	}
}

// TestSubagentManager_HasRunningChildren covers every liveness state the
// idle-release pre-gate probes: a restored-idle record has no runner channel
// and must read as quiet (nil done is the steady state, not liveness), an
// open runner is busy, a closed runner is quiet, the running/driving/finalizing
// flags are busy even with a closed channel, and a pending reconstruction is
// busy even with no records at all.
func TestSubagentManager_HasRunningChildren(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	if m.hasRunningChildren() {
		t.Fatal("empty manager reported busy")
	}

	// A restored-idle record: no runner channel ever assigned.
	m.track(&subagent{id: "restored-idle"})
	if m.hasRunningChildren() {
		t.Fatal("restored-idle record (nil done) reported busy; nil done is the steady state, not liveness")
	}

	// A finished runner reads quiet.
	closed := make(chan struct{})
	close(closed)
	m.track(&subagent{id: "finished", done: closed})
	if m.hasRunningChildren() {
		t.Fatal("closed runner reported busy")
	}

	// An executing runner reads busy.
	m.track(&subagent{id: "running", done: make(chan struct{})})
	if !m.hasRunningChildren() {
		t.Fatal("open runner reported quiet")
	}
	m.remove("running")

	// Drive and finalization flags read busy even with the runner closed.
	m.track(&subagent{id: "driving", done: closed, driving: true})
	if !m.hasRunningChildren() {
		t.Fatal("driving record reported quiet")
	}
	m.remove("driving")
	m.track(&subagent{id: "finalizing", done: closed, finalizing: true})
	if !m.hasRunningChildren() {
		t.Fatal("finalizing record reported quiet")
	}
	m.remove("finalizing")

	// A pending reconstruction reads busy even with no records at all.
	pending := &subagentReconstruction{done: make(chan struct{})}
	m.mu.Lock()
	m.reconstructing["being-restored"] = pending
	m.mu.Unlock()
	if !m.hasRunningChildren() {
		t.Fatal("pending reconstruction reported quiet")
	}
}

// TestSubagentManager_BeginReconstruction_Closing covers the closing-error
// path (line 86-87).
func TestSubagentManager_BeginReconstruction_Closing(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	m.closing = true
	_, _, _, err := m.beginReconstruction("child1")
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
}

// TestSubagentManager_TrackIfAbsent_Closing covers the closing-error path
// (line 117-118).
func TestSubagentManager_TrackIfAbsent_Closing(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
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
	t.Parallel()
	m := newSubagentManager(0)
	m.closing = true
	_, _, err := m.admitReconstructed(nil, func(s *subagent) error { return nil })
	if !errors.Is(err, errSubagentManagerClosing) {
		t.Fatalf("error = %v, want errSubagentManagerClosing", err)
	}
}

// TestSubagentManager_AdmitReconstructed_NilSub covers the nil-sub error
// path (line 136-137).
func TestSubagentManager_AdmitReconstructed_NilSub(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	_, _, err := m.admitReconstructed(nil, func(s *subagent) error { return nil })
	if err == nil {
		t.Fatal("expected error for nil sub")
	}
}

// TestSubagentManager_BeginReconstructionSideEffects_Closing covers the
// closing path (line 161-165).
func TestSubagentManager_BeginReconstructionSideEffects_Closing(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
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

// TestSubagentManager_Sessions covers the sessions accessor.
func TestSubagentManager_Sessions_Empty(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	sessions := m.sessions()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestSubagentReconstructionWait covers the wait method (lines 71-73).
func TestSubagentReconstructionWait(t *testing.T) {
	t.Parallel()
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
