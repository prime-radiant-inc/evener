package llm

import (
	"context"
)

type authenticatorOverrideKey struct{}

// authenticatorOverrides carries one context's call-scoped authenticators.
// Only exact scheme-bound values answer; the legacy generic value is kept for
// WithAuthenticatorOverride's API and never serves a request.
type authenticatorOverrides struct {
	generic  Authenticator
	byScheme map[string]Authenticator
}

// WithAuthenticatorOverride records a legacy call-scoped authenticator, but
// no scheme resolves through it: record-backed OAuth schemes require
// WithScopedAuthenticator, and every other scheme resolves from its Resolved
// record or the global registration. A nil override returns ctx unchanged.
func WithAuthenticatorOverride(ctx context.Context, auth Authenticator) context.Context {
	if auth == nil {
		// context.WithValue panics on a nil value, and the documented
		// contract is that nil keeps the historical lookup: the unchanged
		// context IS that lookup.
		return ctx
	}
	overrides := authenticatorOverridesForContext(ctx)
	overrides.generic = auth
	return context.WithValue(ctx, authenticatorOverrideKey{}, overrides)
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
	for existing, bound := range overrides.byScheme {
		byScheme[existing] = bound
	}
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
	overrides := authenticatorOverridesForContext(ctx)
	if auth := overrides.byScheme[scheme]; auth != nil {
		return auth
	}
	// Record-backed OAuth schemes require an exact scheme-bound authenticator;
	// every other scheme keeps the global lookup. A generic override answers
	// neither, so it cannot run for a bearer request.
	return nil
}
