package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/provider"
)

// TestResolveDelegateRestoreProfileRef_NilResolver_MismatchedProfile covers
// the nil-resolver path where profileID differs from base.ID() (lines 130-132).
func TestResolveDelegateRestoreProfileRef_NilResolver_MismatchedProfile(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	s := &Session{}
	_, err := s.resolveDelegateRestoreProfileRef(base, "openai", "gpt-4")
	if err == nil {
		t.Fatal("expected error for mismatched profileID with nil resolver")
	}
}

// TestResolveDelegateRestoreProfileRef_NilResolver_MatchingProfile covers
// the nil-resolver path where profileID matches base.ID() (lines 133).
func TestResolveDelegateRestoreProfileRef_NilResolver_MatchingProfile(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	s := &Session{}
	p, err := s.resolveDelegateRestoreProfileRef(base, base.ID(), "claude-3.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil profile")
	}
}

// TestResolveDelegateRestoreProfileRef_ResolverError covers the resolver
// returning an error (lines 118-120).
func TestResolveDelegateRestoreProfileRef_ResolverError(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	s := &Session{
		resolveProfile: func(ref string) (*provider.Profile, error) {
			return nil, errors.New("resolver boom")
		},
	}
	_, err := s.resolveDelegateRestoreProfileRef(base, "openai", "gpt-4")
	if err == nil || err.Error() != "resolver boom" {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

// TestResolveDelegateRestoreProfileRef_ResolverNilProfile covers the resolver
// returning a nil profile (lines 122-123).
func TestResolveDelegateRestoreProfileRef_ResolverNilProfile(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	s := &Session{
		resolveProfile: func(ref string) (*provider.Profile, error) {
			return nil, nil
		},
	}
	_, err := s.resolveDelegateRestoreProfileRef(base, "openai", "gpt-4")
	if err == nil {
		t.Fatal("expected error for nil resolved profile")
	}
}

// TestResolveDelegateRestoreProfileRef_ResolverSameID covers the resolver
// returning a profile with the same ID as base (lines 125, 128).
func TestResolveDelegateRestoreProfileRef_ResolverSameID(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	resolved := newAnthropicProfile("claude-3.5")
	s := &Session{
		resolveProfile: func(ref string) (*provider.Profile, error) {
			return resolved, nil
		},
	}
	p, err := s.resolveDelegateRestoreProfileRef(base, base.ID(), "claude-3.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil profile")
	}
}

// TestResolveDelegateRestoreProfileRef_ResolverDifferentID covers the resolver
// returning a profile with a different ID from base, triggering
// WithCommunicateOverridesFrom (lines 125-127).
func TestResolveDelegateRestoreProfileRef_ResolverDifferentID(t *testing.T) {
	t.Parallel()
	base := newAnthropicProfile("claude-3.5")
	resolved := provider.NewOpenAIProfile("gpt-4")
	s := &Session{
		resolveProfile: func(ref string) (*provider.Profile, error) {
			return resolved, nil
		},
	}
	p, err := s.resolveDelegateRestoreProfileRef(base, "openai", "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil profile")
	}
	if p.ID() == base.ID() {
		t.Fatal("expected different ID after WithCommunicateOverridesFrom")
	}
}
