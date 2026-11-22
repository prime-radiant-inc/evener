package llm

import (
	"context"

	"primeradiant.com/evener/llm/registry"
)

type authenticatorOverrideKey struct{}

// authenticatorOverrides carries one context's call-scoped authenticators.
// Scheme-bound values take precedence; the legacy generic value serves only
// schemes that do not read a shared OAuth record.
type authenticatorOverrides struct {
	generic  Authenticator
	byScheme map[string]Authenticator
}

// WithAuthenticatorOverride carries a call-scoped authenticator for schemes
// that do not read a shared OAuth record; those must use
// WithScopedAuthenticator. It replaces the process-global AuthenticatorFor
// lookup for protocol calls issued under the returned context: the caller
// binds the authenticator to the request instead of racing a global rewire.
// A nil override keeps the historical lookup.
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
	// The record-backed OAuth transports use scoped authenticators: their
	// record lookup reads mutable process-global state (DefaultCodex.StateDir,
	// DefaultCopilot.StateDir), so a launch binds its own state root instead.
	// A generic override must not cross those schemes. Every other scheme
	// resolves from the Resolved record alone and keeps the global lookup.
	if scheme != registry.AuthOAuthOpenAICodex && scheme != registry.AuthOAuthGitHubCopilot {
		return overrides.generic
	}
	return nil
}
