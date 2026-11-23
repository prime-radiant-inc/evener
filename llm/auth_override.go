package llm

import (
	"context"
	"maps"

	"primeradiant.com/evener/llm/registry"
)

type authenticatorOverrideKey struct{}

// authenticatorOverrides carries one context's call-scoped authenticators,
// each bound to the one scheme it may serve.
type authenticatorOverrides struct {
	byScheme map[string]Authenticator
}

// WithAuthenticatorOverride records the library's single call-scoped
// authenticator for the Codex transport: the one record-backed scheme this
// legacy entry point names. Every other scheme resolves from its Resolved
// record or the global registration, so a Codex authenticator can never
// authenticate a Copilot or bearer request. A nil override returns ctx
// unchanged.
func WithAuthenticatorOverride(ctx context.Context, auth Authenticator) context.Context {
	return WithScopedAuthenticator(ctx, registry.AuthOAuthOpenAICodex, auth)
}

// WithScopedAuthenticator binds auth to one auth scheme for calls under ctx.
// Record-backed OAuth schemes must use this so a Codex scope cannot answer a
// Copilot request, or vice versa. A nil authenticator or empty scheme keeps ctx.
func WithScopedAuthenticator(ctx context.Context, scheme string, auth Authenticator) context.Context {
	if auth == nil || scheme == "" {
		return ctx
	}
	overrides := authenticatorOverridesForContext(ctx)
	byScheme := make(map[string]Authenticator, len(overrides.byScheme)+1)
	maps.Copy(byScheme, overrides.byScheme)
	byScheme[scheme] = auth
	overrides.byScheme = byScheme
	return context.WithValue(ctx, authenticatorOverrideKey{}, overrides)
}

// authenticatorOverridesForContext returns the call-scoped authenticators, if any.
func authenticatorOverridesForContext(ctx context.Context) authenticatorOverrides {
	if overrides, ok := ctx.Value(authenticatorOverrideKey{}).(authenticatorOverrides); ok {
		return overrides
	}
	return authenticatorOverrides{}
}

// AuthenticatorOverrideFor returns the call-scoped authenticator carried
// by ctx when one is set AND it serves the call's scheme: a Codex scope
// must never answer for a bearer transport or vice versa. Nil keeps the
// global lookup.
func AuthenticatorOverrideFor(ctx context.Context, scheme string) Authenticator {
	return authenticatorOverridesForContext(ctx).byScheme[scheme]
}
