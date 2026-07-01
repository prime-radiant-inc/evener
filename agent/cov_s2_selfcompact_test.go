package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func s2cov_seedHistory(sess *Session, n int) []schema.Turn {
	hist := make([]schema.Turn, 0, n)
	for i := 0; i < n; i++ {
		hist = append(hist, schema.NewTurn(schema.TurnUserInput, llm.User("turn")))
	}
	sess.mu.Lock()
	sess.history = hist
	sess.mu.Unlock()
	return hist
}

// TestS2Cov_MaybeElicitNoteBeforeCompaction_PinsElicitedNote drives the forced-
// note elicitation happy path, the already-pinned skip, and the elicit-error
// warning branch.
func TestS2Cov_MaybeElicitNoteBeforeCompaction_PinsElicitedNote(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	go func() {
		for range sess.Events() {
		}
	}()
	// Force "compaction imminent" and a non-empty foldable prefix.
	sess.contextMgr.CheckpointThreshold = 0
	sess.contextMgr.PreserveRecentTurns = 0
	hist := s2cov_seedHistory(sess, 3)

	var gotFoldable int
	sess.elicitNoteFn = func(_ context.Context, foldable []schema.Turn) (string, error) {
		gotFoldable = len(foldable)
		return "KEEP_THIS_VERBATIM", nil
	}
	sess.maybeElicitNoteBeforeCompaction(context.Background(), hist, 0)
	if gotFoldable != 3 {
		t.Fatalf("foldable len = %d, want 3", gotFoldable)
	}
	if got := sess.PinnedNote(); got != "KEEP_THIS_VERBATIM" {
		t.Fatalf("PinnedNote = %q, want elicited note", got)
	}

	// A second call is a no-op: the note is already pinned (per-cycle latch).
	sess.elicitNoteFn = func(_ context.Context, _ []schema.Turn) (string, error) {
		t.Fatal("elicit must not run when a note is already pinned")
		return "", nil
	}
	sess.maybeElicitNoteBeforeCompaction(context.Background(), hist, 0)
}

func TestS2Cov_MaybeElicitNoteBeforeCompaction_ErrorWarns(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	var mu chanCollector
	go mu.drain(sess)

	sess.contextMgr.CheckpointThreshold = 0
	sess.contextMgr.PreserveRecentTurns = 0
	hist := s2cov_seedHistory(sess, 2)
	sess.elicitNoteFn = func(_ context.Context, _ []schema.Turn) (string, error) {
		return "", errors.New("boom")
	}
	sess.maybeElicitNoteBeforeCompaction(context.Background(), hist, 0)
	if got := sess.PinnedNote(); got != "" {
		t.Fatalf("PinnedNote = %q, want empty after elicit error", got)
	}
	sess.Close()
	if !mu.contains("note elicitation failed") {
		t.Fatalf("no elicitation-failed warning; warnings = %v", mu.messages())
	}
}

// TestS2Cov_MaybeNudgeSelfCompact_NudgesOnceThenLatches covers the WarnThreshold
// nudge and its per-compaction latch.
func TestS2Cov_MaybeNudgeSelfCompact_NudgesOnceThenLatches(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	go func() {
		for range sess.Events() {
		}
	}()
	sess.contextMgr.WarnThreshold = 0
	s2cov_seedHistory(sess, 2)

	if !sess.maybeNudgeSelfCompact(0) {
		t.Fatal("first nudge did not fire")
	}
	if sess.maybeNudgeSelfCompact(0) {
		t.Fatal("second nudge fired despite latch")
	}
	steered := sess.drainSteering()
	if len(steered) != 1 || !strings.Contains(steered[0].Text, "Context is filling up") {
		t.Fatalf("steered = %+v, want a single self-compact nudge", steered)
	}
}

// chanCollector accumulates warning messages from a session's event stream.
type chanCollector struct {
	mu   sync.Mutex
	msgs []string
}

func (c *chanCollector) drain(sess *Session) {
	for ev := range sess.Events() {
		if ev.Kind == events.EventWarning {
			if w, ok := ev.Data.(events.WarningData); ok {
				c.mu.Lock()
				c.msgs = append(c.msgs, w.Message)
				c.mu.Unlock()
			}
		}
	}
}

func (c *chanCollector) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func (c *chanCollector) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}
