package llm

import (
	"context"
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
