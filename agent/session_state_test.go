package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
)

func TestSessionAwaitingStringIsWireAwaiting(t *testing.T) {
	// The string is load-bearing: SessionProcessing is "active", and every
	// status switch on the wire journey defaults unknown strings to idle.
	if got := string(SessionAwaiting); got != "awaiting" {
		t.Fatalf("SessionAwaiting = %q, want %q", got, "awaiting")
	}
}

// TestMeta_CreatedAtStableAcrossCalls pins the WS2 A2 fix: Meta().CreatedAt
// must reflect the session's true creation time and stay stable across
// repeated calls (i.e. across autosaves), not get re-stamped to "now" every
// time Meta() is called. UpdatedAt, by contrast, is expected to keep tracking
// the clock.
func TestMeta_CreatedAtStableAcrossCalls(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))

	first := sess.Meta()

	clk.Advance(time.Hour)

	second := sess.Meta()

	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed across Meta() calls: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt did not advance: first=%v second=%v", first.UpdatedAt, second.UpdatedAt)
	}
}
