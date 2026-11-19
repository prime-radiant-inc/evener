package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// setSessionModel switches the session's model the way a client does: through
// the authority-bound SetModelControl, fenced on the session's own instance and
// boot generation. A test session is not served by a daemon, so it carries no
// generation until this installs one; without one there is no authority to
// fence a switch on and the request is rejected as invalid.
func setSessionModel(t *testing.T, sess *agent.Session, model string) error {
	t.Helper()
	if sess.BootGeneration() == "" {
		sess.SetBootGeneration("test")
	}
	generation := sess.BootGeneration()
	mutationID, err := identifier.NewClientMutationID()
	if err != nil {
		t.Fatalf("new client mutation id: %v", err)
	}
	_, switchErr := sess.SetModelControl(agent.ModelControlRequest{
		Model:                  model,
		ClientMutationID:       mutationID,
		ExpectedInstanceID:     sess.ID(),
		ExpectedBootGeneration: generation,
	})
	return switchErr
}

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

func strictModelSetParams(mutationID, model string) appwire.ThreadModelSetParams {
	return modelControlSetParams("local:s1", "s1", "1", mutationID, "openai", model)
}

// While a turn runs, the protocol layer must NOT refuse a model switch: the
// session's SetModelControl queues it for the turn boundary (the composer's pick-
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

	if _, err := s.handleAppThreadModelSet(context.Background(), strictModelSetParams("pick-mid-turn", "claude-x")); err != nil {
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

	_, err := s.handleAppThreadModelSet(context.Background(), strictModelSetParams("pick-invalid", "claude-x"))
	if err == nil {
		t.Fatal("expected an error from the hook")
	}
	requireModelControlWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
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

	resp, err := s.handleAppThreadModelSet(context.Background(), strictModelSetParams("pick-success", "claude-x"))
	if err != nil {
		t.Fatalf("handleAppThreadModelSet: %v", err)
	}
	if resp.ModelControl == nil || resp.ModelControl.Revision != source.modelControl.Revision {
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

	params := strictModelSetParams("pick-qualified", "anthropic/claude-x")
	params.ModelProvider = "openrouter"
	if _, err := s.handleAppThreadModelSet(context.Background(), params); err != nil {
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

	params := strictModelSetParams("pick-read", "claude-opus-4-6")
	params.ModelProvider = "anthropic"
	if _, err := s.handleAppThreadModelSet(context.Background(), params); err != nil {
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
