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
