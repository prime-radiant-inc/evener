package registry

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var specImplicitOrder = []string{
	"anthropic", "openai-codex", "openai", "google", "groq", "zai", "deepseek", "openrouter", "xai", "mistral",
	"cerebras", "togetherai", "moonshotai", "kimi-for-coding", "minimax", "zai-coding-plan",
	"google-vertex-anthropic", "google-vertex", "google-vertex-express", "amazon-bedrock", "azure", "ollama",
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
	original := append([]byte(nil), embeddedOverlay...)
	defer copy(embeddedOverlay, original)

	first := EmbeddedOverlay()
	first[0] ^= 0xff
	if got := EmbeddedOverlay(); !bytes.Equal(got, original) {
		t.Fatal("mutating returned overlay bytes changed the embedded source")
	}
}

func TestCuratedOverlay_ImplicitListIsDefaultOrder(t *testing.T) {
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
		// A row with no base_url of its own (e.g. google-vertex-express)
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
	l := loadOverlay(t)
	va := l.Transports[PresetVertexAnthropic]
	if va.Auth != AuthGCPADC || va.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || va.StreamEndpoint != "/publishers/anthropic/models/{model}:streamRawPredict" || va.ModelsEndpoint != EndpointUnsupported || va.CountTokensEndpoint != EndpointUnsupported || va.Body["anthropic_version"] != "vertex-2023-10-16" {
		t.Fatalf("vertex-anthropic: %+v", va)
	}
	vg := l.Transports[PresetVertexGemini]
	if vg.Auth != AuthGCPADC || vg.Endpoint != "/publishers/google/models/{model}:generateContent" || vg.StreamEndpoint != "/publishers/google/models/{model}:streamGenerateContent?alt=sse" || vg.ModelsEndpoint != "/publishers/google/models" || vg.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("vertex-gemini: %+v", vg)
	}
	bm := l.Transports[PresetBedrockMantleOpenAI]
	if bm.Auth != AuthBearer || bm.ModelsEndpoint != "/models" || bm.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("bedrock-mantle-openai: %+v", bm)
	}
}

func TestCuratedOverlay_GeminiDefaultsAre38Flash(t *testing.T) {
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
	// A provider with no rows of its own (e.g. google-vertex-express) inherits
	// them through base, so walk that chain before declaring a miss — but stop
	// at a provider that explicitly opts out of inheriting models (codex),
	// rather than silently falling through to its base's rows.
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

func TestCuratedOverlay_GoogleVertexExpress(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_API_KEY": "k-express"}, "")
	p, ok := r.Provider("google-vertex-express")
	if !ok {
		t.Fatal("google-vertex-express: not a curated provider")
	}
	if !BoolValue(p.Implicit) || p.Name != "Google Vertex (API key)" || p.Protocol != ProtocolGoogle || p.Surface != SurfaceGoogle {
		t.Fatalf("head: implicit=%v name=%q protocol=%q surface=%q", BoolValue(p.Implicit), p.Name, p.Protocol, p.Surface)
	}
	tr := p.Transport
	if tr.Auth != AuthHeader || tr.AuthHeader != "x-goog-api-key" {
		t.Fatalf("auth: %+v", tr)
	}
	if tr.Endpoint != "/publishers/google/models/{model}:generateContent" || tr.StreamEndpoint != "/publishers/google/models/{model}:streamGenerateContent?alt=sse" {
		t.Fatalf("endpoints: %+v", tr)
	}
	if tr.ModelsEndpoint != EndpointUnsupported || tr.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("listing/count must be unsupported on the express row: %+v", tr)
	}
	if tr.Vars["BASE_URL"] != "https://aiplatform.googleapis.com/v1" || tr.VarsEnv["BASE_URL"] != "GOOGLE_VERTEX_EXPRESS_BASE_URL" {
		t.Fatalf("vars: %v %v", tr.Vars, tr.VarsEnv)
	}
	if !reflect.DeepEqual(p.APIKeyEnv, []string{"GOOGLE_VERTEX_API_KEY"}) {
		t.Fatalf("api_key_env = %v", p.APIKeyEnv)
	}
	if p.DefaultModel != "gemini-3.8-flash" || p.CheapModel != "gemini-2.5-flash-lite" {
		t.Fatalf("defaults: %q %q", p.DefaultModel, p.CheapModel)
	}
	for id := range p.Models {
		if strings.HasPrefix(id, "claude") {
			t.Fatalf("express row inherited a Claude row %q; it must be Gemini-only", id)
		}
	}
	if _, ok := p.Models["gemini-2.5-flash"]; !ok {
		t.Fatal("express row did not inherit google's gemini-2.5-flash")
	}

	res, err := r.Resolve("google-vertex-express/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Transport.BaseURL != "https://aiplatform.googleapis.com/v1" || res.Credential.Value != "k-express" || res.Credential.Source != "env:GOOGLE_VERTEX_API_KEY" || res.Synthesized {
		t.Fatalf("resolved: base=%q cred=%+v synthesized=%v", res.Transport.BaseURL, res.Credential, res.Synthesized)
	}
	if !BoolValue(res.Caps.WebSearch) {
		t.Fatalf("web_search must survive on the express row: %v", res.Warnings)
	}
}

func TestGoogleVertexExpressExistsOnlyWithItsOwnKey(t *testing.T) {
	if names := instanceNames(fixtureLoad(t, map[string]string{"GEMINI_API_KEY": "g"}, "")); slices.Contains(names, "google-vertex-express") {
		t.Fatalf("GEMINI_API_KEY alone created google-vertex-express: %v", names)
	}
	if names := instanceNames(fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_API_KEY": "k"}, "")); !slices.Contains(names, "google-vertex-express") {
		t.Fatalf("GOOGLE_VERTEX_API_KEY did not create google-vertex-express: %v", names)
	}
	if names := instanceNames(fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_API_KEY": "k"}, "")); slices.Contains(names, "google") {
		t.Fatalf("GOOGLE_VERTEX_API_KEY must not create the google instance: %v", names)
	}
}
