package llm

import (
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestWithAuthenticatorOverrideNilKeepsTheLookup pins the documented
// contract: a nil override means "no override", so it must return the
// context unchanged rather than panicking in context.WithValue.
func TestWithAuthenticatorOverrideNilKeepsTheLookup(t *testing.T) {
	ctx := context.Background()
	got := WithAuthenticatorOverride(ctx, nil)
	if got != ctx {
		t.Fatal("a nil override must return the same context")
	}
	if auth := AuthenticatorOverrideFor(got, registry.AuthOAuthOpenAICodex); auth != nil {
		t.Fatalf("nil override resolved an authenticator: %T", auth)
	}
}

type recordingAuthenticator struct{ applied int }

func (a *recordingAuthenticator) Apply(context.Context, *http.Request, registry.Resolved) error {
	a.applied++
	return nil
}

// TestAuthenticatorOverrideOnlyAnswersForTheCodexScheme pins the scheme gate:
// an override bound to the context serves the scoped Codex transport and is
// invisible to every other scheme, which keeps resolving from the record
// alone. A bearer transport must never receive a Codex-bound override.
func TestAuthenticatorOverrideOnlyAnswersForTheCodexScheme(t *testing.T) {
	auth := &recordingAuthenticator{}
	ctx := WithAuthenticatorOverride(context.Background(), auth)

	if got := AuthenticatorOverrideFor(ctx, registry.AuthOAuthOpenAICodex); got != auth {
		t.Fatalf("codex scheme override = %v, want the bound authenticator", got)
	}
	if got := AuthenticatorOverrideFor(ctx, "bearer"); got != nil {
		t.Fatalf("bearer scheme override = %v, want nil: the override must not answer for another scheme", got)
	}
	if got := AuthenticatorOverrideFor(context.Background(), registry.AuthOAuthOpenAICodex); got != nil {
		t.Fatalf("override without a binding = %v, want nil", got)
	}
}
