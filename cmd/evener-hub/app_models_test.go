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
	"primeradiant.com/evener/llm/registry"
)

type modelMetadataAdapter struct {
	name   string
	models []registry.Model
}

func (a *modelMetadataAdapter) Name() string { return a.name }

func (a *modelMetadataAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (a *modelMetadataAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func (a *modelMetadataAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	return append([]registry.Model(nil), a.models...), nil
}

// TestFetchLiveModels_CarriesListingCapabilitiesUnchanged pins what the hub
// does to a live listing: nothing. Every capability on the wire is one the
// client's ModelListing carried (spec §11.3), so a row the provider reported
// without a context window keeps none rather than borrowing one from a
// catalog the registry replaced.
func TestFetchLiveModels_CarriesListingCapabilitiesUnchanged(t *testing.T) {
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"kimi-anthropic-api": {Base: "kimi-for-coding", APIKey: "k"},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(&modelMetadataAdapter{
		name: "kimi-anthropic-api",
		models: []registry.Model{
			{ID: "k3"},
			{ID: "k3-256k", Caps: registry.Caps{ContextWindow: new(123_456)}},
		},
	})
	// Every other instance the registry knows gets a mute lister so no test
	// client can reach a real transport.
	for _, inst := range r.Instances() {
		if inst.Name != "kimi-anthropic-api" {
			client.Register(&modelMetadataAdapter{name: inst.Name})
		}
	}

	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() {
		liveModelLoadClient = oldLoadClient
	})

	server := NewWebServer(hubcore.WebConfig{})
	models := server.fetchLiveModels(context.Background())
	byModel := make(map[string]appwire.ModelDescriptor, len(models))
	for _, model := range models {
		byModel[model.Model] = model
	}

	if got, ok := byModel["k3"]; !ok {
		t.Fatalf("k3 missing from %+v", models)
	} else if got.ContextWindow != nil {
		t.Errorf("k3 context_window = %d, want none: the listing reported none", *got.ContextWindow)
	}
	if got, ok := byModel["k3-256k"]; !ok {
		t.Fatalf("k3-256k missing from %+v", models)
	} else if got.ContextWindow == nil || *got.ContextWindow != 123_456 {
		t.Errorf("k3-256k context_window = %v, want the listing's 123456", got.ContextWindow)
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
	models := enrichModelListResponse(appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
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

// TestEnrichModelListResponse_KeepsCapabilitiesAndAddsDisplayNames pins what
// the response pipeline is still allowed to do to a descriptor: fill a blank
// display name and sort. Every capability came from the registry's Resolved
// record before it got here (spec §11.3), so nothing may add or overwrite one.
func TestEnrichModelListResponse_KeepsCapabilitiesAndAddsDisplayNames(t *testing.T) {
	contextWindow := 7
	supportsTools := false
	in := appwire.ModelDescriptor{
		Provider:      "anthropic",
		Model:         "claude-opus-4-6",
		DisplayName:   "Configured",
		ContextWindow: &contextWindow,
		SupportsTools: &supportsTools,
	}
	got := enrichModelListResponse(appwire.ModelListResponse{Data: []appwire.ModelDescriptor{in}}).Data
	if len(got) != 1 {
		t.Fatalf("got %d descriptors, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], in) {
		t.Fatalf("descriptor changed: got %+v, want %+v", got[0], in)
	}
}

// TestEnrichModelListResponse_ModelWithoutCapsStillRenders pins the
// graceful-degradation rule: a model the registry carries no capabilities for
// must still render name+provider+id, just without any badge fields.
func TestEnrichModelListResponse_ModelWithoutCapsStillRenders(t *testing.T) {
	models := enrichModelListResponse(appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "mycompany", Model: "totally-unknown-model-xyz"},
	}}).Data
	if len(models) != 1 {
		t.Fatalf("model without caps was dropped: got %d entries, want 1", len(models))
	}
	m := models[0]
	if m.Provider != "mycompany" || m.Model != "totally-unknown-model-xyz" {
		t.Fatalf("entry missing provider/model: %+v", m)
	}
	if m.DisplayName != "Totally Unknown Model Xyz" {
		t.Errorf("display name = %v, want the prettified id", m.DisplayName)
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
			t.Errorf("entry with no registry caps should omit %q", field)
		}
	}
}

// TestEnrichModelListResponse_DropsIncompleteDescriptors: a row with no
// provider or no model id has nothing to select, so it never reaches the
// picker.
func TestEnrichModelListResponse_DropsIncompleteDescriptors(t *testing.T) {
	got := enrichModelListResponse(appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "", Model: "orphan"},
		{Provider: "openai", Model: "  "},
		{Provider: "openai", Model: "gpt-5.2"},
	}}).Data
	if len(got) != 1 || got[0].Model != "gpt-5.2" {
		t.Fatalf("got %+v, want only the complete descriptor", got)
	}
}

// TestWithDisplayNames_DoesNotMutateItsInput: the model list is served from a
// cache the hub keeps, so filling a blank display name must produce new
// descriptors rather than write through to the cached ones.
func TestWithDisplayNames_DoesNotMutateItsInput(t *testing.T) {
	in := []appwire.ModelDescriptor{{Provider: "anthropic", Model: "claude-opus-4-6"}}
	out := withDisplayNames(in)
	if out[0].DisplayName == "" {
		t.Fatal("the copy did not get a display name, so this test proves nothing")
	}
	if in[0].DisplayName != "" {
		t.Fatalf("the input was mutated: %+v", in[0])
	}
}
