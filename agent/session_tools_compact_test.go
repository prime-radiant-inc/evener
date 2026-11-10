package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
)

func TestCompactTool_PinsAndRequests(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	forcePressureAbove(t, s, compactPressureFloor)
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
	forcePressureAbove(t, s, compactPressureFloor)
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

func TestCompactContextBelowPressureFloorDoesNotSchedule(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	forcePressureAbove(t, s, compactPressureFloor)
	rt := s.reg.Get("compact_context")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}

	// A fresh session sits far below the floor: the call is a no-op.
	s2 := newTestSession(t)
	rt2 := s2.reg.Get("compact_context")
	out, err := rt2.Exec(context.Background(), nil, map[string]any{
		"note_to_self":            "keep the plan",
		"compaction_instructions": "drop logs",
	})
	if err != nil {
		t.Fatalf("below floor must not error: %v", err)
	}
	if _, ok := s2.takeForceRequest(); ok {
		t.Fatal("below the pressure floor no compaction may be scheduled")
	}
	if got := s2.pinnedNote(); got != "" {
		t.Fatalf("a no-op must not pin a note, got %q", got)
	}
	state, ok := out.(tool.StateResult)
	if !ok {
		t.Fatalf("result type = %T", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(state.Output), &decoded); err != nil {
		t.Fatalf("not-needed result must be structured JSON: %v", err)
	}
	if decoded["compacted"] != false {
		t.Fatalf("compacted = %v, want false", decoded["compacted"])
	}
	if _, ok := decoded["pressure"].(float64); !ok {
		t.Fatalf("result must carry the current pressure: %v", decoded)
	}

	// Above the floor the call schedules as before.
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": "n"}); err != nil {
		t.Fatalf("above floor: %v", err)
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("above the floor a compaction must be scheduled")
	}
}

func TestCompactContextBelowFloorStillClearsNote(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.setPinnedNote("old")
	rt := s.reg.Get("compact_context")
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if s.pinnedNote() != "" {
		t.Fatalf("clearing is not a compaction request and works at any pressure, got %q", s.pinnedNote())
	}
}
