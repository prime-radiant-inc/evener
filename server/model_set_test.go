package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm/registry"
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

func TestHandleAppThreadModelSet_SuccessReturnsEmptyResponse(t *testing.T) {
	s := NewServer(ServerConfig{})
	var gotProvider, gotModel string
	called := false
	s.SetModelFunc(func(provider, model string) error { gotProvider, gotModel = provider, model; called = true; return nil })

	resp, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if resp != (appwire.EmptyResponse{}) {
		t.Fatalf("expected EmptyResponse, got %#v", resp)
	}
	if !called || gotModel != "claude-x" {
		t.Fatalf("hook not called as expected: called=%v got=%q", called, gotModel)
	}
	if gotProvider != "" {
		t.Fatalf("hook got provider %q for a ref-only request, want an empty provider", gotProvider)
	}
}

// The model id may itself contain slashes (an OpenRouter row is
// "anthropic/claude-opus-5"), so the provider the picker named must reach the
// session as its own argument. Folding the pair into one ref would let the
// resolver read the model's first segment as the instance and keep OpenRouter
// when the reader picked the direct Anthropic row.
func TestHandleAppThreadModelSet_PassesProviderAndSlashedModelApart(t *testing.T) {
	s := NewServer(ServerConfig{})
	var gotProvider, gotModel string
	s.SetModelFunc(func(provider, model string) error { gotProvider, gotModel = provider, model; return nil })

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		ModelProvider: "openrouter",
		Model:         "anthropic/claude-x",
	}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if gotProvider != "openrouter" || gotModel != "anthropic/claude-x" {
		t.Fatalf("hook got provider %q model %q, want the two halves unjoined", gotProvider, gotModel)
	}
}

func TestHandleAppThreadModelSet_ThreadReadReflectsNewModelWithNoInterveningTurn(t *testing.T) {
	s := NewServer(ServerConfig{})
	seedStatus(s, StatusInfo{SessionID: "s1", Model: "gpt-5.4", Profile: "openai"})
	s.SetModelFunc(func(provider, model string) error {
		// Mirrors serve.go: the hook itself refreshes the cached session info
		// synchronously after a successful switch, rather than waiting for the
		// next EventSessionStart (which never fires again mid-session). It
		// samples both halves off the committed profile, so the model is the
		// bare id and the instance is the profile, the way thread/read and the
		// cost lookup read them.
		s.UpdateSessionInfo("s1", model, provider)
		return nil
	})

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{ModelProvider: "anthropic", Model: "claude-opus-4-6"}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}

	thread, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if thread.Thread.ModelProvider != "claude-opus-4-6" || thread.Thread.Evener.Profile != "anthropic" {
		t.Fatalf("thread/read = %q on profile %q, want claude-opus-4-6 on anthropic with no intervening turn",
			thread.Thread.ModelProvider, thread.Thread.Evener.Profile)
	}
}

// A queued pick's acceptance must reach the status cache the same way a
// committed switch does: the model/set hook installs the pick synchronously
// for its own reply window, but a pick queued outside an RPC in flight (the
// deferred-failure retraction, a compact-at switch) has only the bridge's
// event effect. Without it, thread/read keeps naming the previous model with
// no corrective event behind it, which reads to a subscriber as the pick
// silently flipping back.
func TestSessionEventStatus_NextModelChangedRefreshesSessionInfo(t *testing.T) {
	s := NewServer(ServerConfig{})
	seedStatus(s, StatusInfo{SessionID: "s1", Model: "gpt-initial", Profile: "openai"})

	s.applySessionEventStatus(events.SessionEvent{
		Kind:      events.EventNextModelChanged,
		SessionID: "s1",
		Data:      events.NextModelChangedData{Provider: "openai", Model: "gpt-queued"},
	})

	thread, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if thread.Thread.ModelProvider != "gpt-queued" || thread.Thread.Evener.Profile != "openai" {
		t.Fatalf("thread/read = %q on profile %q, want the accepted pick gpt-queued on openai",
			thread.Thread.ModelProvider, thread.Thread.Evener.Profile)
	}
}

// A queued pick rewrites the pair thread/read returns for display, but the
// usage it reports was spent on the committed model, so the cost must price
// at the committed model's rates until the switch actually commits - not
// reprice the session's whole meter at a model that has not served a token.
func TestThreadReadPricesUsageAtTheCommittedModelWhileAPickIsAdvertised(t *testing.T) {
	s := NewServer(ServerConfig{})
	s.SetCostLookupFunc(func(ref string) *registry.Cost {
		switch ref {
		case "openai/gpt-5.2":
			return &registry.Cost{Input: 1, Output: 1}
		case "openai/gpt-5.4":
			return &registry.Cost{Input: 10, Output: 10}
		}
		return nil
	})
	seedStatus(s, StatusInfo{SessionID: "s1", Model: "gpt-5.2", Profile: "openai"})
	setEnvelope(s, func(e *stubThreadEnvelopeSource) {
		e.usage = &appwire.EvenerUsage{InputTokens: 100_000, OutputTokens: 100_000, TotalTokens: 200_000}
	})

	s.applySessionEventStatus(events.SessionEvent{
		Kind:      events.EventNextModelChanged,
		SessionID: "s1",
		Data:      events.NextModelChangedData{Provider: "openai", Model: "gpt-5.4"},
	})

	thread, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if thread.Thread.ModelProvider != "gpt-5.4" {
		t.Fatalf("thread/read model = %q, want the advertised pick gpt-5.4", thread.Thread.ModelProvider)
	}
	// 100_000/1e6*1 + 100_000/1e6*1 = $0.20 at the committed gpt-5.2's rates;
	// the same tokens at the queued gpt-5.4's rates would read ~$2.00.
	if thread.Thread.Evener.Cost != "~$0.20" {
		t.Fatalf("usage cost = %q, want ~$0.20: already-used tokens must stay priced at the committed model's rates", thread.Thread.Evener.Cost)
	}

	// Once the switch commits, both the display pair and the pricing pair move.
	s.applySessionEventStatus(events.SessionEvent{
		Kind:      events.EventModelChanged,
		SessionID: "s1",
		Data:      events.ModelChangedData{NewProvider: "openai", NewModel: "gpt-5.4"},
	})
	thread, err = s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead after commit: %v", err)
	}
	if thread.Thread.Evener.Cost != "~$2.00" {
		t.Fatalf("usage cost after the commit = %q, want ~$2.00 at the applied model's rates", thread.Thread.Evener.Cost)
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
