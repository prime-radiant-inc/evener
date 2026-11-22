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
	t.Parallel()
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

// TestScopedAuthenticatorsAreSchemeSpecific proves one context can carry two
// record-backed OAuth scopes with separate state roots without either scope
// answering for the other scheme.
func TestScopedAuthenticatorsAreSchemeSpecific(t *testing.T) {
	t.Parallel()
	codexAuth := &recordingAuthenticator{}
	copilotAuth := &recordingAuthenticator{}
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthOpenAICodex, codexAuth)
	ctx = WithScopedAuthenticator(ctx, registry.AuthOAuthGitHubCopilot, copilotAuth)

	if got := AuthenticatorOverrideFor(ctx, registry.AuthOAuthOpenAICodex); got != codexAuth {
		t.Fatalf("codex scheme override = %v, want the Codex-bound authenticator", got)
	}
	if got := AuthenticatorOverrideFor(ctx, registry.AuthOAuthGitHubCopilot); got != copilotAuth {
		t.Fatalf("copilot scheme override = %v, want the Copilot-bound authenticator", got)
	}
	if got := AuthenticatorOverrideFor(ctx, "bearer"); got != nil {
		t.Fatalf("bearer scheme override = %v, want nil: a scoped OAuth authenticator must not answer for another scheme", got)
	}
	if got := AuthenticatorOverrideFor(context.Background(), registry.AuthOAuthGitHubCopilot); got != nil {
		t.Fatalf("override without a binding = %v, want nil", got)
	}
}

// TestAuthenticatorOverrideCannotCrossOAuthSchemes pins that the legacy
// single-authenticator override cannot answer for either record-backed OAuth
// scheme. Those schemes require an explicit per-scheme binding so a Codex
// scope can never authenticate a Copilot request.
func TestAuthenticatorOverrideCannotCrossOAuthSchemes(t *testing.T) {
	t.Parallel()
	generic := &recordingAuthenticator{}
	ctx := WithAuthenticatorOverride(context.Background(), generic)
	for _, scheme := range []string{registry.AuthOAuthOpenAICodex, registry.AuthOAuthGitHubCopilot} {
		if got := AuthenticatorOverrideFor(ctx, scheme); got != nil {
			t.Fatalf("%s override = %v, want nil without a scheme-specific binding", scheme, got)
		}
	}
	if got := AuthenticatorOverrideFor(ctx, "bearer"); got != generic {
		t.Fatalf("non-record scheme override = %v, want the legacy generic authenticator", got)
	}
}
