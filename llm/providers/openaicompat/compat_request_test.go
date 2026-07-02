package openaicompat

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func boolp(v bool) *bool { return &v }
func intp(v int) *int    { return &v }
func strp(v string) *string {
	return &v
}

func TestApplyCompatConfig_NilLeavesQuirksUnchanged(t *testing.T) {
	base := QuirksPreset("glm-5")
	got := ApplyCompatConfig(base, nil)
	if !reflect.DeepEqual(got, base) {
		t.Errorf("ApplyCompatConfig(base, nil) = %+v, want %+v", got, base)
	}
}

func TestApplyCompatConfig_OverlaysFieldByField(t *testing.T) {
	base := ProviderQuirks{
		LockTemperature: true,
		ThinkingFormat:  "zai",
		FinishReasonMap: map[string]string{"sensitive": "content_filter"},
	}
	got := ApplyCompatConfig(base, &providercfg.CompatConfig{
		ThinkingFormat:                      "openai",
		SupportsReasoningEffort:             boolp(true),
		MaxTokensField:                      "max_completion_tokens",
		ToolStream:                          boolp(true),
		SupportsStore:                       boolp(true),
		SupportsDeveloperRole:               boolp(true),
		SupportsUsageInStreaming:            boolp(false),
		RequiresToolResultName:              boolp(true),
		RequiresAssistantAfterToolResult:    boolp(true),
		RequiresThinkingAsText:              boolp(true),
		RequiresReasoningContentOnAssistant: boolp(true),
		CacheControlFormat:                  "anthropic",
		LockTemperature:                     boolp(false),
		MaxStopSequences:                    intp(2),
		FinishReasonMap:                     map[string]string{"end": "stop"},
		TranslateMaxToXHigh:                 boolp(true),
	})
	if got.ThinkingFormat != "openai" {
		t.Errorf("ThinkingFormat = %q, want openai", got.ThinkingFormat)
	}
	if got.SupportsReasoningEffort == nil || !*got.SupportsReasoningEffort {
		t.Errorf("SupportsReasoningEffort = %v, want true", got.SupportsReasoningEffort)
	}
	if got.MaxTokensField != "max_completion_tokens" {
		t.Errorf("MaxTokensField = %q", got.MaxTokensField)
	}
	if !got.ToolStream || !got.SendStoreFalse || !got.UseDeveloperRole || !got.OmitStreamUsage {
		t.Errorf("wire flags = %+v, want tool_stream/store/developer/omit-usage all set", got)
	}
	if !got.RequireToolResultName || !got.RequireAssistantAfterToolResult || !got.ThinkingAsText || !got.EmptyReasoningContentOnAssistant {
		t.Errorf("message quirks = %+v, want all set", got)
	}
	if got.CacheControlFormat != "anthropic" {
		t.Errorf("CacheControlFormat = %q", got.CacheControlFormat)
	}
	if got.LockTemperature {
		t.Error("LockTemperature = true, want overridden to false")
	}
	if got.MaxStopSequences != 2 {
		t.Errorf("MaxStopSequences = %d, want 2", got.MaxStopSequences)
	}
	if !reflect.DeepEqual(got.FinishReasonMap, map[string]string{"end": "stop"}) {
		t.Errorf("FinishReasonMap = %v, want full replace", got.FinishReasonMap)
	}
	if !got.TranslateMaxToXHigh {
		t.Error("TranslateMaxToXHigh = false, want true")
	}
	// Unset fields inherit.
	got2 := ApplyCompatConfig(base, &providercfg.CompatConfig{})
	if !got2.LockTemperature || got2.ThinkingFormat != "zai" || got2.FinishReasonMap["sensitive"] != "content_filter" {
		t.Errorf("empty compat overlay changed inherited fields: %+v", got2)
	}
}

func TestNewForInstance_ResolvesPerModelCompat(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "lunaroute",
		BaseURL: "https://gw.example.com/v1",
		Quirks:  QuirksPreset("glm-5"),
		Compat: &providercfg.CompatConfig{
			ThinkingFormat: "zai",
		},
		Models: map[string]providercfg.ModelConfig{
			"glm-5.2-nvfp4": {
				MaxOutputTokens: 131072,
				ThinkingLevels: map[string]string{
					"minimal": "high", "low": "high", "medium": "high", "high": "high", "xhigh": "max",
				},
				Compat: &providercfg.CompatConfig{
					SupportsReasoningEffort: boolp(true),
				},
			},
		},
	})

	// Instance-wide quirks: preset overlaid with instance compat.
	if a.Quirks.ThinkingFormat != "zai" {
		t.Errorf("instance ThinkingFormat = %q, want zai", a.Quirks.ThinkingFormat)
	}
	if !a.Quirks.StripEmptyContent {
		t.Error("instance quirks lost glm-5 preset StripEmptyContent")
	}

	mc := a.compatFor("glm-5.2-nvfp4")
	if mc.Quirks.ThinkingFormat != "zai" {
		t.Errorf("model ThinkingFormat = %q, want zai (inherited)", mc.Quirks.ThinkingFormat)
	}
	if mc.Quirks.SupportsReasoningEffort == nil || !*mc.Quirks.SupportsReasoningEffort {
		t.Errorf("model SupportsReasoningEffort = %v, want true", mc.Quirks.SupportsReasoningEffort)
	}
	if mc.DefaultMaxTokens != 131072 {
		t.Errorf("DefaultMaxTokens = %d, want 131072", mc.DefaultMaxTokens)
	}
	if mc.ThinkingLevels["xhigh"] != "max" {
		t.Errorf("ThinkingLevels = %v, want xhigh→max", mc.ThinkingLevels)
	}

	// Unknown model falls back to instance quirks with no level map.
	other := a.compatFor("other-model")
	if other.Quirks.ThinkingFormat != "zai" || other.ThinkingLevels != nil || other.DefaultMaxTokens != 0 {
		t.Errorf("compatFor(other) = %+v, want bare instance quirks", other)
	}
}

// TestCompatFor_CatalogFillsDefaults verifies that, for a model with no
// providers.toml [instances.X.models] entry, compatFor fills the reasoning-effort
// gate and output cap from the embedded catalog — positive-only for the gate.
func TestCompatFor_CatalogFillsDefaults(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "glm",
		BaseURL: "https://api.z.ai/api/coding/paas/v4",
		Quirks:  QuirksPreset("glm-5"),
	})

	// glm-5.2: catalog declares supports_effort_parameter → gate flips on, and
	// max_output_tokens seeds DefaultMaxTokens.
	mc := a.compatFor("glm-5.2")
	if mc.Quirks.SupportsReasoningEffort == nil || !*mc.Quirks.SupportsReasoningEffort {
		t.Errorf("glm-5.2 SupportsReasoningEffort = %v, want true (from catalog)", mc.Quirks.SupportsReasoningEffort)
	}
	if mc.DefaultMaxTokens != 131072 {
		t.Errorf("glm-5.2 DefaultMaxTokens = %d, want 131072 (from catalog)", mc.DefaultMaxTokens)
	}

	// glm-4.7: catalog has supports_effort_parameter=false → gate must NOT flip
	// (stays nil so the zai format default OFF is preserved). Output cap still fills.
	mc47 := a.compatFor("glm-4.7")
	if mc47.Quirks.SupportsReasoningEffort != nil {
		t.Errorf("glm-4.7 SupportsReasoningEffort = %v, want nil (catalog must not flip the zai default)", mc47.Quirks.SupportsReasoningEffort)
	}
	if mc47.DefaultMaxTokens != 131072 {
		t.Errorf("glm-4.7 DefaultMaxTokens = %d, want 131072 (from catalog)", mc47.DefaultMaxTokens)
	}

	// A model absent from the catalog gets the bare instance quirks.
	unknown := a.compatFor("totally-unknown-model")
	if unknown.Quirks.SupportsReasoningEffort != nil || unknown.DefaultMaxTokens != 0 {
		t.Errorf("unknown model = %+v, want bare instance quirks", unknown)
	}
}

// TestCompatFor_CatalogNeverTurnsEffortOff verifies the positive-only asymmetry:
// an instance whose format defaults the gate on (openai) is never regressed by a
// catalog model that lacks supports_effort_parameter.
func TestCompatFor_CatalogNeverTurnsEffortOff(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "compat",
		BaseURL: "https://gw.example.com/v1",
		// openai default format: gate defaults on when SupportsReasoningEffort is nil.
	})
	mc := a.compatFor("glm-4.7") // catalog says the effort param is unsupported
	if mc.Quirks.SupportsReasoningEffort != nil {
		t.Errorf("SupportsReasoningEffort = %v, want nil (never turned OFF from catalog)", mc.Quirks.SupportsReasoningEffort)
	}
	// The openai format default still resolves to on.
	if !mc.Quirks.supportsEffort(true) {
		t.Error("openai-format gate should stay on (catalog must not regress it)")
	}
}

// TestCompatFor_InstanceEntryWinsOverCatalog verifies a providers.toml model
// entry is authoritative — the catalog fallback is not consulted for it.
func TestCompatFor_InstanceEntryWinsOverCatalog(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "glm",
		BaseURL: "https://api.z.ai/api/coding/paas/v4",
		Quirks:  QuirksPreset("glm-5"),
		Models: map[string]providercfg.ModelConfig{
			"glm-5.2": {MaxOutputTokens: 4096},
		},
	})
	mc := a.compatFor("glm-5.2")
	if mc.DefaultMaxTokens != 4096 {
		t.Errorf("DefaultMaxTokens = %d, want 4096 (instance entry wins)", mc.DefaultMaxTokens)
	}
	// Instance entry declared no reasoning-effort support and the catalog is not
	// consulted, so the gate stays the zai default (nil).
	if mc.Quirks.SupportsReasoningEffort != nil {
		t.Errorf("SupportsReasoningEffort = %v, want nil (catalog not consulted for instance entry)", mc.Quirks.SupportsReasoningEffort)
	}
}

// TestGLMCatalogDefaults_WireBody is the end-to-end wire check: a stock glm
// instance (no per-model config) drives correct thinking/effort/max_tokens for
// GLM models purely from the catalog defaults.
func TestGLMCatalogDefaults_WireBody(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "glm",
		BaseURL: "https://api.z.ai/api/coding/paas/v4",
		Quirks:  QuirksPreset("glm-5"),
	})

	// glm-5.2 with effort max: thinking enabled + reasoning_effort:"max" + catalog max_tokens.
	req52 := plainReq("glm-5.2")
	req52.ReasoningEffort = strp("max")
	body52 := requestBody(t, req52, false, a.compatFor("glm-5.2"))
	wantThinking := map[string]any{"type": "enabled", "clear_thinking": false}
	if got := body52["thinking"]; !reflect.DeepEqual(got, wantThinking) {
		t.Errorf("glm-5.2 thinking = %#v, want %#v", got, wantThinking)
	}
	if got := body52["reasoning_effort"]; got != "max" {
		t.Errorf("glm-5.2 reasoning_effort = %v, want max", got)
	}
	if got := body52["max_tokens"]; got != 131072 {
		t.Errorf("glm-5.2 max_tokens = %v, want 131072", got)
	}

	// glm-4.7 with effort high: thinking enabled, NO reasoning_effort, catalog max_tokens.
	req47 := plainReq("glm-4.7")
	req47.ReasoningEffort = strp("high")
	body47 := requestBody(t, req47, false, a.compatFor("glm-4.7"))
	if got := body47["thinking"]; !reflect.DeepEqual(got, wantThinking) {
		t.Errorf("glm-4.7 thinking = %#v, want %#v", got, wantThinking)
	}
	if _, present := body47["reasoning_effort"]; present {
		t.Errorf("glm-4.7 reasoning_effort present = %v, want absent", body47["reasoning_effort"])
	}
	if got := body47["max_tokens"]; got != 131072 {
		t.Errorf("glm-4.7 max_tokens = %v, want 131072", got)
	}
}

// TestReasoningOff_SuppressesAllThinkingFormats verifies that a model with
// ReasoningOff set (mirroring providers.toml [instances.X.models.*]
// reasoning=false) emits no reasoning-effort/thinking wire field for ANY
// thinking format, even when the request sets ReasoningEffort directly. A
// direct llm.Client caller — not just the session-side profile clamp — must
// not be able to force reasoning controls onto a declared non-reasoning
// model.
func TestReasoningOff_SuppressesAllThinkingFormats(t *testing.T) {
	req := plainReq("m")
	req.ReasoningEffort = strp("high")

	for _, format := range []string{"", "openai", "zai", "deepseek", "openrouter", "together", "qwen", "string-thinking"} {
		t.Run(format, func(t *testing.T) {
			mc := ModelCompat{
				Quirks:       ProviderQuirks{ThinkingFormat: format, SupportsReasoningEffort: boolp(true)},
				ReasoningOff: true,
			}
			body := requestBody(t, req, false, mc)
			for _, key := range []string{"reasoning_effort", "thinking", "reasoning", "enable_thinking", "chat_template_kwargs"} {
				if got, present := body[key]; present {
					t.Errorf("body[%q] = %v, want absent (ReasoningOff)", key, got)
				}
			}
		})
	}
}

// TestReasoningOff_FromModelConfig verifies resolveModelCompat sets
// ReasoningOff from a providers.toml model's reasoning=false, and that a
// nil/true Reasoning leaves it false.
func TestReasoningOff_FromModelConfig(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:    "glm",
		BaseURL: "https://api.z.ai/api/coding/paas/v4",
		Quirks:  QuirksPreset("glm-5"),
		Models: map[string]providercfg.ModelConfig{
			"glm-flash": {Reasoning: boolp(false)},
			"glm-5.2":   {Reasoning: boolp(true)},
			"glm-4.7":   {},
		},
	})
	if !a.Models["glm-flash"].ReasoningOff {
		t.Error("glm-flash ReasoningOff = false, want true (reasoning=false)")
	}
	if a.Models["glm-5.2"].ReasoningOff {
		t.Error("glm-5.2 ReasoningOff = true, want false (reasoning=true)")
	}
	if a.Models["glm-4.7"].ReasoningOff {
		t.Error("glm-4.7 ReasoningOff = true, want false (unset)")
	}

	// End-to-end: buildRequestBody for the declared non-reasoning model emits
	// nothing even with an explicit request effort.
	req := plainReq("glm-flash")
	req.ReasoningEffort = strp("high")
	body := requestBody(t, req, false, a.compatFor("glm-flash"))
	for _, key := range []string{"reasoning_effort", "thinking"} {
		if got, present := body[key]; present {
			t.Errorf("glm-flash body[%q] = %v, want absent", key, got)
		}
	}
}

// TestCompatFor_SuppressCatalogDefaults verifies the ollama-style opt-out:
// when SuppressCatalogDefaults is set, an undeclared model that happens to
// share a name with a real catalog entry (glm-5.2) gets none of that entry's
// defaults. The false (default) case keeps the existing catalog-fill
// behavior, covered by TestCompatFor_CatalogFillsDefaults.
func TestCompatFor_SuppressCatalogDefaults(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		Name:                    "local-ollama",
		BaseURL:                 "http://localhost:11434/v1",
		SuppressCatalogDefaults: true,
	})
	mc := a.compatFor("glm-5.2")
	if mc.Quirks.SupportsReasoningEffort != nil {
		t.Errorf("SupportsReasoningEffort = %v, want nil (catalog suppressed)", mc.Quirks.SupportsReasoningEffort)
	}
	if mc.DefaultMaxTokens != 0 {
		t.Errorf("DefaultMaxTokens = %d, want 0 (catalog suppressed)", mc.DefaultMaxTokens)
	}
}

// requestBody builds a body through the model-aware path.
func requestBody(t *testing.T, req llm.Request, stream bool, mc ModelCompat) map[string]any {
	t.Helper()
	body, err := buildRequestBody(req, stream, mc)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	return body
}

func plainReq(model string) llm.Request {
	return llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		},
	}
}

func TestThinkingFormats_WireShapes(t *testing.T) {
	effortReq := plainReq("m")
	effortReq.ReasoningEffort = strp("high")

	cases := []struct {
		name   string
		quirks ProviderQuirks
		req    llm.Request
		want   map[string]any // key → expected value (nil = must be absent)
	}{
		{
			name:   "openai default sends reasoning_effort",
			quirks: ProviderQuirks{},
			req:    effortReq,
			want:   map[string]any{"reasoning_effort": "high", "thinking": nil, "reasoning": nil},
		},
		{
			name:   "openai format respects supports_reasoning_effort=false",
			quirks: ProviderQuirks{SupportsReasoningEffort: boolp(false)},
			req:    effortReq,
			want:   map[string]any{"reasoning_effort": nil},
		},
		{
			name:   "zai sends thinking enabled without reasoning_effort by default",
			quirks: ProviderQuirks{ThinkingFormat: "zai"},
			req:    effortReq,
			want: map[string]any{
				"thinking":         map[string]any{"type": "enabled", "clear_thinking": false},
				"reasoning_effort": nil,
			},
		},
		{
			name:   "zai with supports_reasoning_effort also sends effort",
			quirks: ProviderQuirks{ThinkingFormat: "zai", SupportsReasoningEffort: boolp(true)},
			req:    effortReq,
			want: map[string]any{
				"thinking":         map[string]any{"type": "enabled", "clear_thinking": false},
				"reasoning_effort": "high",
			},
		},
		{
			name:   "zai with no effort omits thinking (provider default rules)",
			quirks: ProviderQuirks{ThinkingFormat: "zai"},
			req:    plainReq("m"),
			want:   map[string]any{"thinking": nil, "reasoning_effort": nil},
		},
		{
			name:   "deepseek sends thinking enabled plus reasoning_effort",
			quirks: ProviderQuirks{ThinkingFormat: "deepseek"},
			req:    effortReq,
			want: map[string]any{
				"thinking":         map[string]any{"type": "enabled"},
				"reasoning_effort": "high",
			},
		},
		{
			name:   "openrouter nests reasoning.effort",
			quirks: ProviderQuirks{ThinkingFormat: "openrouter"},
			req:    effortReq,
			want: map[string]any{
				"reasoning":        map[string]any{"effort": "high"},
				"reasoning_effort": nil,
			},
		},
		{
			name:   "together sends reasoning.enabled plus effort",
			quirks: ProviderQuirks{ThinkingFormat: "together"},
			req:    effortReq,
			want: map[string]any{
				"reasoning":        map[string]any{"enabled": true},
				"reasoning_effort": "high",
			},
		},
		{
			name:   "qwen sends enable_thinking",
			quirks: ProviderQuirks{ThinkingFormat: "qwen"},
			req:    effortReq,
			want:   map[string]any{"enable_thinking": true, "reasoning_effort": nil},
		},
		{
			name:   "string-thinking sends thinking as effort string",
			quirks: ProviderQuirks{ThinkingFormat: "string-thinking"},
			req:    effortReq,
			want:   map[string]any{"thinking": "high", "reasoning_effort": nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := requestBody(t, tc.req, false, ModelCompat{Quirks: tc.quirks})
			for key, want := range tc.want {
				got, present := body[key]
				if want == nil {
					if present {
						t.Errorf("body[%q] = %v, want absent", key, got)
					}
					continue
				}
				if !present {
					t.Errorf("body[%q] absent, want %v", key, want)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("body[%q] = %#v, want %#v", key, got, want)
				}
			}
		})
	}
}

func TestThinkingLevels_TranslateEffortToWireValue(t *testing.T) {
	req := plainReq("m")
	req.ReasoningEffort = strp("xhigh")
	mc := ModelCompat{
		Quirks:         ProviderQuirks{ThinkingFormat: "zai", SupportsReasoningEffort: boolp(true)},
		ThinkingLevels: map[string]string{"high": "high", "xhigh": "max"},
	}
	body := requestBody(t, req, false, mc)
	if got := body["reasoning_effort"]; got != "max" {
		t.Errorf("reasoning_effort = %v, want max (xhigh mapped)", got)
	}

	// "max" input aliases the xhigh row.
	req.ReasoningEffort = strp("max")
	body = requestBody(t, req, false, mc)
	if got := body["reasoning_effort"]; got != "max" {
		t.Errorf("reasoning_effort for input max = %v, want max", got)
	}

	// The level map beats TranslateMaxToXHigh.
	mc.Quirks.TranslateMaxToXHigh = true
	body = requestBody(t, req, false, mc)
	if got := body["reasoning_effort"]; got != "max" {
		t.Errorf("reasoning_effort with quirk = %v, want map to win with max", got)
	}

	// Without a map, TranslateMaxToXHigh still applies.
	body = requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{TranslateMaxToXHigh: true}})
	if got := body["reasoning_effort"]; got != "xhigh" {
		t.Errorf("reasoning_effort without map = %v, want xhigh", got)
	}

	// A level missing from the map is clamped to the map's nearest declared
	// level before translation, not passed through by name: the adapter
	// itself enforces the map as complete authority for this model, rather
	// than relying on the session-side clamp to keep requests inside it.
	// {"high":"high","xhigh":"max"} has no level at or below "low", so it
	// clamps up to the map's lowest declared level, "high", which then
	// translates to wire "high".
	req.ReasoningEffort = strp("low")
	body = requestBody(t, req, false, mc)
	if got := body["reasoning_effort"]; got != "high" {
		t.Errorf("unmapped level = %v, want clamped-to-high", got)
	}
}

func TestMaxTokensFieldAndDefault(t *testing.T) {
	req := plainReq("m")
	mt := 4096
	req.MaxTokens = &mt
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{MaxTokensField: "max_completion_tokens"}})
	if got := body["max_completion_tokens"]; got != 4096 {
		t.Errorf("max_completion_tokens = %v, want 4096", got)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("max_tokens present alongside max_completion_tokens")
	}

	// DefaultMaxTokens fills in when the request has none.
	body = requestBody(t, plainReq("m"), false, ModelCompat{DefaultMaxTokens: 131072})
	if got := body["max_tokens"]; got != 131072 {
		t.Errorf("default max_tokens = %v, want 131072", got)
	}

	// Request value beats the default.
	body = requestBody(t, req, false, ModelCompat{DefaultMaxTokens: 131072})
	if got := body["max_completion_tokens"]; got != nil {
		t.Errorf("unexpected max_completion_tokens %v", got)
	}
	if got := body["max_tokens"]; got != 4096 {
		t.Errorf("max_tokens = %v, want request's 4096", got)
	}
}

func TestStoreToolStreamAndStreamOptions(t *testing.T) {
	req := plainReq("m")
	req.Tools = []llm.ToolDefinition{{Name: "get_weather"}}

	body := requestBody(t, req, true, ModelCompat{Quirks: ProviderQuirks{SendStoreFalse: true, ToolStream: true}})
	if got, ok := body["store"].(bool); !ok || got {
		t.Errorf("store = %v, want false", body["store"])
	}
	if got, ok := body["tool_stream"].(bool); !ok || !got {
		t.Errorf("tool_stream = %v, want true", body["tool_stream"])
	}
	if _, ok := body["stream_options"]; !ok {
		t.Error("stream_options missing with default usage-in-streaming")
	}

	// tool_stream only when tools present.
	body = requestBody(t, plainReq("m"), true, ModelCompat{Quirks: ProviderQuirks{ToolStream: true}})
	if _, ok := body["tool_stream"]; ok {
		t.Error("tool_stream sent without tools")
	}

	// OmitStreamUsage drops stream_options but keeps stream.
	body = requestBody(t, plainReq("m"), true, ModelCompat{Quirks: ProviderQuirks{OmitStreamUsage: true}})
	if _, ok := body["stream_options"]; ok {
		t.Error("stream_options present with OmitStreamUsage")
	}
	if got, ok := body["stream"].(bool); !ok || !got {
		t.Errorf("stream = %v, want true", body["stream"])
	}
}

func TestDeveloperRole(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		},
	}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{UseDeveloperRole: true}})
	msgs := body["messages"].([]map[string]any)
	if msgs[0]["role"] != "developer" {
		t.Errorf("system message role = %v, want developer", msgs[0]["role"])
	}
	body = requestBody(t, req, false, ModelCompat{})
	msgs = body["messages"].([]map[string]any)
	if msgs[0]["role"] != "system" {
		t.Errorf("system message role = %v, want system", msgs[0]["role"])
	}
}

func toolExchangeMessages() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "weather?"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "get_weather", Arguments: []byte(`{}`)}},
		}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Content: "sunny"}},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "thanks"}}},
	}
}

func TestRequireToolResultName(t *testing.T) {
	req := llm.Request{Model: "m", Messages: toolExchangeMessages()}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{RequireToolResultName: true}})
	msgs := body["messages"].([]map[string]any)
	var toolMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool" {
			toolMsg = m
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message emitted")
	}
	if toolMsg["name"] != "get_weather" {
		t.Errorf("tool message name = %v, want get_weather (recovered from the tool_call)", toolMsg["name"])
	}

	// Without the quirk, no name field.
	body = requestBody(t, req, false, ModelCompat{})
	msgs = body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] == "tool" {
			if _, ok := m["name"]; ok {
				t.Error("tool message has name without quirk")
			}
		}
	}
}

func TestRequireAssistantAfterToolResult(t *testing.T) {
	req := llm.Request{Model: "m", Messages: toolExchangeMessages()}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{RequireAssistantAfterToolResult: true}})
	msgs := body["messages"].([]map[string]any)
	// Expect ... tool, assistant(""), user
	var idxTool, idxUser = -1, -1
	for i, m := range msgs {
		if m["role"] == "tool" {
			idxTool = i
		}
		if m["role"] == "user" && m["content"] == "thanks" {
			idxUser = i
		}
	}
	if idxTool == -1 || idxUser == -1 {
		t.Fatalf("missing tool or trailing user message: %v", msgs)
	}
	if idxUser != idxTool+2 {
		t.Fatalf("want synthetic assistant between tool and user; got messages %v", msgs)
	}
	between := msgs[idxTool+1]
	if between["role"] != "assistant" || between["content"] != "" {
		t.Errorf("between message = %v, want empty assistant", between)
	}
}

func TestThinkingAsText(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "pondering"}},
			{Kind: llm.ContentText, Text: "answer"},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q2"}}},
	}}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{ThinkingAsText: true}})
	msgs := body["messages"].([]map[string]any)
	assistant := msgs[1]
	if _, ok := assistant["reasoning_content"]; ok {
		t.Error("reasoning_content present with ThinkingAsText")
	}
	content, _ := assistant["content"].(string)
	if content != "pondering\n\nanswer" {
		t.Errorf("content = %q, want thinking flattened before text", content)
	}
}

func TestEmptyReasoningContentOnAssistant(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "plain"}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q2"}}},
	}}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{EmptyReasoningContentOnAssistant: true}})
	msgs := body["messages"].([]map[string]any)
	if got, ok := msgs[1]["reasoning_content"]; !ok || got != "" {
		t.Errorf("assistant reasoning_content = %v (present=%t), want empty string", got, ok)
	}
}

func TestAnthropicCacheControl(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "one"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "two"}}},
		},
		Tools: []llm.ToolDefinition{{Name: "a"}, {Name: "b"}},
	}
	body := requestBody(t, req, false, ModelCompat{Quirks: ProviderQuirks{CacheControlFormat: "anthropic"}})
	msgs := body["messages"].([]map[string]any)

	wantCC := map[string]any{"type": "ephemeral"}

	// System prompt content becomes a text-part array with cache_control.
	sysParts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(sysParts) != 1 {
		t.Fatalf("system content = %#v, want single-part array", msgs[0]["content"])
	}
	if !reflect.DeepEqual(sysParts[0]["cache_control"], wantCC) || sysParts[0]["text"] != "sys" {
		t.Errorf("system part = %v, want text sys with cache_control", sysParts[0])
	}

	// Last conversation message gets cache_control.
	lastParts, ok := msgs[2]["content"].([]map[string]any)
	if !ok || len(lastParts) != 1 || !reflect.DeepEqual(lastParts[0]["cache_control"], wantCC) {
		t.Errorf("last message content = %#v, want cache_control on text part", msgs[2]["content"])
	}
	// Earlier message untouched.
	if _, isString := msgs[1]["content"].(string); !isString {
		t.Errorf("middle message content = %#v, want plain string", msgs[1]["content"])
	}

	// Last tool gets cache_control.
	tools := body["tools"].([]map[string]any)
	if _, ok := tools[0]["cache_control"]; ok {
		t.Error("first tool has cache_control, want only last")
	}
	if !reflect.DeepEqual(tools[len(tools)-1]["cache_control"], wantCC) {
		t.Errorf("last tool = %v, want cache_control", tools[len(tools)-1])
	}
}
