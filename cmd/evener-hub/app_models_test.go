package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/valueexpr"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/tokenauth"
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

// modelListAuthRecorder lists models and keeps the request context, so a
// test can assert which authenticator the hub bound to the fetch.
type modelListAuthRecorder struct {
	name       string
	models     []registry.Model
	requestCtx context.Context
}

func (a *modelListAuthRecorder) Name() string { return a.name }

func (a *modelListAuthRecorder) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (a *modelListAuthRecorder) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func (a *modelListAuthRecorder) LiveModels(ctx context.Context) ([]registry.Model, error) {
	a.requestCtx = ctx
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

// The model picker's live pass mints nothing for a command-credentialed
// instance: the hub executes credential commands never (spec §10.1), so
// the picker serves that instance's registry rows — every advertised
// fact, no credential materialized — and leaves its live listing to the
// child.
func TestFetchLiveModelsSkipsCommandCredentialedInstances(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\napi_key = '''$(gw-mint)'''\n" +
		"[providers.gw.models.\"house-model\"]\n"
	if err := os.WriteFile(tomlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(tomlPath),
		registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true),
		registry.WithoutCache(),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	// Every other instance the registry knows gets a mute lister so no
	// test client can reach a real transport.
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			client.Register(&modelMetadataAdapter{name: inst.Name})
		}
	}
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	server := NewWebServer(hubcore.WebConfig{})
	models := server.fetchLiveModels(context.Background())
	found := false
	for _, m := range models {
		if m.Model == "house-model" && m.Provider == "gw" {
			found = true
		}
	}
	if !found {
		t.Fatal("the picker dropped the command-credentialed instance's registry rows; skipping the live fetch must skip the mint, not the models")
	}
	if runs != 0 {
		t.Fatalf("the model picker executed the credential command %d time(s); the hub never runs credential commands", runs)
	}
	if hits != 0 {
		t.Fatal("the model picker fetched a live listing with a credential it never materialized")
	}
}

// TestFetchLiveModels_BindsTheRegistrysCodexScope pins the model-list half of
// a wrong-record bug: the fetch must authenticate with the registry client's
// own state root, not whatever root the process-global Codex holds. The
// adapter records the request context, so the assertion is about what the
// fetch actually carried.
func TestFetchLiveModels_BindsTheRegistrysCodexScope(t *testing.T) {
	root := t.TempDir()
	writeCodexOAuthRecord(t, root, "codex-work")
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(root),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"codex-work": {Base: "openai-codex"}}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	recorder := &modelListAuthRecorder{name: "codex-work", models: []registry.Model{{ID: "gpt-5.6"}}}
	client.Register(recorder)
	// Every other instance the registry knows gets a mute lister so no test
	// client can reach a real transport.
	for _, inst := range r.Instances() {
		if inst.Name != "codex-work" {
			client.Register(&modelMetadataAdapter{name: inst.Name})
		}
	}
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	models := NewWebServer(hubcore.WebConfig{}).fetchLiveModels(context.Background())
	if len(models) == 0 {
		t.Fatal("the fetch listed nothing: it never reached the provider seam")
	}
	auth := llm.AuthenticatorOverrideFor(recorder.requestCtx, registry.AuthOAuthOpenAICodex)
	codex, ok := auth.(*tokenauth.Codex)
	if !ok {
		t.Fatalf("fetch context carried %T, want a scoped *tokenauth.Codex", auth)
	}
	if codex.StateDir != root {
		t.Fatalf("scoped Codex state dir = %q, want the client registry's %q", codex.StateDir, root)
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

// TestAttachRecentModels_ExcludesDelegateSessions pins the machinery filter
// upstream of the availability filter: a delegate session's (provider, model)
// pair is inherited or overridden at spawn, never chosen in the picker, so it
// must not surface in the Recent group even when the pair is offered in
// resp.Data — mirroring RecentProjectDirs' IsSubagent skip.
func TestAttachRecentModels_ExcludesDelegateSessions(t *testing.T) {
	past := hubcore.NewPastIndex("")
	now := time.Now().UTC()
	past.SeedForTest([]schema.SessionMeta{
		{ID: "delegate", ProfileID: "lunaroute", Model: "glm-5.3-flash", IsSubagent: true, UpdatedAt: now.Add(-1 * time.Minute)},
		{ID: "root-b", ProfileID: "zai", Model: "glm-5.3", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "root-a", ProfileID: "openai", Model: "gpt-5.2", UpdatedAt: now.Add(-3 * time.Minute)},
	})
	cfg := hubcore.WebConfig{Past: past}
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
		{Provider: "lunaroute", Model: "glm-5.3-flash", DisplayName: "GLM 5.3 Flash"},
		{Provider: "zai", Model: "glm-5.3", DisplayName: "GLM 5.3"},
		{Provider: "openai", Model: "gpt-5.2", DisplayName: "GPT-5.2"},
	}}
	got := attachRecentModels(cfg, resp)
	want := []appwire.ModelDescriptor{
		{Provider: "zai", Model: "glm-5.3", DisplayName: "GLM 5.3"},
		{Provider: "openai", Model: "gpt-5.2", DisplayName: "GPT-5.2"},
	}
	if !reflect.DeepEqual(got.Recent, want) {
		t.Fatalf("Recent = %+v, want %+v (a delegate session's pair must not surface)", got.Recent, want)
	}
}

func TestPrettifyModelDisplayName(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":             "Claude Opus 4 6",
		"claude-opus-4-6-20251101":    "Claude Opus 4 6", // dated snapshot suffix stripped first
		"claude-opus-4-6-20251101-v1": "Claude Opus 4 6", // dated snapshot + version tag both stripped
		// Vertex dates with "@YYYYMMDD" and Bedrock adds a ":N" revision to
		// its "-vN" tag; both are first-class catalog ids (spec §9.4).
		"claude-sonnet-4-5@20250929":      "Claude Sonnet 4 5",
		"claude-sonnet-4-5-20250929-v1:0": "Claude Sonnet 4 5",
		"gpt-5.1":                         "Gpt 5.1",
		"o3-deep-research":                "O3 Deep Research",
		"glm-5.2":                         "Glm 5.2",
		"bare":                            "Bare",
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
		t.Error("dated snapshot suffix should still be detected with a trailing -v1 version tag")
	}
	if !isDatedSnapshotModelID("claude-sonnet-4-5@20250929") {
		t.Error("Vertex dates with @YYYYMMDD, which is a dated snapshot too")
	}
	if !isDatedSnapshotModelID("claude-sonnet-4-5-20250929-v1:0") {
		t.Error("Bedrock's -vN:N revision follows the date, and the id is still dated")
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
