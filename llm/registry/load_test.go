package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fixtureLoad loads the 40-provider fixture with the real curated overlay,
// no user layer unless config is non-empty, and the given environment.
func fixtureLoad(t *testing.T, env map[string]string, config string, extra ...Option) *Registry {
	t.Helper()
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	opts := []Option{WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir())}
	if config == "" {
		opts = append(opts, WithNoUserLayer())
	} else {
		path := filepath.Join(t.TempDir(), "providers.toml")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		opts = append(opts, WithConfigPath(path))
	}
	opts = append(opts, extra...)
	r, err := Load(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func embeddedLoad(t *testing.T, extra ...Option) *Registry {
	t.Helper()
	opts := []Option{
		WithStateRoot(t.TempDir()),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithOffline(true),
		WithoutCache(),
	}
	opts = append(opts, extra...)
	r, err := Load(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mapEnv(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := env[name]; return v, ok }
}

// overlayWith appends extra TOML to the embedded curated overlay: a custom
// overlay must keep the transport presets the converter's rows reference.
func overlayWith(extra string) []byte {
	return []byte(string(EmbeddedOverlay()) + "\n" + extra)
}

func layerTags(rec *record) []string {
	out := make([]string, 0, len(rec.layers))
	for _, l := range rec.layers {
		out = append(out, l.tag+":"+l.owner)
	}
	return out
}

func TestLoad_EmbeddedSourcesDoNotShareLiveState(t *testing.T) {
	first, second := embeddedLoad(t), embeddedLoad(t)
	first.ApplyLive("ollama", []Model{{ID: "only-on-first"}})
	if got := second.LiveModels("ollama"); len(got) != 0 {
		t.Fatalf("second registry inherited live models: %+v", got)
	}
}

func TestLoad_EmbeddedSourcesDoNotShareInjectedInstances(t *testing.T) {
	first := embeddedLoad(t, WithInstances(map[string]Provider{
		"only-first": {Base: "openai"},
	}))
	second := embeddedLoad(t, WithInstances(map[string]Provider{
		"only-second": {Base: "openai"},
	}))

	if _, err := first.ResolveInstance("only-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ResolveInstance("only-first"); err == nil {
		t.Fatal("second registry inherited first registry's injected instance")
	}
	if _, err := second.ResolveInstance("only-second"); err != nil {
		t.Fatal(err)
	}
}

func TestProvider_ReturnsIndependentValue(t *testing.T) {
	registry := embeddedLoad(t)
	provider, ok := registry.Provider("openai")
	if !ok {
		t.Fatal("openai provider is missing")
	}
	before, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	for id := range provider.Models {
		delete(provider.Models, id)
		break
	}
	if provider.Implicit != nil {
		*provider.Implicit = !*provider.Implicit
	}

	assertUnchanged := func(label string, got Provider) {
		t.Helper()
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, before) {
			t.Fatalf("%s provider changed after mutating a returned value", label)
		}
	}
	sameRegistry, _ := registry.Provider("openai")
	assertUnchanged("same registry", sameRegistry)
	newRegistry, _ := embeddedLoad(t).Provider("openai")
	assertUnchanged("new registry", newRegistry)
}

func TestProvider_ClonesNestedReferenceValues(t *testing.T) {
	provider := Provider{
		ID:                    "example",
		InheritModels:         new(true),
		InheritModelsMatching: []string{"alpha-*"},
		Implicit:              new(true),
		APIKeyEnv:             []string{"EXAMPLE_API_KEY"},
		Headers:               map[string]string{"provider": "header"},
		Transport: Transport{
			Vars: map[string]string{"region": "test"},
			Body: map[string]any{
				"nested": map[string]any{"enabled": true},
				"tables": []map[string]any{{"name": "first"}},
			},
		},
		Caps: Caps{
			ContextWindow:     new(1000),
			ReasoningControls: []string{"effort"},
			Fields:            map[string]bool{"reasoning.effort": true},
			Cost:              &Cost{Tiers: []CostTier{{InputTokensAbove: 100}}},
			ChatTemplateKwargs: map[string]any{
				"options": map[string]any{"mode": "fast"},
			},
		},
		Models: map[string]Model{
			"model": {
				Headers: map[string]string{"model": "header"},
				Caps:    Caps{Tools: new(true)},
				Transport: &Transport{
					Body: map[string]any{"model": map[string]any{"enabled": true}},
				},
			},
		},
	}
	registry := &Registry{curated: map[string]*record{"example": {head: provider}}}
	want, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := registry.Provider("example")
	if !ok {
		t.Fatal("example provider is missing")
	}
	*got.InheritModels = false
	got.InheritModelsMatching[0] = "changed"
	got.APIKeyEnv[0] = "CHANGED"
	got.Headers["provider"] = "changed"
	got.Transport.Vars["region"] = "changed"
	got.Transport.Body["nested"].(map[string]any)["enabled"] = false
	got.Transport.Body["tables"].([]map[string]any)[0]["name"] = "changed"
	*got.Caps.ContextWindow = 2000
	got.Caps.ReasoningControls[0] = "changed"
	got.Caps.Fields["reasoning.effort"] = false
	got.Caps.Cost.Tiers[0].InputTokensAbove = 200
	got.Caps.ChatTemplateKwargs["options"].(map[string]any)["mode"] = "slow"
	model := got.Models["model"]
	model.Headers["model"] = "changed"
	*model.Caps.Tools = false
	model.Transport.Body["model"].(map[string]any)["enabled"] = false
	got.Models["model"] = model

	unchanged, _ := registry.Provider("example")
	data, err := json.Marshal(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("provider changed after mutating nested values in a returned view")
	}
}

func TestLoad_EmbeddedOverridesBypassParsedDefaults(t *testing.T) {
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("snapshot", func(t *testing.T) {
		defaults := embeddedLoad(t)
		custom := embeddedLoad(t, WithSnapshot(data))
		if _, ok := defaults.Provider("302ai"); !ok {
			t.Fatal("default embedded catalog has no 302ai provider")
		}
		if _, ok := custom.Provider("302ai"); ok {
			t.Fatal("custom snapshot unexpectedly used the embedded catalog")
		}
	})

	t.Run("overlay", func(t *testing.T) {
		defaults := embeddedLoad(t)
		custom := embeddedLoad(t, WithOverlay(overlayWith(
			"[providers.plan-review-only]\nbase = \"openai\"\n",
		)))
		if _, ok := defaults.Provider("plan-review-only"); ok {
			t.Fatal("default overlay contains test provider")
		}
		if _, ok := custom.Provider("plan-review-only"); !ok {
			t.Fatal("custom overlay provider is missing")
		}
	})
}

func TestLoad_EmbeddedSourcesSupportConcurrentLoads(t *testing.T) {
	before := embeddedParsedSources(t)
	const workers = 8
	stateRoot := t.TempDir()
	errs := make(chan error, workers)
	ready := make(chan struct{}, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for worker := range workers {
		wg.Go(func() {
			ready <- struct{}{}
			<-start
			r, err := Load(
				WithStateRoot(stateRoot),
				WithEnv(mapEnv(nil)),
				WithNoUserLayer(),
				WithOffline(true),
				WithoutCache(),
			)
			if err != nil {
				errs <- err
				return
			}
			resolved, err := r.Resolve("openai/gpt-5.6")
			if err != nil {
				errs <- err
				return
			}
			if resolved.ProviderID != "openai" || resolved.Protocol != ProtocolOpenAIResponses {
				errs <- fmt.Errorf("worker %d resolved %+v", worker, resolved)
			}
		})
	}
	for range workers {
		<-ready
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if after := embeddedParsedSources(t); !bytes.Equal(after, before) {
		t.Fatal("loading and resolving mutated the cached embedded sources")
	}
}

func embeddedParsedSources(t *testing.T) []byte {
	t.Helper()
	catalog, err := loadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := loadEmbeddedOverlay()
	if err != nil {
		t.Fatal(err)
	}
	notes := make([][]string, len(catalog.providers))
	for i, provider := range catalog.providers {
		notes[i] = slices.Clone(provider.notes)
	}
	data, err := json.Marshal(struct {
		Providers []Provider `json:"providers"`
		Notes     [][]string `json:"provider_notes"`
		Meta      Meta       `json:"meta"`
		Overlay   *Layer     `json:"overlay"`
	}{catalog.providers, notes, catalog.meta, overlay})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func BenchmarkLoadEmbeddedDefaults(b *testing.B) {
	stateRoot := b.TempDir()
	for b.Loop() {
		if _, err := Load(
			WithStateRoot(stateRoot),
			WithEnv(mapEnv(nil)),
			WithNoUserLayer(),
			WithOffline(true),
		); err != nil {
			b.Fatal(err)
		}
	}
}

var providerBenchmarkSink Provider

func BenchmarkProviderCatalogViews(b *testing.B) {
	stateRoot := b.TempDir()
	registry, err := Load(
		WithStateRoot(stateRoot),
		WithEnv(mapEnv(nil)),
		WithNoUserLayer(),
		WithOffline(true),
		WithoutCache(),
	)
	if err != nil {
		b.Fatal(err)
	}
	ids := registry.ProviderIDs()

	b.Run("owned", func(b *testing.B) {
		for b.Loop() {
			for _, id := range ids {
				providerBenchmarkSink, _ = registry.Provider(id)
			}
		}
		b.ReportMetric(float64(len(ids)), "providers/op")
	})
	b.Run("shallow-reference", func(b *testing.B) {
		for b.Loop() {
			for _, id := range ids {
				providerBenchmarkSink = registry.curated[id].head
			}
		}
		b.ReportMetric(float64(len(ids)), "providers/op")
	})
}

func TestLoad_CuratedBaseChainAndInheritModelsFalse(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	codex := r.curated["openai-codex"]
	if codex == nil {
		t.Fatal("openai-codex missing")
	}
	if got := layerTags(codex); !reflect.DeepEqual(got, []string{"snapshot:openai", "overlay:openai", "overlay:openai-codex"}) {
		t.Fatalf("codex layers = %v", got)
	}
	h := codex.head
	if h.Protocol != ProtocolOpenAIResponses || h.Transport.Auth != AuthOAuthOpenAICodex || h.Transport.BaseURL != "{BASE_URL}" || h.Transport.Vars["BASE_URL"] != "https://chatgpt.com/backend-api/codex" || h.Transport.ModelsEndpoint != "/models?client_version=0.0.0" {
		t.Fatalf("codex head: %+v", h.Transport)
	}
	if h.Transport.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("codex must override the inherited count-tokens endpoint, got %q", h.Transport.CountTokensEndpoint)
	}
	if _, ok := h.Models["gpt-5.5"]; ok {
		t.Fatal("inherit_models = false must drop the base's rows")
	}
	if _, ok := h.Models["gpt-5.6"]; !ok || h.Models["gpt-5.6"].WireID != "gpt-5.6-sol" {
		t.Fatalf("codex rows: %v", h.Models["gpt-5.6"])
	}
	for _, l := range codex.layers[:2] {
		if l.rows != nil {
			t.Fatal("base layers must carry no rows after inherit_models = false")
		}
	}
	if codex.providerID != "openai-codex" || codex.head.APIKeyEnv == nil || len(codex.head.APIKeyEnv) != 0 {
		t.Fatalf("providerID=%q apiKeyEnv=%#v", codex.providerID, codex.head.APIKeyEnv)
	}
	openai := r.curated["openai"]
	if openai.head.Transport.CountTokensEndpoint != "/responses/input_tokens" || openai.head.Headers["OpenAI-Organization"] != "$OPENAI_ORG_ID" {
		t.Fatalf("openai head: %+v", openai.head)
	}
}

// A provider can have both an upstream (models.dev) entry and an overlay entry
// naming a base with inherit_models_matching; the glob narrows the rows it
// inherits from the base, never the rows the provider brings itself.
func TestLoad_InheritModelsMatchingKeepsTheProviderOwnUpstreamRows(t *testing.T) {
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["matchup"] = json.RawMessage(`{"id":"matchup","name":"Match Upstream","models":{"beta-9":{"id":"beta-9","name":"Beta 9","modalities":{"input":["text"],"output":["text"]},"limit":{"context":8000,"output":1000}}}}`)
	data, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	overlay := overlayWith(`
[providers.matchbase]
implicit = true
name = "Match Base"
protocol = "openai-chat"
surface = "generic"
base_url = "https://example.test/v1"
auth = "none"

[providers.matchbase.models."alpha-1"]
[providers.matchbase.models."beta-1"]

[providers.matchup]
implicit = true
base = "matchbase"
inherit_models_matching = ["alpha-*"]
`)
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()), WithOverlay(overlay))
	if err != nil {
		t.Fatal(err)
	}
	up := r.curated["matchup"]
	if up == nil {
		t.Fatal("matchup missing")
	}
	if got, want := sortedKeys(up.head.Models), []string{"alpha-1", "beta-9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matchup model ids = %v, want %v (own upstream row kept, base's beta-1 dropped)", got, want)
	}
	if res, err := r.Resolve("matchup/beta-9"); err != nil || res.Synthesized {
		t.Fatalf("matchup's own upstream row beta-9 must survive the inherited-row glob: res=%+v err=%v", res, err)
	}
}

// The user layer takes the same key: an instance built on a curated base
// narrows the rows it inherits and nothing else.
func TestLoad_InheritModelsMatchingNarrowsAUserInstance(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "k"}, `
[providers.mine]
base = "openai"
inherit_models_matching = ["gpt-5*"]
`)
	kept, err := r.Resolve("mine/gpt-5-nano")
	if err != nil || kept.Synthesized {
		t.Fatalf("mine/gpt-5-nano must resolve as an inherited catalog row: res=%+v err=%v", kept, err)
	}
	dropped, err := r.Resolve("mine/gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped.Synthesized {
		t.Fatalf("mine/gpt-4o must not resolve as a catalog row once the glob dropped it: %+v", dropped)
	}
	base, err := r.Resolve("openai/gpt-4o")
	if err != nil || base.Synthesized {
		t.Fatalf("openai's own gpt-4o must be untouched by the instance's glob: res=%+v err=%v", base, err)
	}
}

func TestLoad_InheritModelsMatchingKeepsOnlyMatchingBaseRows(t *testing.T) {
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	overlay := overlayWith(`
[providers.matchbase]
implicit = true
name = "Match Base"
protocol = "openai-chat"
surface = "generic"
base_url = "https://example.test/v1"
auth = "none"

[providers.matchbase.models."alpha-1"]
[providers.matchbase.models."alpha-2"]
[providers.matchbase.models."beta-1"]

[providers.matchderived]
implicit = true
name = "Match Derived"
base = "matchbase"
inherit_models_matching = ["alpha-*"]

[providers.matchderived.models."gamma-9"]
`)
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()), WithOverlay(overlay))
	if err != nil {
		t.Fatal(err)
	}
	derived := r.curated["matchderived"]
	if derived == nil {
		t.Fatal("matchderived missing")
	}
	if got, want := sortedKeys(derived.head.Models), []string{"alpha-1", "alpha-2", "gamma-9"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("derived model ids = %v, want %v", got, want)
	}
	base := r.curated["matchbase"]
	if _, ok := base.head.Models["beta-1"]; !ok {
		t.Fatal("inherit_models_matching must not remove beta-1 from the base's own record")
	}
	if res, err := r.Resolve("matchbase/beta-1"); err != nil || res.Synthesized {
		t.Fatalf("matchbase/beta-1 must still resolve as a real catalog row (no shared-map mutation): res=%+v err=%v", res, err)
	}
	p, ok := r.Provider("matchderived")
	if !ok {
		t.Fatal("matchderived provider missing")
	}
	if !reflect.DeepEqual(p.InheritModelsMatching, []string{"alpha-*"}) {
		t.Fatalf("InheritModelsMatching = %v", p.InheritModelsMatching)
	}
	// beta-1 fell out of matchderived's catalog: an unresolvable id degrades
	// to a synthesized pass-through row rather than an error (Resolve only
	// hard-fails unknown ids on the Codex transport), so Synthesized is what
	// proves the row is gone.
	res, err := r.Resolve("matchderived/beta-1")
	if err != nil {
		t.Fatalf("matchderived/beta-1: %v", err)
	}
	if !res.Synthesized {
		t.Fatalf("matchderived/beta-1 must not resolve as a real catalog row: %+v", res)
	}
}

func TestLoad_InheritModelsMatchingValidation(t *testing.T) {
	cases := map[string]string{
		"no base": "[providers.x]\n" +
			"inherit_models_matching = [\"alpha-*\"]\n",
		"conflicts with inherit_models = false": "[providers.x]\n" +
			"base = \"openai\"\n" +
			"inherit_models = false\n" +
			"inherit_models_matching = [\"alpha-*\"]\n",
		"empty pattern": "[providers.x]\n" +
			"base = \"openai\"\n" +
			"inherit_models_matching = [\"\"]\n",
	}
	wantSubstr := map[string]string{
		"no base":                               "inherit_models_matching needs base",
		"conflicts with inherit_models = false": "conflicts with inherit_models = false",
		"empty pattern":                         "empty pattern",
	}
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()),
				WithOverlay(overlayWith(cfg)))
			if err == nil || !strings.Contains(err.Error(), wantSubstr[name]) {
				t.Fatalf("%s: err = %v, want it to contain %q", name, err, wantSubstr[name])
			}
		})
	}
}

func TestLoad_ExplicitInstances(t *testing.T) {
	cfg := `
[providers.groq]
protocol = "openai-responses"
[providers.work]
base = "openai"
protocol = "openai-chat"
base_url = "https://gw.example.com/v1"
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.openai]
base = "openai-codex"
[providers.mycodex]
base = "openai-codex"
`
	r := fixtureLoad(t, nil, cfg)
	groq := r.explicit["groq"]
	if groq.providerID != "groq" || groq.head.Protocol != ProtocolOpenAIResponses || !reflect.DeepEqual(layerTags(groq), []string{"snapshot:groq", "overlay:groq", "config:groq"}) {
		t.Fatalf("groq: %q %q %v", groq.providerID, groq.head.Protocol, layerTags(groq))
	}
	work := r.explicit["work"]
	if work.providerID != "openai" || work.head.Protocol != ProtocolOpenAIChat || work.head.Transport.Auth != AuthBearer {
		t.Fatalf("work: %+v", work.head)
	}
	if work.head.Transport.CountTokensEndpoint != "" || work.head.Transport.BaseURL != "https://gw.example.com/v1" {
		t.Fatalf("cross-protocol instance must drop the base's endpoint fields: %+v", work.head.Transport)
	}
	if !work.layers[len(work.layers)-1].resetFields {
		t.Fatal("cross-protocol layer must reset inherited Fields")
	}
	if r.explicit["openai"].providerID != "openai-codex" || r.explicit["openai"].head.Transport.Auth != AuthOAuthOpenAICodex {
		t.Fatal("an explicit base must beat the name match")
	}
	if r.explicit["mycodex"].providerID != "openai-codex" {
		t.Fatalf("mycodex providerID = %q", r.explicit["mycodex"].providerID)
	}
	if _, ok := r.explicit["work"].userVars["BASE_URL"]; ok {
		t.Fatal("no user vars were set")
	}
}

func TestLoad_PresetExpansion(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	va := r.curated["google-vertex-anthropic"].head.Transport
	if va.Preset != PresetVertexAnthropic || va.Auth != AuthGCPADC || va.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || va.Body["anthropic_version"] != "vertex-2023-10-16" || va.HostRule != HostRuleVertexLocation {
		t.Fatalf("vertex-anthropic transport: %+v", va)
	}
	row := r.curated["google-vertex"].head.Models["claude-opus-5"]
	if row.Transport == nil || row.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || row.Protocol != ProtocolAnthropic {
		t.Fatalf("vertex claude row must expand the converter's preset: %+v", row)
	}
	mantle := r.curated["amazon-bedrock"].head.Models["openai.gpt-oss-120b"]
	if mantle.Transport == nil || mantle.Transport.Auth != AuthBearer || mantle.Transport.BaseURL != "https://bedrock-mantle.{AWS_REGION}.api.aws/v1" || mantle.Transport.ModelsEndpoint != "/models" {
		t.Fatalf("mantle row: %+v", mantle.Transport)
	}
}

func TestLoad_Errors(t *testing.T) {
	cases := map[string]string{
		"unknown base":              "[providers.x]\nbase = \"nope\"\n",
		"unknown preset":            "[providers.x]\nbase = \"openai\"\ntransport = \"nope\"\n",
		"no protocol":               "[providers.x]\nbase_url = \"https://x/v1\"\n",
		"no base url":               "[providers.x]\nprotocol = \"openai-chat\"\n",
		"dangling alias":            "[providers.anthropic.models.\"mine\"]\nalias_of = \"claude-nope\"\n",
		"alias of alias":            "[providers.anthropic.models.\"mine\"]\nalias_of = \"claude-sonnet-4-5[1m]\"\n",
		"cross-provider unknown":    "[providers.anthropic.models.\"mine\"]\nalias_of = \"nope/claude-opus-5\"\n",
		"cross-provider glob alias": "[providers.anthropic.models.\"mine\"]\nalias_of = \"openai/gpt-5*\"\n",
		"fields key wrong protocol": "[providers.groq.fields]\ninclude = true\n",
		"row fields wrong protocol": "[providers.groq.models.\"llama-3.3-70b-versatile\"]\nfields = { include = true }\n",
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.toml")
			if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile("testdata/models.dev.sample.json")
			_, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithConfigPath(path), WithStateRoot(t.TempDir()))
			if err == nil {
				t.Fatalf("expected error for %s", name)
			}
			if strings.Contains(name, "fields") && !strings.Contains(err.Error(), "include") {
				t.Fatalf("fields error must name the key: %v", err)
			}
		})
	}
	// Inherited fields from a base on another protocol are ignored, not
	// errors — even when the instance is named after a registry id in its own
	// base chain (openai-codex inherits openai's Responses-only fields).
	fixtureLoad(t, nil, "[providers.work]\nbase = \"openai\"\nprotocol = \"openai-chat\"\nbase_url = \"https://gw/v1\"\n")
	fixtureLoad(t, nil, "[providers.openai]\nbase = \"openai-codex\"\nprotocol = \"anthropic\"\nbase_url = \"https://gw/v1\"\n")
}

func TestLoad_OverlayBaseCycleAndCuratedDanglingAlias(t *testing.T) {
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	_, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.a]\nbase = \"b\"\n[providers.b]\nbase = \"a\"\n")))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want base cycle error, got %v", err)
	}
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.anthropic.models.\"gone\"]\nalias_of = \"claude-nope\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	row := r.curated["anthropic"].head.Models["gone"]
	if !row.Hidden {
		t.Fatal("a curated dangling alias must degrade to a hidden row")
	}
	if !strings.Contains(strings.Join(r.Warnings(), "\n"), "dangling alias") {
		t.Fatalf("warnings = %v", r.Warnings())
	}
	// An instance that inherits the curated dangling alias must still load.
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte("[providers.myclaude]\nbase = \"anthropic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithConfigPath(path), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.anthropic.models.\"gone\"]\nalias_of = \"claude-nope\"\n")))
	if err != nil {
		t.Fatalf("an inherited curated dangling alias must not fail the load: %v", err)
	}
	if !r.explicit["myclaude"].head.Models["gone"].Hidden {
		t.Fatal("the inherited dangling row must be hidden on the instance too")
	}
}

func TestLoad_HiddenAgainstEnvironment(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	for id, want := range map[string]bool{
		"azure": true, "amazon-bedrock": true, "google-vertex": true, "google-vertex-anthropic": true,
		"cohere": true, "deepinfra": true, "openai-compatible": true,
		"ollama": false, "openai": false, "anthropic": false, "groq": false,
	} {
		p, ok := r.Provider(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if p.Hidden != want {
			t.Errorf("%s Hidden = %v, want %v", id, p.Hidden, want)
		}
	}
	r = fixtureLoad(t, map[string]string{"AZURE_RESOURCE_NAME": "contoso", "AWS_REGION": "us-east-1", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global", "OPENAI_COMPATIBLE_BASE_URL": "http://localhost:8080/v1"}, "")
	for _, id := range []string{"azure", "amazon-bedrock", "google-vertex", "google-vertex-anthropic", "openai-compatible"} {
		if p, _ := r.Provider(id); p.Hidden {
			t.Errorf("%s must be visible with its variables set", id)
		}
	}
	url, missing, _ := r.resolveBaseURL(r.curated["amazon-bedrock"], r.curated["amazon-bedrock"].head.Transport)
	if url != "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1" || len(missing) != 0 {
		t.Fatalf("bedrock url = %q missing = %v", url, missing)
	}
	url, _, _ = r.resolveBaseURL(r.curated["google-vertex-anthropic"], r.curated["google-vertex-anthropic"].head.Transport)
	if url != "https://aiplatform.googleapis.com/v1/projects/p/locations/global" {
		t.Fatalf("vertex url = %q", url)
	}
}

// TestUnresolvedBaseURL covers what a caller shows for a curated provider whose
// base URL does not resolve here. GOOGLE_VERTEX_HOST is derived from the
// location by the vertex-location host rule, so only the location is named,
// and the credential's variable is none of the URL's business (roborev round
// 6, F2). A location that is set but unusable is a problem to report, not a
// variable to set (round 8, F2).
func TestUnresolvedBaseURL(t *testing.T) {
	for _, tt := range []struct {
		name        string
		env         map[string]string
		id          string
		wantUnset   []string
		wantProblem string
	}{
		{name: "no variables set", env: map[string]string{}, id: "google-vertex", wantUnset: []string{"GOOGLE_VERTEX_LOCATION", "GOOGLE_VERTEX_PROJECT"}},
		{name: "location set", env: map[string]string{"GOOGLE_VERTEX_LOCATION": "global"}, id: "google-vertex", wantUnset: []string{"GOOGLE_VERTEX_PROJECT"}},
		{name: "both set", env: map[string]string{"GOOGLE_VERTEX_LOCATION": "global", "GOOGLE_VERTEX_PROJECT": "p"}, id: "google-vertex"},
		{name: "curated default", env: map[string]string{}, id: "openai"},
		{name: "unknown id", env: map[string]string{}, id: "no-such-provider"},
		{name: "invalid location", env: map[string]string{"GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "bad.host"}, id: "google-vertex", wantProblem: `invalid GOOGLE_VERTEX_LOCATION "bad.host"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := fixtureLoad(t, tt.env, "")
			unset, problems := r.UnresolvedBaseURL(tt.id)
			if !slices.Equal(unset, tt.wantUnset) {
				t.Fatalf("UnresolvedBaseURL(%q) unset = %v, want %v", tt.id, unset, tt.wantUnset)
			}
			if tt.wantProblem == "" {
				if len(problems) != 0 {
					t.Fatalf("UnresolvedBaseURL(%q) problems = %v, want none", tt.id, problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tt.wantProblem) {
				t.Fatalf("UnresolvedBaseURL(%q) problems = %v, want one naming %s", tt.id, problems, tt.wantProblem)
			}
		})
	}
}

// TestTemplateVarsEnv covers which vars_env entries an add form should offer:
// the ones a URL template reads, wherever that template lives (a row's own
// base URL counts), plus a host rule's inputs. The models.dev env list also
// names the credential's own variable (GOOGLE_APPLICATION_CREDENTIALS), which
// no template reads and the registry never substitutes (roborev round 19).
func TestTemplateVarsEnv(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	r.curated["row-only"] = &record{head: Provider{
		Transport: Transport{
			BaseURL: "https://example.test/v1",
			VarsEnv: map[string]string{"REGION": "EXAMPLE_REGION", "UNREAD": "EXAMPLE_UNREAD"},
		},
		Models: map[string]Model{"m": {Transport: &Transport{BaseURL: "https://{REGION}.example.test/v1"}}},
	}}
	r.curated["rule-only"] = &record{head: Provider{Transport: Transport{
		BaseURL:  "{GOOGLE_VERTEX_HOST}/v1",
		HostRule: HostRuleVertexLocation,
		VarsEnv: map[string]string{
			"GOOGLE_VERTEX_LOCATION":         "GOOGLE_VERTEX_LOCATION",
			"GOOGLE_APPLICATION_CREDENTIALS": "GOOGLE_APPLICATION_CREDENTIALS",
		},
	}}}
	for _, tt := range []struct {
		id   string
		want map[string]string
	}{
		{id: "google-vertex", want: map[string]string{"GOOGLE_VERTEX_PROJECT": "GOOGLE_VERTEX_PROJECT", "GOOGLE_VERTEX_LOCATION": "GOOGLE_VERTEX_LOCATION"}},
		{id: "openai", want: map[string]string{"BASE_URL": "OPENAI_BASE_URL"}},
		{id: "row-only", want: map[string]string{"REGION": "EXAMPLE_REGION"}},
		{id: "rule-only", want: map[string]string{"GOOGLE_VERTEX_LOCATION": "GOOGLE_VERTEX_LOCATION"}},
		{id: "no-such-provider"},
	} {
		t.Run(tt.id, func(t *testing.T) {
			if got := r.TemplateVarsEnv(tt.id); !maps.Equal(got, tt.want) {
				t.Fatalf("TemplateVarsEnv(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestLoad_UserVarsBeatEnvBeatCurated(t *testing.T) {
	cfg := "[providers.bedrock]\nbase = \"amazon-bedrock\"\n[providers.bedrock.vars]\n\"AWS_REGION\" = \"eu-west-1\"\n[providers.mine]\nbase = \"openai\"\n"
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "OPENAI_BASE_URL": "https://proxy.example/v1"}, cfg)
	url, _, _ := r.resolveBaseURL(r.explicit["bedrock"], r.explicit["bedrock"].head.Transport)
	if url != "https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1" {
		t.Fatalf("user vars must win: %q", url)
	}
	url, _, _ = r.resolveBaseURL(r.explicit["mine"], r.explicit["mine"].head.Transport)
	if url != "https://proxy.example/v1" {
		t.Fatalf("env must beat the curated default: %q", url)
	}
	url, _, _ = r.resolveBaseURL(r.curated["anthropic"], r.curated["anthropic"].head.Transport)
	if url != "https://api.anthropic.com/v1" {
		t.Fatalf("curated default: %q", url)
	}
}

func TestLoad_UnresolvedUserVarWarnsAndStops(t *testing.T) {
	cfg := "[providers.bedrock]\nbase = \"amazon-bedrock\"\n[providers.bedrock.vars]\n\"AWS_REGION\" = \"$AWS_REGION_PROD\"\n"
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "AWS_BEARER_TOKEN_BEDROCK": "bt"}, cfg)
	rec := r.explicit["bedrock"]
	url, missing, warns := r.resolveBaseURL(rec, rec.head.Transport)
	if !strings.Contains(url, "{AWS_REGION}") || !slices.Contains(missing, "AWS_REGION") {
		t.Fatalf("an unset user var must not fall through to the environment: %q %v", url, missing)
	}
	if !slices.Contains(warns, "unresolved variable AWS_REGION_PROD") {
		t.Fatalf("resolveBaseURL warnings = %v", warns)
	}
	res, err := r.Resolve("bedrock/anthropic.claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Warnings, "unresolved variable AWS_REGION_PROD") {
		t.Fatalf("Resolve warnings = %v", res.Warnings)
	}
}

func TestLoad_OllamaHostRule(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OLLAMA_HOST": "::1"}, "")
	url, _, _ := r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport)
	if url != "http://[::1]:11434/v1" {
		t.Fatalf("OLLAMA_HOST: %q", url)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_HOST": "::1", "OLLAMA_BASE_URL": "http://proxy/ollama/v1"}, "")
	url, _, _ = r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport)
	if url != "http://proxy/ollama/v1" {
		t.Fatalf("OLLAMA_BASE_URL must win: %q", url)
	}
	r = fixtureLoad(t, nil, "[providers.ollama]\nbase_url = \"http://gpu:11434/v1\"\n")
	url, _, _ = r.resolveBaseURL(r.explicit["ollama"], r.explicit["ollama"].head.Transport)
	if url != "http://gpu:11434/v1" {
		t.Fatalf("a literal base_url bypasses the host rule: %q", url)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_HOST": "ftp://bad"}, "")
	if _, _, warns := r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport); len(warns) == 0 {
		t.Fatal("an invalid OLLAMA_HOST must warn")
	}
}

func TestLoad_RowHidden(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	rows := r.curated["amazon-bedrock"].head.Models
	if !rows["global.openai.gpt-5.6-sol"].Hidden || rows["openai.gpt-oss-120b"].Hidden || rows["anthropic.claude-opus-5"].Hidden {
		t.Fatal("bedrock row hiding wrong")
	}
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	ov := overlayWith("[providers.\"amazon-bedrock\".models.\"global.openai.gpt-5.6-sol\"]\ntransport = \"bedrock-mantle-openai\"\nprotocol = \"openai-responses\"\n")
	r2, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()), WithOverlay(ov))
	if err != nil {
		t.Fatal(err)
	}
	if r2.curated["amazon-bedrock"].head.Models["global.openai.gpt-5.6-sol"].Hidden {
		t.Fatal("a layer that supplies a transport must un-hide the row")
	}
}

// TestLoad_BedrockProfileRowsHiddenButResolvable pins spec §9.3's ruling on
// Bedrock's inference-profile ids. bedrock-mantle serves the six unprefixed
// Claude ids its own /v1/models catalog lists; the `global.`/`us.`/`eu.`/
// `jp.`/`au.`/`apac.` spellings address bedrock-runtime, which §1 puts out of
// scope, so a request for one 404s at the endpoint. They stay in the catalog
// for metadata and still resolve when a reference names one explicitly —
// resolution is strict but honest, and the endpoint's 404 tells the truth —
// but they are hidden, which is what keeps them out of `evener models list`
// (which filters on the resolved row's Hidden).
func TestLoad_BedrockProfileRowsHiddenButResolvable(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "AWS_BEARER_TOKEN_BEDROCK": "bt"}, "")

	for _, id := range []string{"global.anthropic.claude-sonnet-5", "us.anthropic.claude-fable-5", "eu.anthropic.claude-opus-5", "jp.anthropic.claude-sonnet-5", "au.anthropic.claude-sonnet-5"} {
		res, err := r.Resolve("amazon-bedrock/" + id)
		if err != nil {
			t.Fatalf("%s: a hidden row must still resolve when named: %v", id, err)
		}
		if !res.Model.Hidden {
			t.Fatalf("%s: profile rows must be hidden from listings", id)
		}
		if res.WireID != id {
			t.Fatalf("%s: wire id %q, want the id verbatim", id, res.WireID)
		}
		if res.Transport.BaseURL != "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1" {
			t.Fatalf("%s: base URL %q", id, res.Transport.BaseURL)
		}
	}

	// The ids Mantle actually serves stay visible.
	for _, id := range []string{"anthropic.claude-sonnet-5", "anthropic.claude-opus-5", "anthropic.claude-fable-5"} {
		res, err := r.Resolve("amazon-bedrock/" + id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if res.Model.Hidden {
			t.Fatalf("%s: the unprefixed ids Mantle serves must stay listed", id)
		}
	}

	// The rows stay in the catalog: hiding is a listing filter, not a delete.
	ids, err := r.CatalogModelIDs("amazon-bedrock")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, "global.anthropic.claude-sonnet-5") {
		t.Fatal("a hidden row must stay in the catalog for metadata")
	}
}

// TestLoad_CuratedDefaultsAreNotHidden pins the invariant the Bedrock profile
// ruling exposed: a provider's curated default_model / cheap_model is what a
// profile picks when nothing is configured, so naming a hidden row there
// hands every such session a reference the provider does not serve. Rows the
// catalog lacks are left alone — those synthesize by design (spec §7.3).
func TestLoad_CuratedDefaultsAreNotHidden(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	for _, id := range sortedKeys(r.curated) {
		rec := r.curated[id]
		for _, pick := range []struct{ what, model string }{
			{"default_model", rec.head.DefaultModel},
			{"cheap_model", rec.head.CheapModel},
		} {
			if pick.model == "" {
				continue
			}
			row, ok := rec.head.Models[pick.model]
			if !ok {
				continue
			}
			if row.Hidden {
				t.Errorf("providers.%s: %s = %q names a hidden row", id, pick.what, pick.model)
			}
		}
	}
}

func TestLoad_UserLayerTriState(t *testing.T) {
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	dir := t.TempDir()
	env := map[string]string{"XDG_CONFIG_HOME": dir, "EVENER_PROVIDERS_CONFIG": ""}
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || !strings.Contains(r.UserLayerNote(), "EVENER_PROVIDERS_CONFIG is empty") {
		t.Fatalf("empty variable: err=%v note=%q", err, r.UserLayerNote())
	}
	delete(env, "EVENER_PROVIDERS_CONFIG")
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || !strings.Contains(r.UserLayerNote(), filepath.Join(dir, "evener", "providers.toml")) {
		t.Fatalf("default path: err=%v note=%q", err, r.UserLayerNote())
	}
	path := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(path, []byte("[providers.groq]\nprotocol = \"openai-responses\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["EVENER_PROVIDERS_CONFIG"] = path
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || r.explicit["groq"] == nil {
		t.Fatalf("explicit path: err=%v", err)
	}
	if err := os.WriteFile(path, []byte("[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir())); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("old schema must surface as ErrOldSchema: %v", err)
	}
}

func TestLoad_WithInstances(t *testing.T) {
	r := fixtureLoad(t, nil, "", WithInstances(map[string]Provider{
		"tiny": {Base: "openai", Protocol: ProtocolOpenAIChat, Transport: Transport{BaseURL: "http://127.0.0.1:1/v1"}},
	}))
	tiny := r.explicit["tiny"]
	if tiny == nil || tiny.providerID != "openai" || tiny.head.Protocol != ProtocolOpenAIChat || !tiny.injected {
		t.Fatalf("tiny: %+v", tiny)
	}
}
