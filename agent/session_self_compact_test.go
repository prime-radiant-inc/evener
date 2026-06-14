package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
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

// forcePressureAbove drives the context manager's reported pressure to at least
// frac by recording a large exact input-token count for the current history
// length. The compaction layers reset lastInputTokens to 0, so this must be
// re-applied after each compaction to re-arm pressure.
func forcePressureAbove(t *testing.T, s *Session, frac float64) {
	t.Helper()
	cw := s.contextMgr.EstimateUsage(nil, 0).Window
	if cw <= 0 {
		t.Fatalf("context window size is %d; cannot force pressure", cw)
	}
	s.mu.Lock()
	histLen := len(s.history)
	s.mu.Unlock()
	tokens := int(frac*float64(cw)) + 1
	s.contextMgr.RecordInputTokens(tokens, histLen)
	if got := s.contextMgr.Pressure(currentHistory(t, s), 0); got < frac {
		t.Fatalf("forcePressureAbove: pressure %.3f < target %.3f", got, frac)
	}
}

func TestNudge_FiresOnceUntilCompaction(t *testing.T) {
	s := newTestSession(t)
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)

	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire when pressure crosses WarnThreshold and latch is clear")
	}
	// The nudge must reach the model: it is queued as steering, which the round
	// loop drains into history before the next model call.
	if got := s.SteeringQueueSnapshot(); len(got) != 1 || !strings.Contains(got[0].Text, "compact") {
		t.Fatalf("nudge did not queue a steering message: %+v", got)
	}
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge must not re-fire until after a compaction")
	}

	// A compaction resets the latch.
	if err := s.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	s.applyPendingForceCompact(context.Background())
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire again after a compaction reset the latch")
	}
}

// TestNudge_ResetsOnSessionCompact verifies Session.Compact (the idle/explicit
// path, distinct from applyPendingForceCompact) also clears the latch so the
// nudge can fire again afterward.
func TestNudge_ResetsOnSessionCompact(t *testing.T) {
	s := newTestSession(t)
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire on first crossing")
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	forcePressureAbove(t, s, s.contextMgr.WarnThreshold)
	if !s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge should fire again after Session.Compact reset the latch")
	}
}

// TestNudge_ResetsOnAutomaticCompaction verifies that the nudge latch resets via
// the shared compactionEmitFunc path — the same emit site used by ALL compaction
// paths (auto ManageContext, content-filter recovery, and both force paths).
// Driving the shared emit site directly is the correct test: it proves the reset
// is wired to the mechanism that every path flows through, not just the force paths.
func TestNudge_ResetsOnAutomaticCompaction(t *testing.T) {
	s := newTestSession(t)

	// Arm the latch as if a nudge already fired.
	s.mu.Lock()
	s.nudgedSinceCompact = true
	s.mu.Unlock()

	// Invoke compactionEmitFunc and fire EventContextCompaction through it.
	// This is the shared emit site that all compaction paths (auto, content-filter,
	// and force) route through; the latch reset must live here.
	hist := makeSteeringSeed(2)
	emitFn, flush := s.compactionEmitFunc(context.Background(), &hist)
	emitFn(events.EventContextCompaction, events.ContextCompactionData{Layer: "test"})
	flush()

	s.mu.Lock()
	stuck := s.nudgedSinceCompact
	s.mu.Unlock()
	if stuck {
		t.Fatal("nudge latch must reset when EventContextCompaction flows through compactionEmitFunc (auto-compaction path)")
	}
}

// TestNudge_SilentBelowThreshold verifies no nudge fires when pressure is below
// WarnThreshold and the latch stays clear.
func TestNudge_SilentBelowThreshold(t *testing.T) {
	s := newTestSession(t)
	if s.maybeNudgeSelfCompact(0) {
		t.Fatal("nudge must not fire below WarnThreshold")
	}
	s.mu.Lock()
	latched := s.nudgedSinceCompact
	s.mu.Unlock()
	if latched {
		t.Fatal("latch must stay clear when pressure is below threshold")
	}
}

// TestPinnedNote_MetaRoundTrip verifies that setPinnedNote is captured by Meta()
// and survives a JSON marshal/unmarshal round-trip (the wire format used by
// SaveSessionMeta/LoadSessionMeta).
func TestPinnedNote_MetaRoundTrip(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("REMEMBER: resume me")
	meta := s.Meta()
	if meta.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("meta.PinnedNote = %q", meta.PinnedNote)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var back schema.SessionMeta
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.PinnedNote != "REMEMBER: resume me" {
		t.Fatalf("json round-trip lost the note: %q", back.PinnedNote)
	}
}

// TestPinnedNote_SurvivesResume verifies that PinnedNote is restored when a
// session is reconstructed via RestoreSessionFromMeta. It uses the same
// RestoreSessionFromMeta helper that the real resume path uses, with a
// SessionMeta carrying PinnedNote set directly (mirroring how
// LoadSessionMeta would return it after SaveSessionMeta wrote it).
func TestPinnedNote_SurvivesResume(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	stateDir := t.TempDir()

	meta := schema.SessionMeta{
		ID:         "resume-pinned-note",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		PinnedNote: "REMEMBER: resume me",
	}

	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()

	if got := restored.PinnedNote(); got != "REMEMBER: resume me" {
		t.Fatalf("PinnedNote after resume = %q, want %q", got, "REMEMBER: resume me")
	}
}
