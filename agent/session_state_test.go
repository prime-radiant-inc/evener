package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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

// TestRestoreSeedsMetricsIntoMeta pins the WS2 A3 K2-seeding-half fix: restoring
// a session from a SessionMeta carrying persisted WorkMillis/CumulativeUsage
// must seed those totals into the live session so Meta(), read immediately
// after restore (before any autosave has a chance to run), reflects the
// persisted values rather than a freshly constructed session's zeroes.
func TestRestoreSeedsMetricsIntoMeta(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	meta := schema.SessionMeta{
		ID:         "01RESTOREMETRICSSEED0001",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		CreatedAt:  time.Now().UTC(),
		WorkMillis: 5000,
		CumulativeUsage: schema.CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    200,
			CacheReadTokens: 50,
			TotalTokens:     300,
		},
	}

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), env, meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	got := sess.Meta()
	if got.WorkMillis != 5000 {
		t.Fatalf("Meta().WorkMillis = %d, want 5000", got.WorkMillis)
	}
	if got.CumulativeUsage != meta.CumulativeUsage {
		t.Fatalf("Meta().CumulativeUsage = %+v, want %+v", got.CumulativeUsage, meta.CumulativeUsage)
	}
}
