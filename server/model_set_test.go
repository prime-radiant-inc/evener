package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// While a turn runs, the protocol layer must NOT refuse a model switch: the
// session's SetModel queues it for the turn boundary (the composer's pick-
// mid-turn contract), so the handler relays to the hook and succeeds. The
// session owns the now-versus-boundary decision.
func TestHandleAppThreadModelSet_WhileProcessingReachesTheHook(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetModelFunc(func(string, string) error { called = true; return nil })
	s.SetProcessing(true)

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"}); err != nil {
		t.Fatalf("a mid-turn switch must not error at the protocol layer: %v", err)
	}
	if !called {
		t.Fatal("model hook must be invoked while a turn is active")
	}
}

func TestHandleAppThreadModelSet_HookErrorSurfacesAsWireError(t *testing.T) {
	s := NewServer(ServerConfig{})
	hookErr := errors.New("unknown instance: nope")
	s.SetModelFunc(func(string, string) error { return hookErr })

	_, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err == nil {
		t.Fatal("expected an error from the hook")
	}
	if !strings.Contains(err.Error(), hookErr.Error()) {
		t.Fatalf("wire error should carry the hook message, got: %v", err)
	}
	if _, ok := errors.AsType[appwire.WireError](err); !ok {
		t.Fatalf("expected a structured appwire.WireError, got %T: %v", err, err)
	}
}

func TestHandleAppThreadModelSet_SuccessReturnsRejectionCode(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	var gotClientMutationID string
	called := false
	s.SetModelFunc(func(m, clientMutationID string) error {
		got = m
		gotClientMutationID = clientMutationID
		called = true
		return nil
	})

	resp, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Model:            "claude-x",
		ClientMutationID: "model-pick-b",
	})
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if resp.RejectionCode != events.WarningCodeModelSwitchRejected {
		t.Fatalf("rejectionCode=%q, want %q", resp.RejectionCode, events.WarningCodeModelSwitchRejected)
	}
	if !called || got != "claude-x" {
		t.Fatalf("hook not called as expected: called=%v got=%q", called, got)
	}
	if gotClientMutationID != "model-pick-b" {
		t.Fatalf("model hook client mutation id = %q, want %q", gotClientMutationID, "model-pick-b")
	}
}

// TestHandleAppThreadModelSet_RefusalPublishesTheRecordedOutcome pins the
// refusal half of the pick projection. A refused switch records its outcome on
// the session and then returns an error; nothing else fires, because no switch
// applied and no turn boundary ran to publish anything. The refusal notice is
// one entry in a bounded ring, so a client that reconnects after it was evicted
// keeps presenting the pick as still in flight unless this reply published it.
func TestHandleAppThreadModelSet_RefusalPublishesTheRecordedOutcome(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	src := &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "th_1"}}
	srv.SetThreadEnvelopeSource(src)
	srv.RefreshThreadEnvelope()

	const refused = "gpt-unrepresentable-here"
	srv.SetModelFunc(func(string, string) error {
		// The session records the rejection before returning the refusal, the
		// way Session.SetModelWithMutationID does.
		src.setModelPick(appwire.ModelPickState{Sequence: 3, Outcome: appwire.ModelPickRejected, ClientMutationID: "pick-refused", Model: refused})
		return errors.New("cannot switch: history contains content unrepresentable by that target (audio)")
	})

	if _, err := srv.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		Model:            refused,
		ClientMutationID: "pick-refused",
	}); err == nil {
		t.Fatal("a refused model switch returned no error")
	}

	got := readThreadOverWire(t, srv, "local:th_1").Evener.ModelPick
	if got == nil {
		t.Fatal("the refusal published no model pick: once the bounded notice ring evicts the warning, a reconnecting client has no account of the rejection")
	}
	if got.Outcome != appwire.ModelPickRejected || got.Model != refused {
		t.Fatalf("modelPick = %+v, want the refused pick recorded as %q", got, appwire.ModelPickRejected)
	}
}

func TestHandleAppThreadModelSet_WithoutMutationIDKeepsCompatibilityResponse(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetModelFunc(func(string, string) error { return nil })

	response, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if response.RejectionCode != "" {
		t.Fatalf("rejection capability was advertised without a request identity")
	}
}

func TestHandleAppThreadModelSet_JoinsSlashedModelUnconditionally(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	s.SetModelFunc(func(m, _ string) error { got = m; return nil })

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

func TestHandleAppThreadModelSet_ThreadReadReflectsNewModelWithNoInterveningTurn(t *testing.T) {
	s := NewServer(ServerConfig{})
	seedStatus(s, StatusInfo{SessionID: "s1", Model: "gpt-5.4", Profile: "openai"})
	s.SetModelFunc(func(m, _ string) error {
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

func TestHandleAppThreadVisionModelSet_RejectsWhileProcessing(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	s.SetVisionModelFunc(func(string) error { called = true; return nil })
	s.SetProcessing(true)

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{VisionModel: "off"}); err == nil {
		t.Fatal("expected an error while a turn is processing")
	}
	if called {
		t.Fatal("vision-model hook must not be invoked while a turn is active")
	}
}

func TestHandleAppThreadVisionModelSet_HookReceivesTheSetting(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	s.SetVisionModelFunc(func(v string) error { got = v; return nil })

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{VisionModel: "anthropic/claude-x"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got != "anthropic/claude-x" {
		t.Fatalf("hook got %q, want the ref unchanged", got)
	}
}

func TestHandleAppThreadVisionModelSet_HookErrorSurfacesAsWireError(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetVisionModelFunc(func(string) error { return errors.New("vision model provider \"nope\" is not configured") })

	if _, err := s.handleAppThreadVisionModelSet(context.Background(), appwire.ThreadVisionModelSetParams{VisionModel: "nope/m"}); err == nil {
		t.Fatal("hook error must surface")
	}
}
