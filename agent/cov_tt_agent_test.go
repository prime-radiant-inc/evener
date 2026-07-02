package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
)

// TestStartOnlyContextDelegatesDeadlineAndValue covers the Deadline and Value
// methods, which forward to the parent context.
func TestStartOnlyContextDelegatesDeadlineAndValue(t *testing.T) {
	type ctxKey struct{}
	deadline := time.Now().Add(time.Hour)
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), ctxKey{}, "v"), deadline)
	defer cancel()

	startCtx, detach := newStartOnlyContext(parent)
	defer detach()

	gotDeadline, ok := startCtx.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("Deadline() = (%v, %v), want (%v, true)", gotDeadline, ok, deadline)
	}
	if got := startCtx.Value(ctxKey{}); got != "v" {
		t.Fatalf("Value(ctxKey) = %v, want v", got)
	}
}

// TestCtxHostForwardsToSession covers the ctxHost adapter methods, which bridge
// a *Session to the contextmgr.Host seam.
func TestCtxHostForwardsToSession(t *testing.T) {
	sess := newSession(t)
	h := &ctxHost{s: sess}

	if h.Profile() == nil {
		t.Fatal("ctxHost.Profile() = nil, want the session profile")
	}
	if h.ID() != sess.ID() {
		t.Fatalf("ctxHost.ID() = %q, want %q", h.ID(), sess.ID())
	}
	if h.StateDir() != sess.StateDir() {
		t.Fatalf("ctxHost.StateDir() = %q, want %q", h.StateDir(), sess.StateDir())
	}

	ran := false
	if err := h.WithResponseSideEffects(context.Background(), func() { ran = true }); err != nil {
		t.Fatalf("ctxHost.WithResponseSideEffects() error = %v", err)
	}
	if !ran {
		t.Fatal("WithResponseSideEffects did not run the callback")
	}

	// Emit forwards to the session emitter; a fresh session with no hooks
	// configured routes the warning through the event stream without side effects.
	h.Emit(events.EventWarning, events.WarningData{Message: "cov-tt warning"})
}

// TestParseSpawnedAgentID covers the non-string, malformed-JSON, missing-ID, and
// success arms of parseSpawnedAgentID.
func TestParseSpawnedAgentID(t *testing.T) {
	if _, err := parseSpawnedAgentID(42); err == nil {
		t.Fatal("parseSpawnedAgentID(non-string) error = nil, want type failure")
	}
	if _, err := parseSpawnedAgentID("{not json"); err == nil {
		t.Fatal("parseSpawnedAgentID(bad json) error = nil, want parse failure")
	}
	if _, err := parseSpawnedAgentID(`{"agent_id":"  "}`); err == nil {
		t.Fatal("parseSpawnedAgentID(blank id) error = nil, want missing-id failure")
	}
	id, err := parseSpawnedAgentID(`{"agent_id":"child-1"}`)
	if err != nil || id != "child-1" {
		t.Fatalf("parseSpawnedAgentID(valid) = (%q, %v), want (child-1, nil)", id, err)
	}
}

// TestIsResultToolDefinition covers the communicate-default, wire-name match,
// and no-match arms.
func TestIsResultToolDefinition(t *testing.T) {
	if !isResultToolDefinition("communicate", "anything", "") {
		t.Fatal("canonical communicate should be a result tool")
	}
	if !isResultToolDefinition("other", "report", "report") {
		t.Fatal("wire name matching the result tool name should be a result tool")
	}
	if isResultToolDefinition("other", "wire", "report") {
		t.Fatal("non-matching tool should not be a result tool")
	}
	// Empty result tool name defaults to "communicate".
	if !isResultToolDefinition("other", "communicate", "") {
		t.Fatal("empty result tool name should default to communicate")
	}
}
