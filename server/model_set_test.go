package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func setModelControlTestHook(t *testing.T, s *Server, fn func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error)) *stubThreadEnvelopeSource {
	t.Helper()
	installModelControlIdentity(t, s, "s1", "1", "local:s1")
	snapshot := modelControlSnapshot("s1", "1", 1, "gpt-old")
	source := &stubThreadEnvelopeSource{modelControl: snapshot}
	s.SetThreadEnvelopeSource(source)
	s.SetModelControlBindFunc(func(appwire.ModelControlAuthority) (ModelControlBinding, error) {
		return ModelControlBinding{Set: fn, Envelope: source}, nil
	})
	return source
}

// While a turn runs, the protocol layer must NOT refuse a model switch: the
// session's SetModel queues it for the turn boundary (the composer's pick-
// mid-turn contract), so the handler relays to the hook and succeeds. The
// session owns the now-versus-boundary decision.
func TestHandleAppThreadModelSet_WhileProcessingReachesTheHook(t *testing.T) {
	s := NewServer(ServerConfig{})
	called := false
	setModelControlTestHook(t, s, func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
		called = true
		return appwire.ThreadModelSetResponse{}, nil
	})
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
	setModelControlTestHook(t, s, func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
		return appwire.ThreadModelSetResponse{}, hookErr
	})

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

func TestHandleAppThreadModelSet_SuccessReturnsTypedResponse(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got string
	called := false
	source := setModelControlTestHook(t, s, func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
		got = params.Model
		called = true
		return appwire.ThreadModelSetResponse{Disposition: appwire.ModelControlDispositionAccepted}, nil
	})

	resp, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{Model: "claude-x"})
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if resp.ModelControlVersion != appwire.ModelControlVersion1 || resp.ModelControl == nil || resp.ModelControl.Revision != source.modelControl.Revision {
		t.Fatalf("typed response = %#v", resp)
	}
	if !called || got != "claude-x" {
		t.Fatalf("hook not called as expected: called=%v got=%q", called, got)
	}
}

func TestHandleAppThreadModelSet_PreservesQualifiedModelAndProviderFields(t *testing.T) {
	s := NewServer(ServerConfig{})
	var got appwire.ThreadModelSetParams
	setModelControlTestHook(t, s, func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
		got = params
		return appwire.ThreadModelSetResponse{}, nil
	})

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{
		ModelProvider: "openrouter",
		Model:         "anthropic/claude-x",
	}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if got.ModelProvider != "openrouter" || got.Model != "anthropic/claude-x" {
		t.Fatalf("target changed: %+v", got)
	}
}

func TestHandleAppThreadModelSet_ThreadReadReflectsNewModelWithNoInterveningTurn(t *testing.T) {
	s := NewServer(ServerConfig{})
	source := setModelControlTestHook(t, s, func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
		snapshot := modelControlSnapshot("s1", "1", 2, params.Model)
		snapshot.Active.ModelProvider = params.ModelProvider
		source := envelopeSourceFor(s)
		source.modelControl = snapshot
		return appwire.ThreadModelSetResponse{Disposition: appwire.ModelControlDispositionAccepted, ModelControl: &snapshot}, nil
	})
	_ = source

	if _, err := s.handleAppThreadModelSet(context.Background(), appwire.ThreadModelSetParams{ModelProvider: "anthropic", Model: "claude-opus-4-6"}); err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}

	thread, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	if thread.Thread.Evener.ModelControl == nil || thread.Thread.Evener.ModelControl.Active.ModelProvider != "anthropic" || thread.Thread.Evener.ModelControl.Active.Model != "claude-opus-4-6" {
		t.Fatalf("thread/read ModelControl = %+v, want the new snapshot with no intervening turn", thread.Thread.Evener.ModelControl)
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
