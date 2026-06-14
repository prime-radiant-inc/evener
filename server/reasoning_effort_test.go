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

func TestHandleAppThreadReasoningEffortSet_UnavailableWhenFuncUnset(t *testing.T) {
	s := NewServer(ServerConfig{})
	if _, err := s.handleAppThreadReasoningEffortSet(context.Background(), appwire.ThreadReasoningEffortSetParams{ReasoningEffort: "high"}); err == nil {
		t.Fatal("expected an unavailable error when the reasoning-effort func is unset")
	}
}
