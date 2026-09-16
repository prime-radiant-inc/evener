package llm

import (
	"context"

	"primeradiant.com/evener/llm/registry"
)

type authenticatorOverrideKey struct{}

// WithAuthenticatorOverride carries a call-scoped authenticator that
// replaces the process-global AuthenticatorFor lookup for protocol
// calls issued under the returned context: the caller binds the
// authenticator (and its state root) to the request instead of racing
// a global rewire. A nil override keeps the historical lookup.
func WithAuthenticatorOverride(ctx context.Context, auth Authenticator) context.Context {
	if auth == nil {
		// context.WithValue panics on a nil value, and the documented
		// contract is that nil keeps the historical lookup: the unchanged
		// context IS that lookup.
		return ctx
	}
	return context.WithValue(ctx, authenticatorOverrideKey{}, auth)
}

// authenticatorOverride returns the call-scoped authenticator, if any.
func authenticatorOverride(ctx context.Context) Authenticator {
	if auth, ok := ctx.Value(authenticatorOverrideKey{}).(Authenticator); ok {
		return auth
	}
	return nil
}

// AuthenticatorOverrideFor returns the call-scoped authenticator carried
// by ctx when one is set AND it serves the call's scheme: a Codex scope
// must never answer for a bearer transport or vice versa. Nil keeps the
// global lookup.
func AuthenticatorOverrideFor(ctx context.Context, scheme string) Authenticator {
	auth := authenticatorOverride(ctx)
	if auth == nil {
		return nil
	}
	// Only the Codex transport uses scoped authenticators today: it is
	// the sole scheme whose record lookup reads mutable process-global
	// state (DefaultCodex.StateDir). Every other scheme resolves from
	// the Resolved record alone and keeps the global lookup.
	if scheme != registry.AuthOAuthOpenAICodex {
		return nil
	}
	return auth
}
