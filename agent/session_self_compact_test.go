package agent

import (
	"testing"
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
