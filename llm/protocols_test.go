package llm

import (
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

type stubProtocol struct{ id string }

func (s stubProtocol) ID() string                { return s.id }
func (stubProtocol) PrunablePaths() []string     { return nil }
func (stubProtocol) BuildBody(Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (stubProtocol) Complete(context.Context, Request, registry.Resolved) (Response, error) {
	return Response{}, nil
}
func (stubProtocol) Stream(context.Context, Request, registry.Resolved) (Stream, error) {
	return nil, ErrStreamUnsupported
}
func (stubProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return nil, ErrModelListingUnsupported
}
func (stubProtocol) CountTokens(context.Context, Request, registry.Resolved) (int, error) {
	return 0, ErrInputTokenCountUnsupported
}

type stubAuth struct{ preparer bool }

func (stubAuth) Apply(context.Context, *http.Request, registry.Resolved) error { return nil }

type stubPreparer struct{ stubAuth }

func (stubPreparer) PrepareRequest(context.Context, *http.Request, map[string]any, Request, registry.Resolved) error {
	return nil
}
func (stubPreparer) RequiresStreamingComplete() bool { return true }

func TestRegisterProtocolAndLookup(t *testing.T) {
	RegisterProtocol(stubProtocol{id: "test-proto-lookup"})
	p, ok := ProtocolFor("test-proto-lookup")
	if !ok || p.ID() != "test-proto-lookup" {
		t.Fatalf("ProtocolFor = %v, %v", p, ok)
	}
	if _, ok := ProtocolFor("test-proto-missing"); ok {
		t.Fatal("unknown protocol must not resolve")
	}
}

func TestRegisterProtocolRejectsDuplicatesAndEmptyIDs(t *testing.T) {
	RegisterProtocol(stubProtocol{id: "test-proto-dup"})
	assertPanics(t, "duplicate", func() { RegisterProtocol(stubProtocol{id: "test-proto-dup"}) })
	assertPanics(t, "empty id", func() { RegisterProtocol(stubProtocol{}) })
}

func TestRegisterAuthenticatorAndPreparer(t *testing.T) {
	RegisterAuthenticator("test-auth-plain", stubAuth{})
	RegisterAuthenticator("test-auth-preparer", stubPreparer{})
	if _, ok := AuthenticatorFor("test-auth-plain"); !ok {
		t.Fatal("plain authenticator not found")
	}
	if _, ok := RequestPreparerFor("test-auth-plain"); ok {
		t.Fatal("plain authenticator must not be a preparer")
	}
	prep, ok := RequestPreparerFor("test-auth-preparer")
	if !ok || !prep.RequiresStreamingComplete() {
		t.Fatalf("preparer lookup = %v, %v", prep, ok)
	}
	if _, ok := AuthenticatorFor("test-auth-missing"); ok {
		t.Fatal("unknown scheme must not resolve")
	}
	assertPanics(t, "duplicate scheme", func() { RegisterAuthenticator("test-auth-plain", stubAuth{}) })
	assertPanics(t, "empty scheme", func() { RegisterAuthenticator("", stubAuth{}) })
}

func assertPanics(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected panic", what)
		}
	}()
	fn()
}
