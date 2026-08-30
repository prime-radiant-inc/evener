package provider

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

// Reasoning support is a model fact. A cataloged model the catalog marks
// non-reasoning must resolve SupportsReasoning() == false with an empty level
// list, so no effort control ever reaches it; an uncataloged model stays
// permitted.
func TestProfile_ReasoningSupportResolvedFromCatalog(t *testing.T) {
	cases := []struct {
		name          string
		profile       *Profile
		wantReasoning bool
	}{
		{"openai cataloged non-reasoning", NewOpenAIProfile("gpt-4.1"), false},
		{"openai cataloged reasoning", NewOpenAIProfile("gpt-5.5"), true},
		{"openai uncataloged is permitted", NewOpenAIProfile("glm-5.3"), true},
		{"gemini cataloged non-reasoning", newGeminiProfile("gemini-2.0-flash"), false},
		{"gemini cataloged reasoning", newGeminiProfile("gemini-2.5-pro"), true},
		{"anthropic cataloged reasoning", newAnthropicProfile("claude-opus-4-6"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.profile.SupportsReasoning(); got != tc.wantReasoning {
				t.Fatalf("SupportsReasoning() = %v, want %v", got, tc.wantReasoning)
			}
			if !tc.wantReasoning && len(tc.profile.ReasoningEffortLevels()) != 0 {
				t.Fatalf("ReasoningEffortLevels() = %v, want empty for a non-reasoning model", tc.profile.ReasoningEffortLevels())
			}
			if tc.wantReasoning && len(tc.profile.ReasoningEffortLevels()) == 0 {
				t.Fatalf("ReasoningEffortLevels() empty, want levels for a reasoning model")
			}
		})
	}
}

// The catalog's per-model default effort reaches the profile.
func TestProfile_DefaultReasoningEffortFromCatalog(t *testing.T) {
	if got := newAnthropicProfile("claude-opus-4-6").DefaultReasoningEffort(); got != "high" {
		t.Fatalf("claude-opus-4-6 DefaultReasoningEffort() = %q, want high (catalog override)", got)
	}
	if got := NewOpenAIProfile("gpt-5.5").DefaultReasoningEffort(); got != "" {
		t.Fatalf("gpt-5.5 DefaultReasoningEffort() = %q, want empty (no source states one)", got)
	}
}

// providers.toml is explicit user intent and wins over the catalog in both
// directions.
func TestProfile_ConfiguredReasoningBeatsCatalog(t *testing.T) {
	on, off := true, false
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "gw",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"gpt-4.1": {Reasoning: &on},
			"gpt-5.5": {Reasoning: &off},
		},
	}}}
	forced, err := ResolveProfileFromConfig(cfg, "gw/gpt-4.1")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}
	if !forced.SupportsReasoning() {
		t.Fatal("gpt-4.1 with reasoning = true: SupportsReasoning() = false, want true (toml wins)")
	}
	declaredOff, err := ResolveProfileFromConfig(cfg, "gw/gpt-5.5")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}
	if declaredOff.SupportsReasoning() {
		t.Fatal("gpt-5.5 with reasoning = false: SupportsReasoning() = true, want false (toml wins)")
	}
}

// A live /models entry that advertises capabilities is authoritative and may
// turn reasoning off; one that does not advertise them can only turn it on.
func TestProfile_WithLiveModelInfo_ReasoningFollowsAdvertisedCapabilities(t *testing.T) {
	base := NewOpenAIProfile("gateway-model")
	if !base.SupportsReasoning() {
		t.Fatal("fixture: uncataloged model should start permitted")
	}
	advertisedOff := base.WithLiveModelInfo(llm.ModelInfo{CapabilitiesAdvertised: true, SupportsReasoning: false})
	if advertisedOff.SupportsReasoning() {
		t.Fatal("advertised SupportsReasoning=false: SupportsReasoning() = true, want false")
	}
	if len(advertisedOff.ReasoningEffortLevels()) != 0 {
		t.Fatalf("advertised non-reasoning: levels = %v, want empty", advertisedOff.ReasoningEffortLevels())
	}
	silent := base.WithLiveModelInfo(llm.ModelInfo{SupportsReasoning: false})
	if !silent.SupportsReasoning() {
		t.Fatal("unadvertised SupportsReasoning=false: SupportsReasoning() = false, want true (silence is not knowledge)")
	}
	withDefault := base.WithLiveModelInfo(llm.ModelInfo{SupportsReasoning: true, DefaultReasoningEffort: "low"})
	if got := withDefault.DefaultReasoningEffort(); got != "low" {
		t.Fatalf("live DefaultReasoningEffort: got %q, want low", got)
	}
}

// Switching model on a provider that rebuilds via its constructor re-derives
// the model facts through construction.
func TestProfile_WithModel_RebuildRederivesReasoningFacts(t *testing.T) {
	p := newAnthropicProfile("claude-opus-4-6").WithModel("claude-3-haiku-20240307")
	if p.SupportsReasoning() {
		t.Fatal("claude-3-haiku after WithModel: SupportsReasoning() = true, want false (cataloged non-reasoning)")
	}
	if got := p.DefaultReasoningEffort(); got != "" {
		t.Fatalf("claude-3-haiku after WithModel: DefaultReasoningEffort() = %q, want empty", got)
	}
}

// google/minimax/kimi-anthropic clone rather than rebuild on a model switch;
// the shallow path must re-derive the facts too, in both directions, and a
// switch away from a non-reasoning model must restore a usable ladder — an
// empty one would defeat the clamp (max → an unclamped 131072-token Gemini
// thinking budget).
func TestProfile_WithModel_ShallowCloneRederivesReasoningFacts(t *testing.T) {
	toNonReasoning := newGeminiProfile("gemini-2.5-pro").WithModel("gemini-2.0-flash")
	if toNonReasoning.SupportsReasoning() {
		t.Fatal("gemini-2.0-flash after WithModel: SupportsReasoning() = true, want false")
	}
	if len(toNonReasoning.ReasoningEffortLevels()) != 0 {
		t.Fatalf("gemini-2.0-flash after WithModel: levels = %v, want empty", toNonReasoning.ReasoningEffortLevels())
	}

	back := toNonReasoning.WithModel("gemini-2.5-pro")
	if !back.SupportsReasoning() {
		t.Fatal("gemini-2.5-pro after WithModel back: SupportsReasoning() = false, want true")
	}
	if len(back.ReasoningEffortLevels()) == 0 {
		t.Fatal("gemini-2.5-pro after WithModel back: empty effort ladder, want the provider vocabulary restored")
	}
}

// litellm's provider-prefixed mirror entries (openrouter/*, ollama/*) carry
// shape and pricing but are sparse about supports_reasoning, so a mirror
// entry never marks a model non-reasoning: openrouter/google/gemini-2.5-pro
// and the ollama -cloud tags stay permitted. Bare curated entries (gpt-4.1)
// remain authoritative.
func TestProfile_MirrorCatalogEntriesDoNotDisableReasoning(t *testing.T) {
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{
		{Name: "openrouter", Type: "openrouter"},
		{Name: "ollama", Type: "ollama"},
	}}
	or, err := ResolveProfileFromConfig(cfg, "openrouter/google/gemini-2.5-pro")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(openrouter): %v", err)
	}
	if !or.SupportsReasoning() {
		t.Fatal("openrouter/google/gemini-2.5-pro: SupportsReasoning() = false, want true (mirror entry is not authoritative)")
	}
	ol, err := ResolveProfileFromConfig(cfg, "ollama/gpt-oss:20b-cloud")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(ollama): %v", err)
	}
	if !ol.SupportsReasoning() {
		t.Fatal("ollama/gpt-oss:20b-cloud: SupportsReasoning() = false, want true (mirror entry is not authoritative)")
	}
}

// providers.toml reasoning = true is a permission statement, not a level
// configuration: a live /models ladder must still be adopted.
func TestProfile_WithLiveModelInfo_ReasoningTrueKeepsLiveLevels(t *testing.T) {
	on := true
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "gw",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"gw-model": {Reasoning: &on},
		},
	}}}
	p, err := ResolveProfileFromConfig(cfg, "gw/gw-model")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig: %v", err)
	}
	live := p.WithLiveModelInfo(llm.ModelInfo{SupportsReasoning: true, ReasoningEffortLevels: []string{"minimal", "low", "medium", "high", "xhigh"}})
	if got := live.ReasoningEffortLevels(); len(got) != 5 || got[4] != "xhigh" {
		t.Fatalf("levels = %v, want the live ladder adopted (reasoning = true configures support, not levels)", got)
	}
}

// A live entry that turns reasoning back on for a profile whose ladder was
// emptied must restore a usable ladder, not leave the clamp toothless.
func TestProfile_WithLiveModelInfo_ReasoningOnRestoresLadder(t *testing.T) {
	base := NewOpenAIProfile("gpt-4.1") // cataloged non-reasoning: empty ladder
	live := base.WithLiveModelInfo(llm.ModelInfo{CapabilitiesAdvertised: true, SupportsReasoning: true})
	if !live.SupportsReasoning() {
		t.Fatal("SupportsReasoning() = false, want true from advertised capabilities")
	}
	if len(live.ReasoningEffortLevels()) == 0 {
		t.Fatal("empty effort ladder after live reasoning-on, want the provider vocabulary restored")
	}
}
