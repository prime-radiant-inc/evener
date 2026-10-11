package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A turn's end is stamped at the processing boundary with the session's own
// clock. The Board's Finished band measures from it (S4): unlike UpdatedAt it
// never moves on a rename or a mid-turn meta write.
func TestLastTurnEndedAt_StampedWhenATurnEnds(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}), withSteps(
		func(req llm.Request) llm.Response {
			clk.Advance(2 * time.Second)
			return finalResponse("ok")
		},
	))
	if got := sess.Meta().LastTurnEndedAt; !got.IsZero() {
		t.Fatalf("a session that has not run a turn reports %v", got)
	}
	// TRIPWIRE: scripted in-process adapter with a fake clock, no real I/O;
	// only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got, want := sess.Meta().LastTurnEndedAt, clk.Now().UTC(); !got.Equal(want) {
		t.Fatalf("LastTurnEndedAt = %v, want the boundary's %v", got, want)
	}
}

// A restored session still knows when its last turn ended, so a daemon restart
// neither turns a seen session unseen nor the reverse (S4).
func TestRestoreSeedsLastTurnEndedAt(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	ended := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	meta := schema.SessionMeta{
		ID:              "01RESTOREENDEDTIME000001",
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		CreatedAt:       ended.Add(-time.Hour),
		LastTurnEndedAt: ended,
	}
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	if got := sess.Meta().LastTurnEndedAt; !got.Equal(ended) {
		t.Fatalf("restored LastTurnEndedAt = %v, want %v", got, ended)
	}
}

// Close ends a turn that is still in flight without passing the processing
// boundary. That turn's end is stamped too, and Close's autosave persists it,
// so a daemon shut down mid-turn restores knowing when the turn ended (S4).
func TestLastTurnEndedAt_StampedWhenCloseEndsATurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, StateDir: dir}))
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.turnStartedAt = clk.Now()
	sess.mu.Unlock()
	clk.Advance(3 * time.Second)

	sess.Close()

	want := clk.Now().UTC()
	if got := sess.Meta().LastTurnEndedAt; !got.Equal(want) {
		t.Fatalf("LastTurnEndedAt after Close = %v, want the close's %v", got, want)
	}
	reloaded, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if !reloaded.LastTurnEndedAt.Equal(want) {
		t.Fatalf("on-disk LastTurnEndedAt after Close = %v, want %v", reloaded.LastTurnEndedAt, want)
	}
}
