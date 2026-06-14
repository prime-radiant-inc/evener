package server

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestHandleAppThreadReasoningEffortSet_CallsFuncWithTrimmedValue(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	called := false
	s.SetReasoningEffortFunc(func(e string) { got = e; called = true })

	if _, err := s.handleAppThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "  high  "}); err != nil {
		t.Fatalf("handleAppThreadReasoningEffortSet: %v", err)
	}
	if !called {
		t.Fatal("reasoning-effort func was not called")
	}
	if got != "high" {
		t.Fatalf("func got %q, want trimmed %q", got, "high")
	}
}

func TestHandleAppThreadReasoningEffortSet_RejectsUnknownEffort(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetReasoningEffortFunc(func(string) { called = true })

	if _, err := s.handleAppThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "hihg"}); err == nil {
		t.Fatal("expected an error for an unknown reasoning-effort value")
	}
	if called {
		t.Fatal("the setter must not be called for an invalid value")
	}
}

func TestHandleAppThreadReasoningEffortSet_NoneNormalizesToEmpty(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	called := false
	s.SetReasoningEffortFunc(func(e string) { got = e; called = true })

	if _, err := s.handleAppThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "none"}); err != nil {
		t.Fatalf("handleAppThreadReasoningEffortSet: %v", err)
	}
	if !called || got != "" {
		t.Fatalf("none should normalize to empty (clear); called=%v got=%q", called, got)
	}
}

func TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset(t *testing.T) {
	s := NewServer(ServerConfig{})
	if _, err := s.handleAppThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "high"}); err == nil {
		t.Fatal("expected an unavailable error when the reasoning-effort func is unset")
	}
}
