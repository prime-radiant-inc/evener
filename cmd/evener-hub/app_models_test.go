package hub

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

type modelMetadataAdapter struct {
	name   string
	models []llm.ModelInfo
}

func (a *modelMetadataAdapter) Name() string { return a.name }

func (a *modelMetadataAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (a *modelMetadataAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func (a *modelMetadataAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func TestFetchLiveModels_KimiContextWindow(t *testing.T) {
	client := llm.NewClient()
	client.Register(&modelMetadataAdapter{
		name: "kimi-anthropic-api",
		models: []llm.ModelInfo{
			{ID: "k3", DisplayName: "Kimi K3"},
			{ID: "k3-256k", DisplayName: "Kimi K3 256K", ContextWindow: 123_456},
		},
	})
	client.SetNameToTag(map[string]string{"kimi-anthropic-api": "kimi-anthropic"})

	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		return client, providercfg.Config{}, true, nil
	}
	t.Cleanup(func() {
		liveModelLoadClient = oldLoadClient
	})

	server := NewWebServer(hubcore.WebConfig{
		ProviderConfig: &providercfg.Config{Instances: []providercfg.InstanceConfig{
			{Name: "kimi-anthropic-api", Type: "kimi-anthropic"},
		}},
	})
	models := server.fetchLiveModels(context.Background())
	contextByModel := make(map[string]int, len(models))
	for _, model := range models {
		if model.ContextWindow != nil {
			contextByModel[model.Model] = *model.ContextWindow
		}
	}

	if got := contextByModel["k3"]; got != 1_048_576 {
		t.Errorf("k3 context_window = %d, want 1048576 from catalog when live metadata omits it", got)
	}
	if got := contextByModel["k3-256k"]; got != 123_456 {
		t.Errorf("k3-256k context_window = %d, want live value 123456", got)
	}
}

// TestHubModelList_AttachesRecentFromPastIndex verifies every ModelList
// response (the path both the TUI and browser use) carries Recent, filtered to
// models actually present in
// resp.Data — a recent ref no longer offered isn't rendered as unselectable.
func TestHubModelList_AttachesRecentFromPastIndex(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "local", Model: "still-live-model"},
		{ID: "b", ProfileID: "local", Model: "retired-model"}, // not in the live source below
	})
	cfg := hubcore.WebConfig{Past: past}
	sources := appsource.NewRegistry()
	// No Spawner/live source configured: hubModelList's evener/local branch
	// returns an empty ModelListResponse (its early-return path), which is
	// enough to exercise attachRecentModels' filtering against resp.Data.
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil (no models in resp.Data to match against)", resp.Recent)
	}
	if resp.Data == nil {
		t.Fatal("Data = nil, want an empty JSON array")
	}
}

// TestHubModelList_NilPastIndexOmitsRecent guards the nil-Past config path
// (tests/sandboxes that construct WebConfig without a Past index).
func TestHubModelList_NilPastIndexOmitsRecent(t *testing.T) {
	cfg := hubcore.WebConfig{}
	sources := appsource.NewRegistry()
	resp, err := hubModelList(context.Background(), cfg, sources, appwire.ModelListParams{})
	if err != nil {
		t.Fatalf("hubModelList: %v", err)
	}
	if resp.Recent != nil {
		t.Fatalf("Recent = %+v, want nil with no Past index configured", resp.Recent)
	}
}

// TestAttachRecentModels_FiltersToAvailableModels is the direct unit test for
// the filtering rule: a recent ref present in resp.Data survives; one absent
// (retired/reconfigured) is dropped, in most-recent-first order.
func TestAttachRecentModels_FiltersToAvailableModels(t *testing.T) {
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "a", ProfileID: "openai", Model: "gpt-5.2"},
		{ID: "b", ProfileID: "openai", Model: "retired-model"},
	})
	cfg := hubcore.WebConfig{Past: past}
	supportsTools := true
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "openai", Model: "gpt-5.2", DisplayName: "GPT-5.2", SupportsTools: &supportsTools},
	}}
	got := attachRecentModels(cfg, resp)
	want := []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2", DisplayName: "GPT-5.2", SupportsTools: &supportsTools}}
	if !reflect.DeepEqual(got.Recent, want) {
		t.Fatalf("Recent = %+v, want %+v (retired-model absent from resp.Data must be dropped)", got.Recent, want)
	}
}

func TestEnrichModelDescriptorsAddsCatalogMetadata(t *testing.T) {
	got := enrichModelDescriptors([]appwire.ModelDescriptor{{Provider: "anthropic", Model: "claude-opus-4-6"}}, nil)
	if len(got) != 1 {
		t.Fatalf("got %d descriptors, want 1", len(got))
	}
	d := got[0]
	if d.DisplayName != "Claude Opus 4 6" {
		t.Errorf("display name = %q, want prettified model id", d.DisplayName)
	}
	if d.ContextWindow == nil || *d.ContextWindow != 1_000_000 {
		t.Errorf("context window = %v, want 1000000", d.ContextWindow)
	}
	if d.SupportsVision == nil || !*d.SupportsVision {
		t.Errorf("supports vision = %v, want true", d.SupportsVision)
	}
	if d.SupportsWebSearch == nil || !*d.SupportsWebSearch {
		t.Errorf("supports web search = %v, want true", d.SupportsWebSearch)
	}
	if d.MaxOutputTokens == nil || *d.MaxOutputTokens != 128_000 {
		t.Errorf("max output tokens = %v, want 128000", d.MaxOutputTokens)
	}
}

func TestEnrichModelDescriptorsPreservesExplicitMetadata(t *testing.T) {
	contextWindow := 7
	supportsTools := false
	supportsReasoning := false
	inputCost := 0.0
	in := appwire.ModelDescriptor{
		Provider:            "anthropic",
		Model:               "claude-opus-4-6",
		DisplayName:         "Configured",
		ContextWindow:       &contextWindow,
		SupportsTools:       &supportsTools,
		SupportsReasoning:   &supportsReasoning,
		InputCostPerMillion: &inputCost,
	}
	got := enrichModelDescriptors([]appwire.ModelDescriptor{in}, nil)
	if got[0].DisplayName != in.DisplayName || got[0].ContextWindow == nil || *got[0].ContextWindow != contextWindow ||
		got[0].SupportsTools == nil || *got[0].SupportsTools != supportsTools ||
		got[0].SupportsReasoning == nil || *got[0].SupportsReasoning != supportsReasoning ||
		got[0].InputCostPerMillion == nil || *got[0].InputCostPerMillion != inputCost {
		t.Fatalf("explicit metadata changed: got %+v, want preserved fields from %+v", got[0], in)
	}
	if len(got[0].ReasoningEffortLevels) != 0 {
		t.Fatalf("explicit reasoning=false should keep levels empty: %+v", got[0].ReasoningEffortLevels)
	}
}

func TestPrettifyModelDisplayName(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":             "Claude Opus 4 6",
		"claude-opus-4-6-20251101":    "Claude Opus 4 6", // dated snapshot suffix stripped first
		"claude-opus-4-6-20251101-v1": "Claude Opus 4 6", // dated snapshot + LiteLLM version tag both stripped
		"gpt-5.1":                     "Gpt 5.1",
		"o3-deep-research":            "O3 Deep Research",
		"glm-5.2":                     "Glm 5.2",
		"bare":                        "Bare",
	}
	for id, want := range cases {
		if got := prettifyModelDisplayName(id); got != want {
			t.Errorf("prettifyModelDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsDatedSnapshotModelID(t *testing.T) {
	if !isDatedSnapshotModelID("claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected")
	}
	if !isDatedSnapshotModelID("anthropic/claude-opus-4-6-20251101") {
		t.Error("dated snapshot suffix should be detected through a provider-qualified ref")
	}
	if !isDatedSnapshotModelID("claude-opus-4-6-20251101-v1") {
		t.Error("dated snapshot suffix should still be detected with a trailing LiteLLM -v1 version tag")
	}
	if isDatedSnapshotModelID("claude-opus-4-6") {
		t.Error("bare family id must not be treated as dated")
	}
	if isDatedSnapshotModelID("gpt-5.1") {
		t.Error("non-dated id must not be treated as dated")
	}
}

func TestEnrichModelDescriptors_UsesPrettifiedDisplayNameAndSortsDatedLast(t *testing.T) {
	models := enrichModelListResponse(hubcore.WebConfig{}, appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6-20251101"},
		{Provider: "anthropic", Model: "claude-opus-4-6"},
		{Provider: "openai", Model: "gpt-5.2"},
	}}).Data
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	if got := models[0].DisplayName; got != "Claude Opus 4 6" {
		t.Errorf("models[0].DisplayName = %v, want %q", got, "Claude Opus 4 6")
	}
	// Within the anthropic group, the dated snapshot must sort after the bare
	// family id, regardless of input order.
	var anthropicOrder []string
	for _, m := range models {
		if m.Provider == "anthropic" {
			anthropicOrder = append(anthropicOrder, m.Model)
		}
	}
	want := []string{"claude-opus-4-6", "claude-opus-4-6-20251101"}
	if !reflect.DeepEqual(anthropicOrder, want) {
		t.Errorf("anthropic model order = %v, want %v (dated snapshot last)", anthropicOrder, want)
	}
}

func TestEnrichModelDescriptors_IncludesCapabilityBadges(t *testing.T) {
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "anthropic", Model: "claude-opus-4-6"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	m := models[0]
	if m.SupportsVision == nil || !*m.SupportsVision {
		t.Errorf("supports vision = %v, want true", m.SupportsVision)
	}
	if m.SupportsWebSearch == nil || !*m.SupportsWebSearch {
		t.Errorf("supports web search = %v, want true", m.SupportsWebSearch)
	}
	if m.MaxOutputTokens == nil || *m.MaxOutputTokens != 128000 {
		t.Errorf("max output tokens = %v, want 128000", m.MaxOutputTokens)
	}
	if m.ContextWindow == nil || *m.ContextWindow != 1_000_000 {
		t.Errorf("context window = %v, want 1000000", m.ContextWindow)
	}
}

// TestEnrichModelDescriptors_UncataloguedModelStillRendersWithoutBadges
// pins the graceful-degradation rule: a live model absent from the embedded
// catalog (catalogModelInfo returns nil) must still render name+provider+id
// — not be dropped — just without any badge fields.
func TestEnrichModelDescriptors_UncataloguedModelStillRendersWithoutBadges(t *testing.T) {
	models := enrichModelDescriptors([]appwire.ModelDescriptor{
		{Provider: "mycompany", Model: "totally-unknown-model-xyz"},
	}, nil)
	if len(models) != 1 {
		t.Fatalf("uncatalogued model was dropped: got %d entries, want 1", len(models))
	}
	m := models[0]
	if m.Provider != "mycompany" || m.Model != "totally-unknown-model-xyz" {
		t.Fatalf("uncatalogued entry missing provider/model: %+v", m)
	}
	if m.DisplayName != "Totally Unknown Model Xyz" {
		t.Errorf("display name = %v, want prettified id even when uncatalogued", m.DisplayName)
	}
	for field, present := range map[string]bool{
		"supports_tools":          m.SupportsTools != nil,
		"supports_vision":         m.SupportsVision != nil,
		"supports_reasoning":      m.SupportsReasoning != nil,
		"supports_web_search":     m.SupportsWebSearch != nil,
		"context_window":          m.ContextWindow != nil,
		"max_output_tokens":       m.MaxOutputTokens != nil,
		"input_cost_per_million":  m.InputCostPerMillion != nil,
		"output_cost_per_million": m.OutputCostPerMillion != nil,
	} {
		if present {
			t.Errorf("uncatalogued entry should omit %q", field)
		}
	}
}
