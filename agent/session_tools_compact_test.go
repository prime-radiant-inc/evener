package agent

import (
	"context"
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestCompactTool_PinsAndRequests(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"note_to_self":            "keep the migration plan",
		"compaction_instructions": "drop the build logs",
	})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	if s.pinnedNote() != "keep the migration plan" {
		t.Fatalf("note not pinned: got %q", s.pinnedNote())
	}
	instr, ok := s.takeForceRequest()
	if !ok || instr != "drop the build logs" {
		t.Fatalf("force not requested: %q %v", instr, ok)
	}
}

func TestCompactTool_ClearNote_NoForce(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("old")
	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if s.pinnedNote() != "" {
		t.Fatalf("empty note should clear, got %q", s.pinnedNote())
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("clearing a note must not force a compaction")
	}
}

func TestCompactTool_DoubleCompact_Errors(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rt := s.reg.Get("compact_context")
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": "a"}); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": "b"}); err == nil {
		t.Fatal("second compact in the same round must error")
	}
	// The rejected second call must not have clobbered the accepted first note.
	if got := s.pinnedNote(); got != "a" {
		t.Fatalf("rejected double-call mutated the note: got %q, want %q", got, "a")
	}
}

func TestCompactTool_LowPressurePredictionReflectsFoldableHistory(t *testing.T) {
	t.Parallel()
	// 1. With empty history (no foldable turns) and light pressure, it matches predictionMessage for non-foldable light context.
	sLight := newTestSession(t)
	if sLight.hasFoldableHistory() {
		t.Fatal("expected empty history to report no foldable turns")
	}
	rtLight := sLight.reg.Get("compact_context")
	if rtLight == nil {
		t.Fatal("compact tool not registered")
	}
	resLight, err := rtLight.Exec(context.Background(), nil, map[string]any{"note_to_self": "pinned note"})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	srLight, ok := resLight.(tool.StateResult)
	if !ok {
		t.Fatalf("expected StateResult, got %T", resLight)
	}
	wantLight := predictionMessage(false, sLight.contextPressure(), false)
	if srLight.Output != wantLight {
		t.Fatalf("srLight.Output = %q, want %q", srLight.Output, wantLight)
	}

	// 2. With history larger than PreserveRecentTurns (e.g. 10 turns > 6), even at low pressure (< 0.30),
	// it must match predictionMessage for seam compaction and differ from the light non-foldable prediction.
	sHeavy := newTestSession(t)
	sHeavy.mu.Lock()
	for i := range 10 {
		sHeavy.history = append(sHeavy.history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("turn %d", i))))
	}
	sHeavy.mu.Unlock()
	if !sHeavy.hasFoldableHistory() {
		t.Fatal("expected 10-turn history to report foldable turns")
	}

	rtHeavy := sHeavy.reg.Get("compact_context")
	if rtHeavy == nil {
		t.Fatal("compact tool not registered")
	}
	resHeavy, err := rtHeavy.Exec(context.Background(), nil, map[string]any{"note_to_self": "pinned note 2"})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	srHeavy, ok := resHeavy.(tool.StateResult)
	if !ok {
		t.Fatalf("expected StateResult, got %T", resHeavy)
	}
	if srHeavy.Output == srLight.Output {
		t.Fatalf("foldable history output must differ from non-foldable output: %q", srHeavy.Output)
	}
	wantHeavy := predictionMessage(false, 0.50, true)
	if srHeavy.Output != wantHeavy {
		t.Fatalf("srHeavy.Output = %q, want seam compaction prediction %q", srHeavy.Output, wantHeavy)
	}
}

func TestCompactTool_LowPressurePredictionAtPreserveRecentBoundary(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	// Exactly 6 turns (equal to PreserveRecentTurns):
	// During tool execution, TurnAssistant is turn 6, and TurnToolResults will be turn 7
	// before compaction runs at the seam. hasFoldableHistory must account for it.
	s.mu.Lock()
	for i := range 6 {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("turn %d", i))))
	}
	s.mu.Unlock()

	if !s.hasFoldableHistory() {
		t.Fatal("expected 6-turn history to report foldable turns accounting for in-flight tool result")
	}

	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	res, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": "boundary note"})
	if err != nil {
		t.Fatalf("compact exec: %v", err)
	}
	sr, ok := res.(tool.StateResult)
	if !ok {
		t.Fatalf("expected StateResult, got %T", res)
	}
	want := predictionMessage(false, 0.50, true)
	if sr.Output != want {
		t.Fatalf("at boundary len==6, sr.Output = %q, want seam compaction prediction %q", sr.Output, want)
	}
}
