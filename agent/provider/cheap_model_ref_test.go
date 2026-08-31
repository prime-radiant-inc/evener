package provider_test

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
)

func TestWithCheapModel_QualifiedRefSetsCheapProvider(t *testing.T) {
	p := provider.NewOpenAIProfile("gpt-5.2")
	got := provider.WithCheapModel(p, "anthropic/claude-haiku-4-5-20251001")

	if got.ConfiguredCheapModel() != "claude-haiku-4-5-20251001" {
		t.Errorf("ConfiguredCheapModel() = %q, want claude-haiku-4-5-20251001", got.ConfiguredCheapModel())
	}
	if got.CheapProvider() != "anthropic" {
		t.Errorf("CheapProvider() = %q, want anthropic", got.CheapProvider())
	}
	prov, model := got.CheapModelRef()
	if prov != "anthropic" || model != "claude-haiku-4-5-20251001" {
		t.Errorf("CheapModelRef() = (%q, %q), want (anthropic, claude-haiku-4-5-20251001)", prov, model)
	}
	// The active model/provider are unchanged.
	if got.ID() != "openai" || got.Model() != "gpt-5.2" {
		t.Errorf("active profile changed: id=%q model=%q", got.ID(), got.Model())
	}
}

func TestWithCheapModel_BareRefKeepsSameProvider(t *testing.T) {
	p := provider.NewOpenAIProfile("gpt-5.2")
	got := provider.WithCheapModel(p, "gpt-4.1-mini")

	if got.ConfiguredCheapModel() != "gpt-4.1-mini" {
		t.Errorf("ConfiguredCheapModel() = %q, want gpt-4.1-mini", got.ConfiguredCheapModel())
	}
	// A bare model keeps the active provider.
	if got.CheapProvider() != "openai" {
		t.Errorf("CheapProvider() = %q, want openai", got.CheapProvider())
	}
	prov, model := got.CheapModelRef()
	if prov != "openai" || model != "gpt-4.1-mini" {
		t.Errorf("CheapModelRef() = (%q, %q), want (openai, gpt-4.1-mini)", prov, model)
	}
}

func TestCheapModelRefString_RoundTrips(t *testing.T) {
	base := provider.NewOpenAIProfile("gpt-5.2")

	cases := []struct {
		ref  string
		want string
	}{
		{"anthropic/claude-haiku-4-5-20251001", "anthropic/claude-haiku-4-5-20251001"},
		{"gpt-4.1-mini", "gpt-4.1-mini"},
	}
	for _, tc := range cases {
		p := provider.WithCheapModel(base, tc.ref)
		if got := p.CheapModelRefString(); got != tc.want {
			t.Errorf("CheapModelRefString() = %q, want %q", got, tc.want)
		}
		// Re-applying the persisted string reproduces the routing.
		rt := provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), p.CheapModelRefString())
		rtProvider, rtModel := rt.CheapModelRef()
		providerName, model := p.CheapModelRef()
		if rtProvider != providerName || rtModel != model {
			t.Errorf("round-trip mismatch for %q: got (%q,%q), want (%q,%q)",
				tc.ref, rtProvider, rtModel, providerName, model)
		}
	}

	// Unconfigured: empty string (so persistence does not pin a default).
	if got := base.CheapModelRefString(); got != "" {
		t.Errorf("CheapModelRefString() with no cheap configured = %q, want empty", got)
	}
}

func TestCheapModelRef_DefaultsToActiveProviderAndModel(t *testing.T) {
	p := provider.NewOpenAIProfile("gpt-5.2")
	// No cheap model configured: ConfiguredCheapModel stays empty, while
	// auxiliary work falls back to the active provider and model.
	if p.ConfiguredCheapModel() != "" {
		t.Errorf("ConfiguredCheapModel() = %q, want empty", p.ConfiguredCheapModel())
	}
	if p.CheapProvider() != "openai" {
		t.Errorf("CheapProvider() = %q, want openai", p.CheapProvider())
	}
	prov, model := p.CheapModelRef()
	if prov != "openai" || model != "gpt-5.2" {
		t.Errorf("CheapModelRef() = (%q, %q), want (openai, gpt-5.2)", prov, model)
	}
}
