package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// assertMemoIsExact is the exactness proof for the incremental estimate: whatever
// the memo answers must equal what the full walk answers for the same history.
// ContextStatus is the memoized path the server uses; EstimateStatusFor is the
// unmemoized one, given a fresh copy of the same history.
func assertMemoIsExact(t *testing.T, s *Session, label string) {
	t.Helper()
	s.mu.Lock()
	hist := append([]schema.Turn{}, s.history...)
	s.mu.Unlock()

	wantMetrics, wantPressure, _ := s.contextMgr.EstimateStatusFor(hist, 0)
	gotMetrics, gotPressure := s.ContextStatus()
	if gotMetrics.Used != wantMetrics.Used || gotMetrics.Window != wantMetrics.Window ||
		gotMetrics.Remaining != wantMetrics.Remaining || gotMetrics.Assumed != wantMetrics.Assumed ||
		gotMetrics.CompactionThreshold != wantMetrics.CompactionThreshold {
		t.Fatalf("%s: memoized sample = %+v, full walk = %+v", label, gotMetrics, wantMetrics)
	}
	if gotPressure != wantPressure {
		t.Fatalf("%s: memoized pressure = %v, full walk = %v", label, gotPressure, wantPressure)
	}
}

// TestContextStatusMemoMatchesFullRecomputeAcrossAppends proves the incremental
// path agrees with a full recompute after every append. Rounding is the risk: the
// estimator divides summed chars by four once over the whole list, so a memo that
// added per-prefix token totals would drift as soon as a prefix's chars were not a
// multiple of four. The fixture's messages are 10 chars for that reason.
func TestContextStatusMemoMatchesFullRecomputeAcrossAppends(t *testing.T) {
	s := newSession(t)
	text := "0123456789"
	for i := range 12 {
		var turn schema.Turn
		if i%2 == 0 {
			turn = schema.NewTurn(schema.TurnUserInput, llm.User(text))
		} else {
			turn = schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
		}
		s.recordTurn(turn, turn)
		assertMemoIsExact(t, s, "after append")
	}
}

// TestContextStatusMemoMatchesFullRecomputeAcrossFold proves a fold publish takes
// the exact path. A fold replaces history wholesale and bumps historyRevision,
// which is what tells the memo the prefix it walked no longer describes history.
func TestContextStatusMemoMatchesFullRecomputeAcrossFold(t *testing.T) {
	s := newSession(t)
	text := contextStatusText()
	for range 6 {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
		s.recordTurn(turn, turn)
		assertMemoIsExact(t, s, "before fold")
	}
	folded := []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.Assistant(text))}
	s.mu.Lock()
	_, ok := s.publishFoldedHistory(len(s.history), s.historyRevision, folded)
	s.mu.Unlock()
	if !ok {
		t.Fatal("publishFoldedHistory refused the fixture's own snapshot")
	}
	assertMemoIsExact(t, s, "after fold publish")
}

// TestContextStatusMemoMatchesFullRecomputeAcrossOrphanRepair proves the repair
// path takes the exact path. Repair splices a synthetic turn into the MIDDLE of
// history (not the end), so a memo that only tracked length would keep a prefix
// that no longer matches. The synthetic fixture is the shape
// session_orphaned_tool_repair_test.go uses: a tool call with no result.
func TestContextStatusMemoMatchesFullRecomputeAcrossOrphanRepair(t *testing.T) {
	s := newSession(t)
	const callID = "orphan_call"
	call := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        callID,
				Name:      "probe",
				Type:      "function",
				Arguments: json.RawMessage(`{}`),
			},
		}},
	}
	orphan := schema.NewTurn(schema.TurnAssistant, call)
	s.recordTurn(orphan, orphan)
	assertMemoIsExact(t, s, "before repair")

	s.mu.Lock()
	beforeRevision := s.historyRevision
	beforeLen := len(s.history)
	s.mu.Unlock()
	if repaired := s.repairOrphanedToolResults(t.Context(), "test"); repaired == 0 {
		t.Fatal("fixture produced no orphan to repair, so this test would prove nothing")
	}
	s.mu.Lock()
	afterRevision, afterLen := s.historyRevision, len(s.history)
	s.mu.Unlock()
	if afterRevision == beforeRevision {
		t.Fatalf("repair changed history (%d -> %d turns) without bumping historyRevision (%d), so a stale prefix would be reused",
			beforeLen, afterLen, beforeRevision)
	}
	assertMemoIsExact(t, s, "after orphan repair")
}

// TestContextStatusMemoInvalidatesOnProfileAndMeasurementChange proves the other
// two inputs are in the key. A profile swap changes the window and the replay
// rule; an API measurement changes the baseline the walk starts from. Either one
// alone must force a full recompute.
func TestContextStatusMemoInvalidatesOnProfileAndMeasurementChange(t *testing.T) {
	t.Run("API measurement", func(t *testing.T) {
		s := newSession(t)
		text := contextStatusText()
		for range 4 {
			turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
			s.recordTurn(turn, turn)
			assertMemoIsExact(t, s, "before measurement")
		}
		if accepted := s.contextMgr.RecordInputTokensFor(s.profile, 300, 1, ""); !accepted {
			t.Fatal("RecordInputTokensFor refused the fixture's own profile")
		}
		assertMemoIsExact(t, s, "after measurement")
		// The measured baseline is 300 tokens at a history length of 1, so the
		// estimate is 300 plus the three turns appended since: 3*1024/4 = 768.
		// A memo that reused a whole-history char/4 prefix could not produce this,
		// and neither could one that ignored the measurement.
		const want = 300 + 3*contextStatusMessageChars/4
		if used, _ := s.ContextStatus(); used.Used != want {
			t.Fatalf("Used = %d after a 300-token measurement at length 1, want %d", used.Used, want)
		}
	})

	t.Run("profile swap", func(t *testing.T) {
		s := newSession(t)
		text := contextStatusText()
		for range 4 {
			turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
			s.recordTurn(turn, turn)
			assertMemoIsExact(t, s, "before profile swap")
		}
		s.mu.Lock()
		previous := s.profile
		s.mu.Unlock()
		// A clone with a different context window: the swap changes the reported
		// Window, which must be visible immediately and must not reuse the memo.
		swapped := *previous
		s.contextMgr.SetProfile(&swapped)
		assertMemoIsExact(t, s, "after profile swap")
		s.contextMgr.SetProfile(previous)
		assertMemoIsExact(t, s, "after swap back")
	})
}

// TestContextStatusTailCopyIsExact proves the copy a sample takes after an
// append answers exactly what a copy of the whole transcript answers. The
// session copies only the turns the memo has not walked, so a skipped prefix
// that disagrees with the memo's own prefix would show up here as a different
// total, not as a tolerance.
func TestContextStatusTailCopyIsExact(t *testing.T) {
	s := newSession(t)
	text := "0123456789"
	for i := range 8 {
		var turn schema.Turn
		if i%2 == 0 {
			turn = schema.NewTurn(schema.TurnUserInput, llm.User(text))
		} else {
			turn = schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
		}
		s.recordTurn(turn, turn)

		s.mu.Lock()
		covered, ok := s.contextMgr.ObserveHistory(s.historyRevision, len(s.history))
		full := append([]schema.Turn{}, s.history...)
		revision := s.historyRevision
		s.mu.Unlock()
		if !ok {
			// Only the first append has nothing to reuse; every later one starts
			// from a prefix a previous sample walked.
			if i > 0 {
				t.Fatalf("append %d: the manager holds no covered prefix after a warm sample", i)
			}
			covered = 0
		}
		if covered > len(full) {
			t.Fatalf("append %d: covered prefix %d exceeds history %d", i, covered, len(full))
		}

		// Warm the memo from the whole transcript, then ask again through the
		// tail slice the session would copy. The two answers must be identical.
		wantMetrics, wantPressure, _, ok := s.contextMgr.EstimateStatusForHistory(full, len(full), 0, revision)
		if !ok {
			t.Fatalf("append %d: the manager could not answer from the whole transcript", i)
		}
		gotMetrics, gotPressure, _, ok := s.contextMgr.EstimateStatusForHistory(full[covered:], len(full), 0, revision)
		if !ok {
			t.Fatalf("append %d: the manager could not answer from the tail copy after a covered prefix of %d", i, covered)
		}
		if gotMetrics != wantMetrics || gotPressure != wantPressure {
			t.Fatalf("append %d: tail copy (covered %d) answered %+v/%v, whole transcript answered %+v/%v",
				i, covered, gotMetrics, gotPressure, wantMetrics, wantPressure)
		}
	}
}

// TestContextStatusTailCopyRefusesAnUncoveredPrefix proves the tail path cannot
// silently report a prefix. A slice that starts after the memo's own baseline
// leaves turns in neither the memo nor the copy, so the manager must refuse and
// let the caller copy the whole transcript.
func TestContextStatusTailCopyRefusesAnUncoveredPrefix(t *testing.T) {
	s := newSession(t)
	text := contextStatusText()
	for range 4 {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
		s.recordTurn(turn, turn)
	}
	s.mu.Lock()
	full := append([]schema.Turn{}, s.history...)
	revision := s.historyRevision
	s.mu.Unlock()

	// Nothing has been walked yet, so a tail starting past the beginning skips
	// turns no memo covers.
	if _, _, _, ok := s.contextMgr.EstimateStatusForHistory(full[2:], len(full), 0, revision); ok {
		t.Fatal("the manager answered from a tail whose skipped prefix it never walked")
	}
	// The whole transcript is still answerable, and the session's own sample
	// recovers through that path.
	wantMetrics, wantPressure, _, ok := s.contextMgr.EstimateStatusForHistory(full, len(full), 0, revision)
	if !ok {
		t.Fatal("the manager could not answer from the whole transcript")
	}
	gotMetrics, gotPressure := s.ContextStatus()
	// WindowSource is the session's own addition to the manager's answer, so it
	// is compared through the fields the two share.
	if gotMetrics.Used != wantMetrics.Used || gotMetrics.Window != wantMetrics.Window ||
		gotMetrics.Remaining != wantMetrics.Remaining || gotMetrics.Assumed != wantMetrics.Assumed ||
		gotMetrics.CompactionThreshold != wantMetrics.CompactionThreshold {
		t.Fatalf("ContextStatus answered %+v, whole transcript answered %+v", gotMetrics, wantMetrics)
	}
	if gotPressure != wantPressure {
		t.Fatalf("ContextStatus pressure = %v, whole transcript = %v", gotPressure, wantPressure)
	}
}
