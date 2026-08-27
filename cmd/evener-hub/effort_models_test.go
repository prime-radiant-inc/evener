package hub

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

// wantOpus46Levels is the canonical effort-level slice for claude-opus-4-6 as
// defined in the embedded model catalog.
var wantOpus46Levels = []string{"low", "medium", "high", "max"}

// The model picker needs each model's supported reasoning-effort levels so the
// effort chip can offer per-model choices instead of a static list.
func TestEnrichModelDescriptors_IncludesReasoningEffortLevels(t *testing.T) {
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels := models[0].ReasoningEffortLevels
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("reasoning_effort_levels = %v, want %v", levels, wantOpus46Levels)
	}
}

// Provider-qualified models (e.g. an openrouter-anthropic instance serving
// "anthropic/claude-opus-4-6") must still resolve the catalog override keyed by
// the bare model id, so the effort chip offers per-model levels (incl. "max").
func TestEnrichModelDescriptors_ProviderQualifiedModelResolvesLevels(t *testing.T) {
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "openrouter-anthropic", Model: "anthropic/claude-opus-4-6"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels := models[0].ReasoningEffortLevels
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("namespaced model: reasoning_effort_levels = %v, want %v (catalog strip-prefix must resolve to opus-4-6 entry)", levels, wantOpus46Levels)
	}
}

// An instance-defined model in providers.toml must win over the embedded
// catalog: its ThinkingLevels keys, rank-ordered, are what the effort chip
// offers — even when the catalog has its own (different) opinion for the same
// bare model id.
func TestEnrichModelDescriptors_InstanceModelLevelsWinOverCatalog(t *testing.T) {
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "lunaroute",
				Type: "openai",
				Models: map[string]providercfg.ModelConfig{
					"glm-5.2-nvfp4": {
						// Deliberately out of rank order to prove the hub
						// re-sorts by llm.ReasoningEffortRank rather than
						// trusting map/TOML iteration order.
						ThinkingLevels: map[string]string{
							"xhigh":   "xhigh",
							"low":     "low",
							"medium":  "medium",
							"minimal": "minimal",
							"high":    "high",
						},
					},
				},
			},
		},
	}
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "lunaroute", Model: "glm-5.2-nvfp4"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels := models[0].ReasoningEffortLevels
	want := []string{"minimal", "low", "medium", "high", "xhigh"}
	if !reflect.DeepEqual(levels, want) {
		t.Errorf("reasoning_effort_levels = %v, want %v (rank order)", levels, want)
	}
}

// An instance model declared non-reasoning (reasoning=false) must report no
// effort levels at all, even if the catalog has levels for the same bare id.
func TestEnrichModelDescriptors_InstanceModelReasoningFalseClearsLevels(t *testing.T) {
	no := false
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "anthropic",
				Type: "anthropic",
				Models: map[string]providercfg.ModelConfig{
					"claude-opus-4-6": {Reasoning: &no},
				},
			},
		},
	}
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if len(models[0].ReasoningEffortLevels) > 0 {
		t.Errorf("reasoning_effort_levels = %v, want empty (reasoning=false)", models[0].ReasoningEffortLevels)
	}
	if models[0].SupportsReasoning == nil || *models[0].SupportsReasoning {
		t.Errorf("supports_reasoning = %v, want false", models[0].SupportsReasoning)
	}
}

// A model absent from the instance's Models map falls back to catalog
// behavior unchanged, even when the instance defines other models.
func TestEnrichModelDescriptors_ModelAbsentFromInstanceFallsBackToCatalog(t *testing.T) {
	cfg := &providercfg.Config{
		Instances: []providercfg.InstanceConfig{
			{
				Name: "anthropic",
				Type: "anthropic",
				Models: map[string]providercfg.ModelConfig{
					"some-other-model": {ContextWindow: 999},
				},
			},
		},
	}
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, cfg)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	levels := models[0].ReasoningEffortLevels
	if !reflect.DeepEqual(levels, wantOpus46Levels) {
		t.Errorf("reasoning_effort_levels = %v, want %v (unchanged catalog behavior)", levels, wantOpus46Levels)
	}
}

// reasoning = true without custom levels must still override a live/catalog
// supports_reasoning=false — the session treats the model as reasoning-capable,
// so the spawn UI must not hide the effort picker.
func TestEnrichModelDescriptors_InstanceReasoningTrueOverridesCatalog(t *testing.T) {
	on := true
	cfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "gw",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"local-reasoner": {Reasoning: &on},
		},
	}}}
	no := false
	entry := appwire.ModelDescriptor{SupportsReasoning: &no}
	applyInstanceModelOverride(&entry, cfg, "gw", "local-reasoner")
	if entry.SupportsReasoning == nil || !*entry.SupportsReasoning {
		t.Errorf("supports_reasoning = %v, want true (explicit reasoning=true beats catalog/live)", entry.SupportsReasoning)
	}
	if entry.ReasoningEffortLevels != nil {
		t.Errorf("levels should stay derived when only reasoning=true is configured: %v", entry.ReasoningEffortLevels)
	}
}

// Enrichment must never mutate the raw cached entries — the cache keeps raw
// live values so overlays stay a per-request, per-config view.
func TestEnrichModelDescriptors_DoesNotMutateInput(t *testing.T) {
	cfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:     "gw",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"m": {ContextWindow: 999_999},
		},
	}}}
	contextWindow := 100
	cached := []appwire.ModelDescriptor{{Provider: "gw", Model: "m", ContextWindow: &contextWindow}}

	out := enrichModelDescriptors(cached, cfg)
	if out[0].ContextWindow == nil || *out[0].ContextWindow != 999_999 {
		t.Fatalf("overlay context_window = %v, want 999999", out[0].ContextWindow)
	}
	if cached[0].ContextWindow == nil || *cached[0].ContextWindow != 100 {
		t.Fatalf("cached raw entry mutated: context_window = %v, want 100", cached[0].ContextWindow)
	}

	// A server with no overrides sees the raw values, not a prior overlay.
	out2 := enrichModelDescriptors(cached, &providercfg.Config{})
	if out2[0].ContextWindow == nil || *out2[0].ContextWindow != 100 {
		t.Fatalf("config-free server saw a foreign overlay: context_window = %v, want 100", out2[0].ContextWindow)
	}
}

// The hub's catalog lookup falls back to provider-qualified entries: a live
// OpenRouter listing reports bare ids whose bundled metadata may be keyed
// ONLY "openrouter/<model>" — missing those would drop the model from the
// picker as not tool-capable. The canonicalized bare lookup stays FIRST
// because LiteLLM's qualified entries often carry null capability flags
// where the canonical entry is richer (deepseek/deepseek-chat).
func TestCatalogModelInfo_QualifiedFallback(t *testing.T) {
	cat := llm.EmbeddedModelCatalog()
	// Qualified-only entry (bare "anthropic/claude-3-haiku" is absent as a
	// key; LookupModelInfo's namespace strip also misses because bare
	// "claude-3-haiku" was removed upstream): the tag-qualified fallback
	// must resolve it.
	if cat.GetModelInfo("openrouter/anthropic/claude-3-haiku") == nil {
		t.Fatal("test premise broken: openrouter/anthropic/claude-3-haiku missing from catalog")
	}
	if catalogModelInfo(cat, "openrouter", "anthropic/claude-3-haiku") == nil &&
		cat.LookupModelInfo("anthropic/claude-3-haiku") == nil {
		t.Fatal("qualified fallback missed openrouter/anthropic/claude-3-haiku")
	}
	// Richer canonical entry wins over a null-flags qualified one.
	mi := catalogModelInfo(cat, "openrouter", "deepseek/deepseek-chat")
	if mi == nil || !mi.SupportsTools {
		t.Fatalf("bare canonical precedence lost: deepseek/deepseek-chat = %+v", mi)
	}
	// Bare fallback still canonicalizes for non-qualified refs.
	if catalogModelInfo(cat, "anthropic", "claude-opus-4-6[1m]") == nil {
		t.Error("bare fallback lost [1m] canonicalization")
	}
	// behaviorTagFor: configured instance resolves its tag; unknown name
	// doubles as the tag (env-seeded convention).
	cfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name: "myrouter", Type: "openrouter",
	}}}
	if got := behaviorTagFor(cfg, "myrouter"); got != "openrouter" {
		t.Errorf("behaviorTagFor(myrouter) = %q, want openrouter", got)
	}
	if got := behaviorTagFor(cfg, "glm"); got != "glm" {
		t.Errorf("behaviorTagFor(unknown) = %q, want name-as-tag", got)
	}
}

// The hub's catalog lookup applies the ollama bare-lookup suppression: a
// local model named like an upstream entry must not show that entry's
// metadata in model/list.
func TestCatalogModelInfo_OllamaSuppressesBareLookup(t *testing.T) {
	cat := llm.EmbeddedModelCatalog()
	if cat.GetModelInfo("glm-5.2") == nil {
		t.Fatal("test premise broken: glm-5.2 missing from catalog")
	}
	if mi := catalogModelInfo(cat, "ollama", "glm-5.2"); mi != nil {
		t.Fatalf("ollama local model inherited upstream metadata: %+v", mi)
	}
}
