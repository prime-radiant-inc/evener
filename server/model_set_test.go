package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestHandleAppThreadModelSet_RejectsWhileProcessing(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetModelFunc(func(string) error { called = true; return nil })
	s.SetProcessing(true)

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"}); err == nil {
		t.Fatal("expected an error while a turn is processing")
	}
	if called {
		t.Fatal("model hook must not be invoked while a turn is active")
	}
}

func TestHandleAppThreadModelSet_RejectsWhileTurnReserved_NamesTurnID(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetModelFunc(func(string) error { called = true; return nil })
	s.appActiveTurnID = "turn_abc123"
	s.appReservedTurnID = "turn_abc123"

	_, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err == nil {
		t.Fatal("expected an error while a turn is reserved")
	}
	if called {
		t.Fatal("model hook must not be invoked while a turn is reserved")
	}
	if !strings.Contains(err.Error(), "turn_abc123") {
		t.Fatalf("error should name the active turn id, got: %v", err)
	}
}

func TestHandleAppThreadModelSet_HookErrorSurfacesAsWireError(t *testing.T) {
	s := NewServer(ServerConfig{})
	hookErr := errors.New("unknown instance: nope")
	s.SetModelFunc(func(string) error { return hookErr })

	_, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err == nil {
		t.Fatal("expected an error from the hook")
	}
	if !strings.Contains(err.Error(), hookErr.Error()) {
		t.Fatalf("wire error should carry the hook message, got: %v", err)
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("expected a structured appwire.WireError, got %T: %v", err, err)
	}
}

func TestHandleAppThreadModelSet_SuccessReturnsEmptyResponse(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	called := false
	s.SetModelFunc(func(m string) error { got = m; called = true; return nil })

	resp, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if resp != (appwire.EmptyResponse{}) {
		t.Fatalf("expected EmptyResponse, got %#v", resp)
	}
	if !called || got != "claude-x" {
		t.Fatalf("hook not called as expected: called=%v got=%q", called, got)
	}
}

func TestHandleAppThreadModelSet_JoinsSlashedModelUnconditionally(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	s.SetModelFunc(func(m string) error { got = m; return nil })

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		ModelProvider: "openrouter",
		Model:         "anthropic/claude-x",
	}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if got != "openrouter/anthropic/claude-x" {
		t.Fatalf("expected joined ref %q, got %q", "openrouter/anthropic/claude-x", got)
	}
}

func TestHandleModel_RejectsWhileTurnActive(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetModelFunc(func(string) error { called = true; return nil })
	s.SetProcessing(true)

	req := httptest.NewRequest(http.MethodPost, "/model", strings.NewReader(`{"model":"claude-x"}`))
	w := httptest.NewRecorder()
	s.handleModel(w, req)

	if w.Code < 400 {
		t.Fatalf("expected an error status while processing, got %d", w.Code)
	}
	if called {
		t.Fatal("model hook must not be invoked while a turn is active")
	}
}

func TestHandleModel_HookErrorReturnsError(t *testing.T) {
	s := NewServer(ServerConfig{})
	hookErr := errors.New("unknown instance: nope")
	s.SetModelFunc(func(string) error { return hookErr })

	req := httptest.NewRequest(http.MethodPost, "/model", strings.NewReader(`{"model":"claude-x"}`))
	w := httptest.NewRecorder()
	s.handleModel(w, req)

	if w.Code < 400 {
		t.Fatalf("expected an error status from the hook error, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), hookErr.Error()) {
		t.Fatalf("response body should carry the hook message, got: %s", w.Body.String())
	}
}

func TestHandleModel_SuccessReturnsNoContent(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	called := false
	s.SetModelFunc(func(m string) error { got = m; called = true; return nil })

	req := httptest.NewRequest(http.MethodPost, "/model", strings.NewReader(`{"model":"claude-x"}`))
	w := httptest.NewRecorder()
	s.handleModel(w, req)

	if w.Code != 204 {
		t.Fatalf("expected 204 No Content, got %d: %s", w.Code, w.Body.String())
	}
	if !called || got != "claude-x" {
		t.Fatalf("hook not called as expected: called=%v got=%q", called, got)
	}
}

// TestHandleAppThreadModelSet_ThreadReadReflectsNewModelWithNoInterveningTurn
// pins the G2 snapshot-freshness fix (Task 4d): the daemon's cached session
// info (status.Model) must refresh SYNCHRONOUSLY on the model-hook path, so
// thread/read reports the new model immediately after thread/model/set
// returns — with NO intervening turn and no EventSessionStart round trip.
// This is the same wiring shape cmd/serf/serve.go's SetModelFunc closure
// uses: on a successful model hook, call UpdateSessionInfo with the new
// provider/model before returning.
func TestHandleAppThreadModelSet_ThreadReadReflectsNewModelWithNoInterveningTurn(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetStatus(StatusInfo{SessionID: "s1", Model: "gpt-5.4", Profile: "openai"})
	s.SetModelFunc(func(m string) error {
		// Mirrors serve.go: the hook itself refreshes the cached session info
		// synchronously after a successful switch, rather than waiting for the
		// next EventSessionStart (which never fires again mid-session).
		s.UpdateSessionInfo("s1", m, "anthropic")
		return nil
	})

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{ModelProvider: "anthropic", Model: "claude-opus-4-6"}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}

	thread, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if thread.Thread.ModelProvider != "anthropic/claude-opus-4-6" {
		t.Fatalf("thread/read ModelProvider = %q, want anthropic/claude-opus-4-6 with no intervening turn", thread.Thread.ModelProvider)
	}
}
