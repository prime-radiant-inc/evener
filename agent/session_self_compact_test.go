package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSetPinnedNote_AndClear(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("remember the API signature")
	if got := s.PinnedNote(); got != "remember the API signature" {
		t.Fatalf("note not stored: %q", got)
	}
	s.setPinnedNote("")
	if got := s.PinnedNote(); got != "" {
		t.Fatalf("empty note should clear: %q", got)
	}
}

func TestRequestForceCompact_OnePerRound(t *testing.T) {
	s := newTestSession(t)
	if err := s.requestForceCompact("drop logs"); err != nil {
		t.Fatalf("first request should succeed: %v", err)
	}
	if err := s.requestForceCompact("drop more"); err == nil {
		t.Fatal("second request in the same round must error")
	}
	instr, ok := s.takeForceRequest()
	if !ok || instr != "drop logs" {
		t.Fatalf("takeForceRequest = %q,%v", instr, ok)
	}
	if err := s.requestForceCompact("next round"); err != nil {
		t.Fatalf("after consume, a new request should succeed: %v", err)
	}
}

// makeSteeringSeed builds a slice of n ordinary (non-steering) turns for use as
// the history seed in runPreCompactHook tests.
func makeSteeringSeed(n int) []schema.Turn {
	turns := make([]schema.Turn, n)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.User("turn"))
	}
	return turns
}

// indexOfSteering returns the index of the first TurnSteering turn whose text
// contains substr, or -1 if not found.
func indexOfSteering(history []schema.Turn, substr string) int {
	for i, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), substr) {
			return i
		}
	}
	return -1
}

// countSteering counts TurnSteering turns whose text contains substr.
func countSteering(history []schema.Turn, substr string) int {
	n := 0
	for _, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), substr) {
			n++
		}
	}
	return n
}

// TestRunPreCompactHook_StampsNoteBeforeObjective verifies that when both a
// pinned note and an active goal are set, runPreCompactHook appends a note
// steering turn that (a) is present, and (b) precedes the goal objective turn
// so the objective stays in the trailing/strongest-recency position.
func TestRunPreCompactHook_StampsNoteBeforeObjective(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	s.getOrCreateGoalStore().Set("Ship the feature", time.Now())

	hist := makeSteeringSeed(4)
	s.runPreCompactHook(context.Background(), &hist)

	noteIdx := indexOfSteering(hist, pinnedNoteOpen)
	goalIdx := indexOfSteering(hist, "Ship the feature")
	if noteIdx < 0 {
		t.Fatal("note not stamped")
	}
	if goalIdx >= 0 && noteIdx > goalIdx {
		t.Fatal("note must precede the goal objective (objective stays trailing)")
	}
}

// TestRunPreCompactHook_NoDuplicateNote verifies the exactly-one-note invariant:
// calling runPreCompactHook twice must leave exactly one note steering turn.
func TestRunPreCompactHook_NoDuplicateNote(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: do X")
	hist := makeSteeringSeed(4)
	s.runPreCompactHook(context.Background(), &hist)
	s.runPreCompactHook(context.Background(), &hist)
	if n := countSteering(hist, pinnedNoteOpen); n != 1 {
		t.Fatalf("expected exactly one note turn, got %d", n)
	}
}

// seedSessionHistory appends n ordinary TurnUserInput turns to s.history under
// s.mu. This gives the compaction layers enough history to exercise the
// checkpoint path (checkpoint preserves only the recent PreserveRecentTurns).
func seedSessionHistory(t *testing.T, s *Session, n int) {
	t.Helper()
	s.mu.Lock()
	for i := 0; i < n; i++ {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User("turn")))
	}
	s.mu.Unlock()
}

// currentHistory returns a snapshot of s.history under s.mu.
func currentHistory(t *testing.T, s *Session) []schema.Turn {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn{}, s.history...)
}

func TestApplyPendingForceCompact_CompactsWithNote(t *testing.T) {
	s := newTestSession(t)
	seedSessionHistory(t, s, 14) // >PreserveRecentTurns ordinary turns
	s.setPinnedNote("REMEMBER: API is Foo(ctx, id)")
	if err := s.requestForceCompact("drop the file dumps"); err != nil {
		t.Fatal(err)
	}

	s.applyPendingForceCompact(context.Background())

	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("force request should be consumed by applyPendingForceCompact")
	}
	h := currentHistory(t, s)
	if countSteering(h, pinnedNoteOpen) != 1 || indexOfSteering(h, "REMEMBER: API is Foo(ctx, id)") < 0 {
		t.Fatal("pinned note not re-stamped exactly once after force compaction")
	}
}

func TestApplyPendingForceCompact_NoRequest_NoOp(t *testing.T) {
	s := newTestSession(t)
	seedSessionHistory(t, s, 14)
	before := len(currentHistory(t, s))
	s.applyPendingForceCompact(context.Background()) // no pending request
	if len(currentHistory(t, s)) != before {
		t.Fatal("with no pending request, applyPendingForceCompact must be a no-op")
	}
}
