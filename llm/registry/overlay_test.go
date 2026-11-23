package registry

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

var specImplicitOrder = []string{
	"anthropic", "openai-codex", "openai", "google", "groq", "zai", "deepseek", "openrouter", "xai", "mistral",
	"cerebras", "togetherai", "moonshotai", "kimi-for-coding", "minimax", "zai-coding-plan",
	"google-vertex-anthropic", "google-vertex", "amazon-bedrock", "azure", "ollama",
}

var pseudoProviders = []string{"openai-compatible", "anthropic-compatible", "google-compatible"}

func loadOverlay(t *testing.T) *Layer {
	t.Helper()
	l, err := ParseOverlay(EmbeddedOverlay())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestEmbeddedOverlayReturnsIndependentCopy(t *testing.T) {
	t.Parallel()
	original := append([]byte(nil), embeddedOverlay...)

	first := EmbeddedOverlay()
	first[0] ^= 0xff
	if got := EmbeddedOverlay(); !bytes.Equal(got, original) {
		t.Fatal("mutating returned overlay bytes changed the embedded source")
	}
}

func TestCuratedOverlay_ImplicitListIsDefaultOrder(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	if !reflect.DeepEqual(l.DefaultOrder, specImplicitOrder) {
		t.Fatalf("default_order = %v", l.DefaultOrder)
	}
	inOrder := map[string]bool{}
	for _, id := range l.DefaultOrder {
		inOrder[id] = true
	}
	for id, p := range l.Providers {
		implicit := p.Implicit != nil && *p.Implicit
		pseudo := false
		for _, ps := range pseudoProviders {
			if ps == id {
				pseudo = true
			}
		}
		if implicit && !inOrder[id] && !pseudo {
			t.Errorf("%s is implicit but not in default_order", id)
		}
		if pseudo && !implicit {
			t.Errorf("pseudo-provider %s must be implicit", id)
		}
	}
}

func TestCuratedOverlay_DefaultsAndTemplates(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	for _, id := range l.DefaultOrder {
		p := l.Providers[id]
		switch id {
		case "azure", "ollama":
			if p.DefaultModel != "" || p.CheapModel != "" {
				t.Errorf("%s must have no default/cheap model", id)
			}
		default:
			if p.DefaultModel == "" || p.CheapModel == "" {
				t.Errorf("%s needs default_model and cheap_model", id)
			}
		}
		// A row with no base_url of its own (e.g. openai-codex)
		// inherits one through base; walk that chain before judging it.
		bu, cur := p.Transport.BaseURL, id
		for bu == "" && l.Providers[cur].Base != "" {
			cur = l.Providers[cur].Base
			bu = l.Providers[cur].Transport.BaseURL
		}
		if !strings.Contains(bu, "{") {
			t.Errorf("%s: base_url must be a template, got %q", id, bu)
		}
	}
	for _, id := range pseudoProviders {
		p := l.Providers[id]
		if p.Transport.BaseURL != "{BASE_URL}" || len(p.Transport.Vars) != 0 || p.DefaultModel != "" {
			t.Errorf("%s: %+v", id, p.Transport)
		}
		want := strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
		if p.Transport.VarsEnv["BASE_URL"] != want+"_BASE_URL" || !reflect.DeepEqual(p.APIKeyEnv, []string{want + "_API_KEY"}) {
			t.Errorf("%s env names: %v %v", id, p.Transport.VarsEnv, p.APIKeyEnv)
		}
	}
}

func TestCuratedOverlay_Transports(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	va := l.Transports[PresetVertexAnthropic]
	if va.Auth != AuthGCPADC || va.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || va.StreamEndpoint != "/publishers/anthropic/models/{model}:streamRawPredict" || va.ModelsEndpoint != EndpointUnsupported || va.CountTokensEndpoint != EndpointUnsupported || va.Body["anthropic_version"] != "vertex-2023-10-16" {
		t.Fatalf("vertex-anthropic: %+v", va)
	}
	vg := l.Transports[PresetVertexGemini]
	if vg.Auth != AuthGCPADC || vg.Endpoint != "" || vg.StreamEndpoint != "" || vg.ModelsEndpoint != "/publishers/google/models" || vg.CountTokensEndpoint != "" {
		t.Fatalf("vertex-gemini: %+v", vg)
	}
	bm := l.Transports[PresetBedrockMantleOpenAI]
	if bm.Auth != AuthBearer || bm.ModelsEndpoint != "/models" || bm.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("bedrock-mantle-openai: %+v", bm)
	}
}

func TestCuratedOverlay_GeminiDefaultsAre38Flash(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	for _, id := range []string{"google", "google-vertex"} {
		p, ok := l.Providers[id]
		if !ok {
			t.Fatalf("%s: no overlay row", id)
		}
		if p.DefaultModel != "gemini-3.8-flash" {
			t.Errorf("%s default_model = %q, want gemini-3.8-flash", id, p.DefaultModel)
		}
	}
}

func TestCuratedOverlay_AnthropicRows(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	a := l.Providers["anthropic"]
	// Every [1m] row aliases its base row; what each carries beyond that
	// differs. Opus 4.5's 1M is still a beta opt-in, so those rows pin the
	// window and the header themselves. Sonnet 4.5's 1M is GA and lives on the
	// base row, so its [1m] rows are pure aliases — a window or a header
	// reappearing on them means the GA fold silently regressed.
	for _, id := range []string{"claude-sonnet-4-5[1m]", "claude-sonnet-4-5-20250929[1m]", "claude-opus-4-5[1m]", "claude-opus-4-5-20251101[1m]"} {
		row, ok := a.Models[id]
		base := strings.TrimSuffix(id, "[1m]")
		if !ok || row.AliasOf != base || row.WireID != base {
			t.Errorf("%s: %+v", id, row)
			continue
		}
		if strings.HasPrefix(id, "claude-sonnet-4-5") {
			if row.Caps.ContextWindow != nil || row.Headers["anthropic-beta"] != "" {
				t.Errorf("%s must be a pure alias, got window %d and anthropic-beta %q",
					id, ip(row.Caps.ContextWindow), row.Headers["anthropic-beta"])
			}
			continue
		}
		if row.Caps.ContextWindow == nil || *row.Caps.ContextWindow != 1000000 || row.Headers["anthropic-beta"] != "context-1m-2025-08-07" {
			t.Errorf("%s: %+v", id, row)
		}
	}
	// Both Sonnet 4.5 spellings are 1M (GA, verified live 2026-08-31). The
	// [1m] aliases above inherit the window from here.
	for _, id := range []string{"claude-sonnet-4-5", "claude-sonnet-4-5-20250929"} {
		if cw := a.Models[id].Caps.ContextWindow; cw == nil || *cw != 1000000 {
			t.Errorf("%s must be pinned to 1000000, got %d", id, ip(a.Models[id].Caps.ContextWindow))
		}
	}
	if a.Models["claude-mythos-5"].AliasOf != "azure/claude-mythos-5" {
		t.Error("claude-mythos-5 must alias the Azure row")
	}
	mp := a.Models["claude-mythos-preview"]
	if mp.Family != "claude-mythos" || mp.Caps.Cost == nil || mp.Caps.Cost.Input != 10 || *mp.Caps.MaxOutputTokens != 128000 || len(mp.Caps.EffortValues) != 5 {
		t.Errorf("claude-mythos-preview: %+v", mp)
	}
	if a.Family != "claude" || a.Surface != SurfaceAnthropic {
		t.Errorf("anthropic surface/family: %q %q", a.Surface, a.Family)
	}
	if _, ok := l.TopGlobs["*claude-opus-4-5*"]; !ok {
		t.Error("missing top-level opus-4-5 glob")
	}
}

func TestCuratedOverlay_CodexProvider(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	c := l.Providers["openai-codex"]
	if c.Base != "openai" || c.InheritModels == nil || *c.InheritModels || c.Transport.Auth != AuthOAuthOpenAICodex {
		t.Fatalf("openai-codex: %+v", c)
	}
	if c.APIKeyEnv == nil || len(c.APIKeyEnv) != 0 {
		t.Fatalf("openai-codex must pin api_key_env = [], got %#v", c.APIKeyEnv)
	}
	if c.Models["gpt-5.6"].WireID != "gpt-5.6-sol" || c.Models["gpt-5.6"].AliasOf != "openai/gpt-5.6" {
		t.Fatalf("gpt-5.6 row: %+v", c.Models["gpt-5.6"])
	}
	for _, f := range []string{"temperature", "top_p", "max_output_tokens", "previous_response_id", "conversation", "service_tier", "safety_identifier", "prompt_cache_retention", "truncation", "max_tool_calls", "background"} {
		if v, ok := c.Caps.Fields[f]; !ok || v {
			t.Errorf("openai-codex fields.%s must be false", f)
		}
	}
	lite := c.Models["gpt-5.6*"]
	if lite.Caps.ResponsesLite == nil || !*lite.Caps.ResponsesLite || lite.Transport == nil || lite.Transport.Body["reasoning.context"] != "all_turns" || lite.Transport.Body["parallel_tool_calls"] != false {
		t.Errorf("gpt-5.6* row: %+v", lite)
	}
	if c.Headers["OpenAI-Organization"] != "" {
		t.Error("codex must blank the inherited org header")
	}
	if _, ok := c.Headers["OpenAI-Organization"]; !ok {
		t.Error("codex must set the org header key to the empty string (removal), not omit it")
	}
}

// Every default_model, cheap_model, and alias_of target names a row that
// exists upstream or in the overlay, so a typo cannot ship.
func TestCuratedOverlay_ReferencesResolve(t *testing.T) {
	t.Parallel()
	l := loadOverlay(t)
	raw, _, err := EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := FromModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]map[string]bool{}
	for _, p := range upstream {
		rows[p.ID] = map[string]bool{}
		for id := range p.Models {
			rows[p.ID][id] = true
		}
	}
	for id, p := range l.Providers {
		if rows[id] == nil {
			rows[id] = map[string]bool{}
		}
		for mid := range p.Models {
			if !isGlob(mid) {
				rows[id][mid] = true
			}
		}
	}
	// openai-codex has inherit_models = false: only its own rows count.
	// A provider with no rows of its own (e.g. google-vertex) inherits them
	// through base, so walk that chain before declaring a miss — but stop at a
	// provider that explicitly opts out of inheriting models (codex), rather
	// than silently falling through to its base's rows.
	has := func(provider, id string) bool {
		for {
			if rows[provider][id] {
				return true
			}
			p := l.Providers[provider]
			if p.InheritModels != nil && !*p.InheritModels {
				return false
			}
			if p.Base == "" {
				return false
			}
			provider = p.Base
		}
	}
	// Direct pin: openai-codex must keep inherit_models = false, and its own
	// default_model/cheap_model must resolve from its own rows alone (not by
	// walking to openai's base) — the generic loop below would still pass if
	// this had silently regressed to a walk, since gpt-5.6/gpt-5.6-luna also
	// exist as openai rows.
	codex := l.Providers["openai-codex"]
	if codex.InheritModels == nil || *codex.InheritModels {
		t.Fatal("openai-codex must have inherit_models = false")
	}
	if !rows["openai-codex"][codex.DefaultModel] {
		t.Errorf("openai-codex: default_model %q is not one of its own rows", codex.DefaultModel)
	}
	if !rows["openai-codex"][codex.CheapModel] {
		t.Errorf("openai-codex: cheap_model %q is not one of its own rows", codex.CheapModel)
	}
	for id, p := range l.Providers {
		for _, ref := range []string{p.DefaultModel, p.CheapModel} {
			if ref != "" && !has(id, ref) {
				t.Errorf("%s: %q is not a row of that provider", id, ref)
			}
		}
		for mid, m := range p.Models {
			if m.AliasOf == "" {
				continue
			}
			target, targetProv := m.AliasOf, id
			if i := strings.Index(m.AliasOf, "/"); i > 0 && rows[m.AliasOf[:i]] != nil {
				targetProv, target = m.AliasOf[:i], m.AliasOf[i+1:]
			}
			if !has(targetProv, target) {
				t.Errorf("%s/%s: alias_of %q does not resolve", id, mid, m.AliasOf)
			}
		}
	}
}

func TestCuratedOverlay_GoogleVertexExpressRetired(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, map[string]string{"GEMINI_API_KEY": "k-gem", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global"}, "")
	if _, ok := r.Provider("google-vertex-express"); ok {
		t.Fatal("google-vertex-express must be retired from curated providers")
	}
	for _, provID := range []string{"google", "google-vertex"} {
		p, ok := r.Provider(provID)
		if !ok {
			t.Fatalf("%s missing", provID)
		}
		if p.CheapModel != "gemini-3.5-flash-lite" {
			t.Fatalf("%s cheap_model = %q, want gemini-3.5-flash-lite", provID, p.CheapModel)
		}
		if BoolValue(p.Caps.WebSearch) {
			t.Fatalf("%s web_search must default to false", provID)
		}
	}

	gRes := mustResolve(t, r, "google/gemini-3.8-flash")
	if gRes.Transport.Endpoint != "/interactions" || gRes.Transport.StreamEndpoint != "/interactions" || gRes.Transport.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("google endpoints: %+v", gRes.Transport)
	}
	if !BoolValue(gRes.Caps.CombinedSearchAndFunctions) || !BoolValue(gRes.Caps.MultimodalToolResults) {
		t.Fatalf("gemini-3.8-flash caps: combined=%v multimodal=%v", gRes.Caps.CombinedSearchAndFunctions, gRes.Caps.MultimodalToolResults)
	}
	if !gRes.Caps.Fields["safety_settings"] || !gRes.Caps.Fields["store"] || !gRes.Caps.Fields["previous_interaction_id"] || !gRes.Caps.Fields["service_tier"] || !gRes.Caps.Fields["labels"] {
		t.Fatalf("google fields: %+v", gRes.Caps.Fields)
	}
	vRes := mustResolve(t, r, "google-vertex/gemini-3.8-flash")
	if vRes.Caps.Fields["safety_settings"] {
		t.Fatalf("google-vertex safety_settings must default false: %+v", vRes.Caps.Fields)
	}
	if BoolValue(vRes.Caps.WebSearch) {
		t.Fatalf("google-vertex/gemini-3.8-flash WebSearch = true, want false")
	}
	claudeRes := mustResolve(t, r, "google-vertex/claude-opus-5")
	if !BoolValue(claudeRes.Caps.WebSearch) {
		t.Fatalf("google-vertex/claude-opus-5 WebSearch = false, want true")
	}

	env := map[string]string{"GEMINI_API_KEY": "k-gem", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global"}
	rOff := fixtureLoad(t, env, "[providers.\"google-vertex\"]\nweb_search = false\n")
	if BoolValue(mustResolve(t, rOff, "google-vertex/claude-opus-5").Caps.WebSearch) {
		t.Fatal("user LayerConfig web_search = false must override google-vertex/claude-opus-5")
	}
	if BoolValue(mustResolve(t, rOff, "google-vertex/gemini-3.8-flash").Caps.WebSearch) {
		t.Fatal("user LayerConfig web_search = false must keep google-vertex/gemini-3.8-flash false")
	}
	rOn := fixtureLoad(t, env, "[providers.\"google-vertex\"]\nweb_search = true\n")
	if !BoolValue(mustResolve(t, rOn, "google-vertex/claude-opus-5").Caps.WebSearch) {
		t.Fatal("user LayerConfig web_search = true must keep google-vertex/claude-opus-5 true")
	}
	if !BoolValue(mustResolve(t, rOn, "google-vertex/gemini-3.8-flash").Caps.WebSearch) {
		t.Fatal("user LayerConfig web_search = true must override google-vertex/gemini-3.8-flash to true")
	}
	imgRes := mustResolve(t, r, "google/gemini-3-pro-image")
	if BoolValue(imgRes.Caps.Tools) || BoolValue(imgRes.Caps.MultimodalToolResults) || BoolValue(imgRes.Caps.CombinedSearchAndFunctions) {
		t.Fatalf("gemini-3-pro-image must have tools/multimodal/combined disabled: %+v", imgRes.Caps)
	}
	neonRes := mustResolve(t, r, "google/gemini-4-neon")
	if !reflect.DeepEqual(neonRes.Caps.EffortValues, []string{"low", "medium", "high", "xhigh"}) || StringValue(neonRes.Caps.DefaultEffort) != "high" || StringValue(neonRes.Caps.ThinkingShape) != "level" {
		t.Fatalf("gemini-4-neon caps: %+v", neonRes.Caps)
	}
	g25 := mustResolve(t, r, "google/gemini-2.5-flash")
	if StringValue(g25.Caps.ThinkingShape) != "level" || !reflect.DeepEqual(g25.Caps.EffortValues, []string{"low", "medium", "high"}) {
		t.Fatalf("google/gemini-2.5-flash caps: %+v", g25.Caps)
	}
}
