package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelCatalogFromLiteLLMJSON_GetListLatest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	body := `{
  "sample_spec": {"litellm_provider":"openai","mode":"chat"},
  "gpt-5.2": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,"max_output_tokens":2000,
    "input_cost_per_token":0.000001,"output_cost_per_token":0.000002,
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gpt-5.2-mini": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":500,"max_output_tokens":1000,
    "supports_function_calling":true
  },
  "claude-opus-4-6": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":"200000","max_output_tokens":"8192",
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gemini-3-flash-preview": {
    "litellm_provider":"gemini","mode":"chat",
    "max_input_tokens":1000000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_vision":true
  },
  "text-embedding-3-large": {
    "litellm_provider":"openai","mode":"embedding","max_input_tokens":8191
  }
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadModelCatalogFromLiteLLMJSON(p)
	if err != nil {
		t.Fatalf("LoadModelCatalogFromLiteLLMJSON: %v", err)
	}
	// sample_spec + embedding entry should be skipped.
	if got, wantMin := len(c.Models), 4; got != wantMin {
		t.Fatalf("models: got %d want %d", got, wantMin)
	}

	mi := c.GetModelInfo("gpt-5.2")
	if mi == nil {
		t.Fatalf("GetModelInfo returned nil")
	}
	if mi.Provider != "openai" {
		t.Fatalf("provider: got %q want %q", mi.Provider, "openai")
	}
	if mi.ContextWindow != 1000 {
		t.Fatalf("context_window: got %d want %d", mi.ContextWindow, 1000)
	}
	if mi.MaxOutputTokens == nil || *mi.MaxOutputTokens != 2000 {
		t.Fatalf("max_output_tokens: got %v want %d", mi.MaxOutputTokens, 2000)
	}
	if mi.InputCostPerMillion == nil || *mi.InputCostPerMillion != 1.0 {
		t.Fatalf("input_cost_per_million: got %v want %v", mi.InputCostPerMillion, 1.0)
	}
	if mi.OutputCostPerMillion == nil || *mi.OutputCostPerMillion != 2.0 {
		t.Fatalf("output_cost_per_million: got %v want %v", mi.OutputCostPerMillion, 2.0)
	}

	opens := c.ListModels("openai")
	if got, want := len(opens), 2; got != want {
		t.Fatalf("openai models: got %d want %d", got, want)
	}
	gems := c.ListModels("google") // catalog stores gemini models under "google"
	if got, want := len(gems), 1; got != want {
		t.Fatalf("google models: got %d want %d", got, want)
	}
	if gems[0].Provider != "google" {
		t.Fatalf("gemini provider normalized: got %q want %q", gems[0].Provider, "google")
	}

	latestOpenAI := c.GetLatestModel("openai", "")
	if latestOpenAI == nil || latestOpenAI.ID != "gpt-5.2" {
		t.Fatalf("latest openai: got %+v want gpt-5.2", latestOpenAI)
	}
	latestVision := c.GetLatestModel("openai", "vision")
	if latestVision == nil || latestVision.ID != "gpt-5.2" {
		t.Fatalf("latest openai vision: got %+v want gpt-5.2", latestVision)
	}
	latestReasoning := c.GetLatestModel("google", "reasoning")
	if latestReasoning != nil {
		t.Fatalf("expected no google reasoning model in sample catalog; got %+v", latestReasoning)
	}
}

// TestModelCatalog_GeminiStoredUnderGoogle verifies that after removing the
// gemini→google lookup alias in ListModels, Gemini models loaded from the
// litellm catalog (where litellm_provider is "gemini") are stored under the
// "google" provider key and are retrievable via ListModels("google") (PRI-1880).
// The alias ListModels("gemini") is removed; callers must use "google".
func TestModelCatalog_GeminiStoredUnderGoogle(t *testing.T) {
	body := `{
  "gemini-2.5-pro": {
    "litellm_provider":"gemini","mode":"chat",
    "max_input_tokens":1000000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_vision":true
  }
}`
	cat, err := parseLiteLLMCatalog([]byte(body))
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	// The model must be stored under "google" (normalizeCatalogProvider maps "gemini"→"google").
	mi := cat.GetModelInfo("gemini-2.5-pro")
	if mi == nil {
		t.Fatal("gemini-2.5-pro not found in catalog")
	}
	if mi.Provider != "google" {
		t.Fatalf("Provider = %q, want \"google\"", mi.Provider)
	}
	// ListModels("google") must return it.
	googleModels := cat.ListModels("google")
	if len(googleModels) != 1 {
		t.Fatalf("ListModels(\"google\") returned %d models, want 1", len(googleModels))
	}
	// ListModels("gemini") must NOT return it (alias removed).
	geminiModels := cat.ListModels("gemini")
	if len(geminiModels) != 0 {
		t.Fatalf("ListModels(\"gemini\") returned %d models, want 0 (alias removed)", len(geminiModels))
	}
}

func TestParseLiteLLMCatalog(t *testing.T) {
	body := `{
  "sample_spec": {"litellm_provider":"openai","mode":"chat"},
  "gpt-5.2": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,"max_output_tokens":2000,
    "supports_function_calling":true
  }
}`
	cat, err := parseLiteLLMCatalog([]byte(body))
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	if len(cat.Models) != 1 {
		t.Fatalf("models: got %d want 1", len(cat.Models))
	}
	if cat.Models[0].ID != "gpt-5.2" {
		t.Fatalf("model ID = %q", cat.Models[0].ID)
	}
}

func TestModelCatalog_AliasLookup(t *testing.T) {
	cat := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "gpt-5.2", Provider: "openai", ContextWindow: 1000, Aliases: []string{"gpt-latest", "gpt"}},
			{ID: "claude-opus-4-6", Provider: "anthropic", ContextWindow: 200000},
		},
	}

	// Lookup by primary ID still works.
	mi := cat.GetModelInfo("gpt-5.2")
	if mi == nil || mi.ID != "gpt-5.2" {
		t.Fatalf("GetModelInfo by ID: got %v", mi)
	}

	// Lookup by alias returns the same model.
	mi = cat.GetModelInfo("gpt-latest")
	if mi == nil || mi.ID != "gpt-5.2" {
		t.Fatalf("GetModelInfo by alias 'gpt-latest': got %v", mi)
	}
	mi = cat.GetModelInfo("gpt")
	if mi == nil || mi.ID != "gpt-5.2" {
		t.Fatalf("GetModelInfo by alias 'gpt': got %v", mi)
	}

	// Alias doesn't shadow an existing model ID.
	cat2 := &ModelCatalog{
		Models: []ModelInfo{
			{ID: "model-a", Provider: "openai", Aliases: []string{"model-b"}},
			{ID: "model-b", Provider: "openai"},
		},
	}
	mi = cat2.GetModelInfo("model-b")
	if mi == nil || mi.ID != "model-b" {
		t.Fatalf("alias should not shadow existing model ID; got %v", mi)
	}
}

func TestEmbeddedModelCatalog(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("EmbeddedModelCatalog returned nil")
	}
	if len(cat.Models) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	// Spot-check a well-known model.
	info := cat.GetModelInfo("gpt-4o")
	if info == nil {
		t.Fatal("gpt-4o not found in embedded catalog")
	}
	if !strings.EqualFold(info.Provider, "openai") {
		t.Fatalf("gpt-4o provider = %q", info.Provider)
	}
}

// LookupModelInfo must canonicalize a model ref the same way regardless of who
// asks: strip the Anthropic "[1m]" 1M-context suffix, then the provider
// namespace ("anthropic/…" from an openrouter-anthropic instance), and resolve
// dated snapshots via the family override. Every ref below names the opus-4-5
// family, whose serf override lists [low, medium, high].
func TestLookupModelInfo_CanonicalizesRefs(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog nil")
	}
	refs := []string{
		"claude-opus-4-5",                        // bare
		"claude-opus-4-5[1m]",                    // 1M-context suffix
		"claude-opus-4-5-20251101",               // dated snapshot
		"claude-opus-4-5-20251101[1m]",           // dated + 1M
		"anthropic/claude-opus-4-5",              // provider-qualified
		"anthropic/claude-opus-4-5-20251101[1m]", // provider-qualified + dated + 1M
	}
	for _, ref := range refs {
		mi := cat.LookupModelInfo(ref)
		if mi == nil {
			t.Errorf("LookupModelInfo(%q) = nil, want the opus-4-5 family entry", ref)
			continue
		}
		if got := mi.ReasoningEffortLevels; len(got) != 3 {
			t.Errorf("LookupModelInfo(%q).ReasoningEffortLevels = %v, want 3 (opus-4-5 family)", ref, got)
		}
	}
	if cat.LookupModelInfo("totally-unknown-model-xyz") != nil {
		t.Error("unknown model should resolve to nil")
	}
}

// A "[1m]" ref selects the 1M-context beta, so LookupModelInfo must report a 1M
// context window for it — not the base entry's window — while leaving the base
// ref's window untouched.
func TestLookupModelInfo_OneMillionContext(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog nil")
	}
	base := cat.LookupModelInfo("claude-opus-4-5")
	if base == nil {
		t.Fatal("claude-opus-4-5 missing from catalog")
	}
	if base.ContextWindow == 1_000_000 {
		t.Fatalf("base claude-opus-4-5 ContextWindow = %d; expected the smaller base window, not 1M", base.ContextWindow)
	}
	oneM := cat.LookupModelInfo("claude-opus-4-5[1m]")
	if oneM == nil {
		t.Fatal("claude-opus-4-5[1m] did not resolve")
	}
	if oneM.ContextWindow != 1_000_000 {
		t.Errorf("claude-opus-4-5[1m] ContextWindow = %d, want 1000000", oneM.ContextWindow)
	}
	// Effort levels still resolve to the family's set for the [1m] ref.
	if len(oneM.ReasoningEffortLevels) != 3 {
		t.Errorf("claude-opus-4-5[1m] ReasoningEffortLevels = %v, want 3", oneM.ReasoningEffortLevels)
	}
}

// A dated Anthropic ref that is NOT yet in the embedded catalog (a snapshot
// newer than the bundled LiteLLM data) must still resolve to its family override
// via familyModelID, so the effort clamp gets real levels instead of falling
// back to the default max-capable set.
func TestLookupModelInfo_UncatalogedDatedRefResolvesFamily(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog nil")
	}
	// A far-future date is not present in the bundled catalog.
	if cat.GetModelInfo("claude-opus-4-5-20991231") != nil {
		t.Skip("date unexpectedly present in catalog; pick another")
	}
	for _, ref := range []string{
		"claude-opus-4-5-20991231",
		"claude-opus-4-5-20991231[1m]",
		"anthropic/claude-opus-4-5-20991231[1m]",
	} {
		mi := cat.LookupModelInfo(ref)
		if mi == nil {
			t.Errorf("LookupModelInfo(%q) = nil, want opus-4-5 family via familyModelID", ref)
			continue
		}
		if len(mi.ReasoningEffortLevels) != 3 {
			t.Errorf("LookupModelInfo(%q) levels = %v, want 3 (family)", ref, mi.ReasoningEffortLevels)
		}
	}
	if mi := cat.LookupModelInfo("claude-opus-4-5-20991231[1m]"); mi != nil && mi.ContextWindow != 1_000_000 {
		t.Errorf("[1m] context = %d, want 1000000", mi.ContextWindow)
	}
}

func TestParseLiteLLMCatalog_CacheTierPricing(t *testing.T) {
	body := `{
  "claude-opus-4-5": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":200000,
    "input_cost_per_token":0.000005,
    "output_cost_per_token":0.000025,
    "cache_read_input_token_cost":0.0000005,
    "cache_creation_input_token_cost":0.00000625,
    "cache_creation_input_token_cost_above_1hr":0.00001
  },
  "gpt-5-codex": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":272000,
    "input_cost_per_token":0.00000125,
    "output_cost_per_token":0.00001,
    "cache_read_input_token_cost":0.000000125
  },
  "no-cache-model": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,
    "input_cost_per_token":0.000001,
    "output_cost_per_token":0.000002
  }
}`
	cat, err := parseLiteLLMCatalog([]byte(body))
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}

	opus := cat.GetModelInfo("claude-opus-4-5")
	if opus == nil {
		t.Fatal("claude-opus-4-5 not found")
	}
	if opus.CacheReadInputCostPerMillion == nil || *opus.CacheReadInputCostPerMillion != 0.5 {
		t.Errorf("opus cache_read: got %v, want 0.5", opus.CacheReadInputCostPerMillion)
	}
	if opus.CacheCreation5mCostPerMillion == nil || *opus.CacheCreation5mCostPerMillion != 6.25 {
		t.Errorf("opus cache_create_5m: got %v, want 6.25", opus.CacheCreation5mCostPerMillion)
	}
	if opus.CacheCreation1hCostPerMillion == nil || *opus.CacheCreation1hCostPerMillion != 10.0 {
		t.Errorf("opus cache_create_1h: got %v, want 10.0", opus.CacheCreation1hCostPerMillion)
	}

	codex := cat.GetModelInfo("gpt-5-codex")
	if codex == nil {
		t.Fatal("gpt-5-codex not found")
	}
	if codex.CacheReadInputCostPerMillion == nil || *codex.CacheReadInputCostPerMillion != 0.125 {
		t.Errorf("codex cache_read: got %v, want 0.125", codex.CacheReadInputCostPerMillion)
	}
	if codex.CacheCreation5mCostPerMillion != nil {
		t.Errorf("codex cache_create_5m: should be nil (OpenAI has no creation cost), got %v", codex.CacheCreation5mCostPerMillion)
	}
	if codex.CacheCreation1hCostPerMillion != nil {
		t.Errorf("codex cache_create_1h: should be nil, got %v", codex.CacheCreation1hCostPerMillion)
	}

	noCache := cat.GetModelInfo("no-cache-model")
	if noCache == nil {
		t.Fatal("no-cache-model not found")
	}
	if noCache.CacheReadInputCostPerMillion != nil {
		t.Errorf("no-cache cache_read: should be nil, got %v", noCache.CacheReadInputCostPerMillion)
	}
}

func TestEmbeddedCatalog_CacheTierPricing(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Skip("no embedded catalog")
	}

	cases := []struct {
		id              string
		wantCacheRead   *float64
		wantCacheCreate *float64
	}{
		{id: "claude-opus-4-5", wantCacheRead: f64(0.5), wantCacheCreate: f64(6.25)},
		{id: "claude-sonnet-4-5", wantCacheRead: f64(0.3), wantCacheCreate: f64(3.75)},
		{id: "gpt-5-codex", wantCacheRead: f64(0.125), wantCacheCreate: nil},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			mi := cat.GetModelInfo(tc.id)
			if mi == nil {
				t.Skipf("%s not in embedded catalog", tc.id)
			}
			if !floatPtrApproxEqual(mi.CacheReadInputCostPerMillion, tc.wantCacheRead) {
				t.Errorf("cache_read: got %v, want %v", mi.CacheReadInputCostPerMillion, tc.wantCacheRead)
			}
			if !floatPtrApproxEqual(mi.CacheCreation5mCostPerMillion, tc.wantCacheCreate) {
				t.Errorf("cache_create_5m: got %v, want %v", mi.CacheCreation5mCostPerMillion, tc.wantCacheCreate)
			}
		})
	}
}

func f64(v float64) *float64 { return &v }

func floatPtrApproxEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	diff := *a - *b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

func TestParseLiteLLMCatalog_ReasoningEffortFields(t *testing.T) {
	body := `{
  "claude-opus-4-6": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":200000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_reasoning":true,
    "reasoning_effort_levels": ["low", "medium", "high", "max"],
    "supports_adaptive_thinking": true,
    "supports_effort_parameter": true
  },
  "claude-opus-4-5": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":200000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_reasoning":true,
    "reasoning_effort_levels": ["low", "medium", "high"],
    "supports_adaptive_thinking": false,
    "supports_effort_parameter": true
  },
  "claude-sonnet-4-5": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":200000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_reasoning":true,
    "reasoning_effort_levels": ["low", "medium", "high"]
  }
}`
	cat, err := parseLiteLLMCatalog([]byte(body))
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	if len(cat.Models) != 3 {
		t.Fatalf("models: got %d want 3", len(cat.Models))
	}

	// Test Opus 4.6: adaptive + effort
	opus46 := cat.GetModelInfo("claude-opus-4-6")
	if opus46 == nil {
		t.Fatal("claude-opus-4-6 not found")
	}
	if got := opus46.ReasoningEffortLevels; len(got) != 4 || got[0] != "low" || got[3] != "max" {
		t.Fatalf("opus-4-6 effort levels: got %v, want [low medium high max]", got)
	}
	if !opus46.SupportsAdaptiveThinking {
		t.Fatal("opus-4-6 SupportsAdaptiveThinking should be true")
	}
	if !opus46.SupportsEffortParameter {
		t.Fatal("opus-4-6 SupportsEffortParameter should be true")
	}

	// Test Opus 4.5: manual + effort (hybrid)
	opus45 := cat.GetModelInfo("claude-opus-4-5")
	if opus45 == nil {
		t.Fatal("claude-opus-4-5 not found")
	}
	if got := opus45.ReasoningEffortLevels; len(got) != 3 || got[2] != "high" {
		t.Fatalf("opus-4-5 effort levels: got %v, want [low medium high]", got)
	}
	if opus45.SupportsAdaptiveThinking {
		t.Fatal("opus-4-5 SupportsAdaptiveThinking should be false")
	}
	if !opus45.SupportsEffortParameter {
		t.Fatal("opus-4-5 SupportsEffortParameter should be true")
	}

	// Test Sonnet 4.5: manual only (no effort fields means defaults to false)
	sonnet45 := cat.GetModelInfo("claude-sonnet-4-5")
	if sonnet45 == nil {
		t.Fatal("claude-sonnet-4-5 not found")
	}
	if got := sonnet45.ReasoningEffortLevels; len(got) != 3 {
		t.Fatalf("sonnet-4-5 effort levels: got %v, want [low medium high]", got)
	}
	if sonnet45.SupportsAdaptiveThinking {
		t.Fatal("sonnet-4-5 SupportsAdaptiveThinking should default to false")
	}
	if sonnet45.SupportsEffortParameter {
		t.Fatal("sonnet-4-5 SupportsEffortParameter should default to false")
	}
}

// TestParseLiteLLMCatalog_WebSearchPresence verifies that
// SupportsWebSearch is presence-aware: the parsed ModelInfo carries a
// distinguishable "field absent" vs "explicitly false" vs "explicitly
// true" state. Without that, downstream callers can't tell whether the
// catalog is silent on web-search support (so the constructor default
// should win) or has explicitly disabled it (which must be honored).
func TestParseLiteLLMCatalog_WebSearchPresence(t *testing.T) {
	data := []byte(`{
        "model-explicit-true":  {"litellm_provider": "x", "supports_web_search": true},
        "model-explicit-false": {"litellm_provider": "x", "supports_web_search": false},
        "model-field-absent":   {"litellm_provider": "x"}
    }`)
	cat, err := parseLiteLLMCatalog(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	mTrue := cat.GetModelInfo("model-explicit-true")
	if mTrue == nil || mTrue.SupportsWebSearch == nil {
		t.Fatal("explicit-true: SupportsWebSearch is nil, want non-nil pointer to true")
	}
	if !*mTrue.SupportsWebSearch {
		t.Fatal("explicit-true: *SupportsWebSearch = false, want true")
	}

	mFalse := cat.GetModelInfo("model-explicit-false")
	if mFalse == nil || mFalse.SupportsWebSearch == nil {
		t.Fatal("explicit-false: SupportsWebSearch is nil, want non-nil pointer to false (must distinguish from absent)")
	}
	if *mFalse.SupportsWebSearch {
		t.Fatal("explicit-false: *SupportsWebSearch = true, want false")
	}

	mAbsent := cat.GetModelInfo("model-field-absent")
	if mAbsent == nil {
		t.Fatal("absent: model not found")
	}
	if mAbsent.SupportsWebSearch != nil {
		t.Fatalf("absent: SupportsWebSearch = %v, want nil (field omitted in catalog)", *mAbsent.SupportsWebSearch)
	}
}

// TestApplyOverrides_MergesWebSearch verifies that the serf override
// layer can flip supports_web_search on a base catalog entry. Before
// this, only effort_levels and a couple of effort flags were merged,
// so an override saying supports_web_search:false was silently lost.
func TestApplyOverrides_MergesWebSearch(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{
        "some-model": {"litellm_provider": "x", "supports_web_search": true}
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	applyOverrides(cat, []byte(`{"some-model": {"supports_web_search": false}}`))

	mi := cat.GetModelInfo("some-model")
	if mi == nil || mi.SupportsWebSearch == nil {
		t.Fatal("merged entry has SupportsWebSearch == nil; override should set it")
	}
	if *mi.SupportsWebSearch {
		t.Fatal("*SupportsWebSearch = true, want false (override flipped it)")
	}
}

// Dated Anthropic snapshots (claude-opus-4-5-20251101, ...-v1) carry no override
// of their own — Serf overrides are keyed on the bare family ID. They must
// inherit the family's effort metadata so the effort clamp resolves real levels
// for a dated fallback instead of leaking the primary model's levels.
func TestApplyOverrides_DatedVariantInheritsFamily(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{
        "claude-opus-4-5":             {"litellm_provider": "anthropic"},
        "claude-opus-4-5-20251101":    {"litellm_provider": "anthropic"},
        "claude-opus-4-5-20251101-v1": {"litellm_provider": "anthropic"}
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyOverrides(cat, []byte(`{"claude-opus-4-5": {"reasoning_effort_levels": ["low","medium","high"], "supports_effort_parameter": true}}`))

	for _, id := range []string{"claude-opus-4-5", "claude-opus-4-5-20251101", "claude-opus-4-5-20251101-v1"} {
		mi := cat.GetModelInfo(id)
		if mi == nil {
			t.Fatalf("%s: missing from catalog", id)
		}
		if got := mi.ReasoningEffortLevels; len(got) != 3 {
			t.Errorf("%s: ReasoningEffortLevels = %v, want [low medium high] (dated variant should inherit family override)", id, got)
		}
		if !mi.SupportsEffortParameter {
			t.Errorf("%s: SupportsEffortParameter = false, want true (inherited from family)", id)
		}
	}
}

// A dated entry with its OWN override keeps it — exact match wins over the family
// fallback.
func TestApplyOverrides_ExactDatedOverrideWinsOverFamily(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{
        "claude-opus-4-5":          {"litellm_provider": "anthropic"},
        "claude-opus-4-5-20251101": {"litellm_provider": "anthropic"}
    }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyOverrides(cat, []byte(`{
        "claude-opus-4-5":          {"reasoning_effort_levels": ["low","medium","high"]},
        "claude-opus-4-5-20251101": {"reasoning_effort_levels": ["low"]}
    }`))

	if got := cat.GetModelInfo("claude-opus-4-5-20251101").ReasoningEffortLevels; len(got) != 1 || got[0] != "low" {
		t.Errorf("dated exact override should win: got %v, want [low]", got)
	}
}

func TestFamilyModelID(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-5-20251101":    "claude-opus-4-5",
		"claude-opus-4-5-20251101-v1": "claude-opus-4-5",
		"claude-3-5-sonnet-20240620":  "claude-3-5-sonnet",
		"claude-opus-4-5":             "claude-opus-4-5", // no date → unchanged
		"gpt-4o":                      "gpt-4o",          // no date → unchanged
		"model-v1":                    "model-v1",        // bare -vN (no date) → unchanged
	}
	for in, want := range cases {
		if got := familyModelID(in); got != want {
			t.Errorf("familyModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

// A Serf override entry that carries base metadata (a context window) and matches
// no LiteLLM model materializes a Serf-only catalog entry — how Serf ships models
// LiteLLM doesn't cover (kimi-for-coding). Overlay-only entries stay no-ops.
func TestApplyOverrides_MaterializesSerfOnlyModel(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{"existing-model": {"litellm_provider": "x"}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyOverrides(cat, []byte(`{
		"serf-only":    {"provider": "kimi", "context_window": 262144, "supports_reasoning": true, "reasoning_effort_levels": ["minimal","low","medium","high"]},
		"overlay-only": {"reasoning_effort_levels": ["low"]}
	}`))

	mi := cat.GetModelInfo("serf-only")
	if mi == nil {
		t.Fatal("serf-only model was not materialized")
	}
	if mi.ContextWindow != 262144 {
		t.Errorf("ContextWindow = %d, want 262144", mi.ContextWindow)
	}
	if len(mi.ReasoningEffortLevels) != 4 {
		t.Errorf("ReasoningEffortLevels = %v, want 4", mi.ReasoningEffortLevels)
	}
	// overlay-only carries no base metadata and matches no model → must NOT appear.
	if cat.GetModelInfo("overlay-only") != nil {
		t.Error("overlay-only entry (no context_window) should not create a catalog model")
	}
}

// TestApplyOverrides_MaxOutputTokens verifies the overrides schema carries
// max_output_tokens onto both a materialized Serf-only entry and an overlay of
// an existing catalog model.
func TestApplyOverrides_MaxOutputTokens(t *testing.T) {
	cat, err := parseLiteLLMCatalog([]byte(`{"existing-model": {"litellm_provider": "x", "max_input_tokens": 4096}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	applyOverrides(cat, []byte(`{
		"serf-only":      {"provider": "zai", "context_window": 1000000, "max_output_tokens": 131072},
		"existing-model": {"max_output_tokens": 8192}
	}`))

	serfOnly := cat.GetModelInfo("serf-only")
	if serfOnly == nil {
		t.Fatal("serf-only model was not materialized")
	}
	if serfOnly.MaxOutputTokens == nil || *serfOnly.MaxOutputTokens != 131072 {
		t.Errorf("serf-only MaxOutputTokens = %v, want 131072", serfOnly.MaxOutputTokens)
	}

	existing := cat.GetModelInfo("existing-model")
	if existing == nil {
		t.Fatal("existing-model missing")
	}
	if existing.MaxOutputTokens == nil || *existing.MaxOutputTokens != 8192 {
		t.Errorf("existing-model MaxOutputTokens = %v, want 8192 (overlay)", existing.MaxOutputTokens)
	}
}

// TestEmbeddedCatalog_ZAIGLMModels verifies the stock z.ai GLM catalog defaults
// ship with zero providers.toml config: context windows, output caps, and the
// glm-5.2 effort ladder derived from Pi's thinkingLevelMap.
func TestEmbeddedCatalog_ZAIGLMModels(t *testing.T) {
	cat := EmbeddedModelCatalog()

	// glm-5.2: reasoning_effort supported, wire-spelled ladder ["high","max"].
	glm52 := cat.GetModelInfo("glm-5.2")
	if glm52 == nil {
		t.Fatal("glm-5.2 missing from embedded catalog")
	}
	if glm52.ContextWindow != 1000000 {
		t.Errorf("glm-5.2 ContextWindow = %d, want 1000000", glm52.ContextWindow)
	}
	if glm52.MaxOutputTokens == nil || *glm52.MaxOutputTokens != 131072 {
		t.Errorf("glm-5.2 MaxOutputTokens = %v, want 131072", glm52.MaxOutputTokens)
	}
	if !glm52.SupportsEffortParameter {
		t.Error("glm-5.2 SupportsEffortParameter should be true")
	}
	if got := glm52.ReasoningEffortLevels; len(got) != 2 || got[0] != "high" || got[1] != "max" {
		t.Errorf("glm-5.2 ReasoningEffortLevels = %v, want [high max]", got)
	}

	// glm-4.7: no effort param — keeps default ladder, thinking on/off only.
	glm47 := cat.GetModelInfo("glm-4.7")
	if glm47 == nil {
		t.Fatal("glm-4.7 missing from embedded catalog")
	}
	if glm47.ContextWindow != 204800 {
		t.Errorf("glm-4.7 ContextWindow = %d, want 204800", glm47.ContextWindow)
	}
	if glm47.MaxOutputTokens == nil || *glm47.MaxOutputTokens != 131072 {
		t.Errorf("glm-4.7 MaxOutputTokens = %v, want 131072", glm47.MaxOutputTokens)
	}
	if glm47.SupportsEffortParameter {
		t.Error("glm-4.7 SupportsEffortParameter should be false (no effort param)")
	}
	if len(glm47.ReasoningEffortLevels) != 0 {
		t.Errorf("glm-4.7 ReasoningEffortLevels = %v, want empty (default ladder)", glm47.ReasoningEffortLevels)
	}

	// glm-4.5-air output cap differs (98304).
	air := cat.GetModelInfo("glm-4.5-air")
	if air == nil {
		t.Fatal("glm-4.5-air missing from embedded catalog")
	}
	if air.ContextWindow != 131072 {
		t.Errorf("glm-4.5-air ContextWindow = %d, want 131072", air.ContextWindow)
	}
	if air.MaxOutputTokens == nil || *air.MaxOutputTokens != 98304 {
		t.Errorf("glm-4.5-air MaxOutputTokens = %v, want 98304", air.MaxOutputTokens)
	}
}

// TestEmbeddedCatalog_DeepSeekV4Models verifies DeepSeek v4 catalog defaults:
// both models support the effort parameter with the ["high","max"] ladder.
func TestEmbeddedCatalog_DeepSeekV4Models(t *testing.T) {
	cat := EmbeddedModelCatalog()
	for _, id := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		mi := cat.GetModelInfo(id)
		if mi == nil {
			t.Fatalf("%s missing from embedded catalog", id)
		}
		if mi.ContextWindow != 1000000 {
			t.Errorf("%s ContextWindow = %d, want 1000000", id, mi.ContextWindow)
		}
		// Upstream now defines these models; the slimmed override defers the
		// output cap to LiteLLM's 8192 (an over-cap 400s at the provider).
		if mi.MaxOutputTokens == nil || *mi.MaxOutputTokens != 8192 {
			t.Errorf("%s MaxOutputTokens = %v, want upstream's 8192", id, mi.MaxOutputTokens)
		}
		if !mi.SupportsEffortParameter {
			t.Errorf("%s SupportsEffortParameter should be true", id)
		}
		// Upstream now defines these models WITHOUT the reasoning flag; the
		// override must overlay it or the spawn UI hides the effort picker.
		if !mi.SupportsReasoning {
			t.Errorf("%s SupportsReasoning should be true (override overlays matched models)", id)
		}
		if got := mi.ReasoningEffortLevels; len(got) != 2 || got[0] != "high" || got[1] != "max" {
			t.Errorf("%s ReasoningEffortLevels = %v, want [high max]", id, got)
		}
	}
}

func TestEmbeddedCatalog_KimiForCoding(t *testing.T) {
	cat := EmbeddedModelCatalog()
	mi := cat.GetModelInfo("kimi-for-coding")
	if mi == nil {
		t.Fatal("kimi-for-coding missing from embedded catalog")
	}
	if mi.ContextWindow != 262144 {
		t.Errorf("kimi-for-coding ContextWindow = %d, want 262144", mi.ContextWindow)
	}
	if len(mi.ReasoningEffortLevels) == 0 {
		t.Error("kimi-for-coding has no reasoning_effort_levels")
	}
	// kimi-for-coding is tool-capable (verified end-to-end: it drives delegate +
	// shell). The catalog must reflect that, not the no-tools default.
	if !mi.SupportsTools {
		t.Error("kimi-for-coding should be SupportsTools=true")
	}
}

// A context_window override must also win for models the upstream catalog
// already defines (not just materialized serf-only entries) — the overrides
// layer is authoritative, so upstream later adding one of our curated models
// can't regress its shape.
func TestApplyOverrides_ContextWindowOverlaysMatchedModel(t *testing.T) {
	base := []byte(`{"m-upstream": {"litellm_provider": "openai", "mode": "chat", "max_input_tokens": 100}}`)
	overrides := []byte(`{"m-upstream": {"context_window": 999}}`)
	cat, err := parseLiteLLMCatalog(base)
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	applyOverrides(cat, overrides)
	mi := cat.GetModelInfo("m-upstream")
	if mi == nil {
		t.Fatal("m-upstream missing after overlay")
	}
	if mi.ContextWindow != 999 {
		t.Errorf("ContextWindow = %d, want 999 (override beats upstream)", mi.ContextWindow)
	}
}

// Non-model top-level objects in the upstream file (fallback_generalizations,
// alongside the already-skipped sample_spec) must not ingest as bogus models.
func TestParseLiteLLMCatalog_SkipsNonModelObjects(t *testing.T) {
	src := []byte(`{
		"sample_spec": {"mode": "chat"},
		"fallback_generalizations": {"rules": [{"name": "x"}]},
		"real-model": {"litellm_provider": "openai", "mode": "chat", "max_input_tokens": 100}
	}`)
	cat, err := parseLiteLLMCatalog(src)
	if err != nil {
		t.Fatalf("parseLiteLLMCatalog: %v", err)
	}
	if cat.GetModelInfo("fallback_generalizations") != nil {
		t.Error("fallback_generalizations ingested as a model")
	}
	if cat.GetModelInfo("real-model") == nil {
		t.Error("real model dropped")
	}
}
