package agent

import (
	"context"
	"testing"
)

func TestCompactTool_PinsAndRequests(t *testing.T) {
	s := newTestSession(t)
	rt := s.reg.Get("compact")
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
	if s.PinnedNote() != "keep the migration plan" {
		t.Fatalf("note not pinned: got %q", s.PinnedNote())
	}
	instr, ok := s.takeForceRequest()
	if !ok || instr != "drop the build logs" {
		t.Fatalf("force not requested: %q %v", instr, ok)
	}
}

func TestCompactTool_ClearNote_NoForce(t *testing.T) {
	s := newTestSession(t)
	s.setPinnedNote("old")
	rt := s.reg.Get("compact")
	if rt == nil {
		t.Fatal("compact tool not registered")
	}
	if _, err := rt.Exec(context.Background(), nil, map[string]any{"note_to_self": ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if s.PinnedNote() != "" {
		t.Fatalf("empty note should clear, got %q", s.PinnedNote())
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("clearing a note must not force a compaction")
	}
}
