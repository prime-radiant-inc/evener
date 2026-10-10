package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/internal/credentials"
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
// child. Those rows pass the same §5 visibility filter the child's own
// listing applies, so nothing the child hides reaches the picker.
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
	// The instance bases on a curated provider whose catalog carries
	// hidden rows (bedrock's non-anthropic ids, §9.3), so its registry
	// rows include some the child's own listing drops. The base_url
	// override keeps a misbehaving fetch on the local server, not the
	// real mantle.
	cfg := "[providers.gw]\nbase = \"amazon-bedrock\"\nbase_url = \"" + srv.URL + "/anthropic/v1\"\napi_key = '''$(gw-mint)'''\n" +
		"[providers.gw.vars]\n\"AWS_REGION\" = \"eu-west-1\"\n" +
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
	// The rows the child's own listing would hide, for the assertion
	// below: resolved at the facts depth the picker itself resolves,
	// so the fixture mints nothing either.
	ids, err := r.ModelIDs("gw")
	if err != nil {
		t.Fatalf("model ids: %v", err)
	}
	hidden := make(map[string]bool)
	for _, id := range ids {
		if row, err := r.ResolveInstanceModelFacts("gw", id); err == nil && row.Model.Hidden {
			hidden[id] = true
		}
	}
	if len(hidden) == 0 {
		t.Fatal("fixture setup: the bedrock-based instance carries no hidden rows, so this test guards nothing")
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
	for _, m := range models {
		if m.Provider == "gw" && hidden[m.Model] {
			t.Fatalf("the picker served %q, a row the child's own listing hides (spec §5)", m.Model)
		}
	}
	if runs != 0 {
		t.Fatalf("the model picker executed the credential command %d time(s); the hub never runs credential commands", runs)
	}
	if hits != 0 {
		t.Fatal("the model picker fetched a live listing with a credential it never materialized")
	}
}

// A row that pins its own auth scheme reaches the command credential
// under the row's transport, whatever the provider-level scheme says:
// the picker must refuse the live fetch on the predicate that sees the
// override, or the automatic view mints through the row.
func TestFetchLiveModelsSkipsRowAuthOverrideInstances(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-live"}]}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \"" + srv.URL + "/v1\"\nprotocol = \"openai-chat\"\nauth = \"none\"\napi_key = '''$(gw-mint)'''\n" +
		"[providers.gw.models.\"house-model\"]\nauth = \"bearer\"\n"
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
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			client.Register(&modelMetadataAdapter{name: inst.Name})
		}
	}
	oldLoadClient := liveModelLoadClient
	liveModelLoadClient = func(string) (*llm.Client, error) { return client, nil }
	t.Cleanup(func() { liveModelLoadClient = oldLoadClient })

	models := NewWebServer(hubcore.WebConfig{}).fetchLiveModels(context.Background())
	found := false
	for _, m := range models {
		if m.Model == "house-model" && m.Provider == "gw" {
			found = true
		}
	}
	if !found {
		t.Fatal("the picker dropped the instance's registry rows; refusing the live fetch must refuse the mint, not the models")
	}
	if runs != 0 {
		t.Fatalf("the picker executed the credential command %d time(s) through the row's auth override", runs)
	}
	if hits != 0 {
		t.Fatal("the picker fetched a live listing through the row's auth override")
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

// countLaunchContractSpawner counts how many times the evener launch contract
// was asked for and can vary the answer per call, so a test can tell a cache
// hit from a fresh `evener launch-check --models` and a stale serve from a
// background refresh.
type countLaunchContractSpawner struct {
	fakeRPCSpawner
	mu       sync.Mutex
	calls    int
	lastCtx  context.Context
	modelsFn func(call int, workingDir string) appwire.ModelListResponse
	err      error
}

func (f *countLaunchContractSpawner) ListLaunchModelContract(ctx context.Context) (appwire.ModelListResponse, error) {
	return f.record(ctx, "")
}

func (f *countLaunchContractSpawner) ListLaunchModelContractForWorkingDir(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	return f.record(ctx, workingDir)
}

// record notes the call under the lock, then runs modelsFn outside it: a test
// whose modelsFn blocks must still be able to read callCount and contextOf.
func (f *countLaunchContractSpawner) record(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
	f.mu.Lock()
	f.calls++
	f.lastCtx = ctx
	call, fn, err := f.calls, f.modelsFn, f.err
	f.mu.Unlock()
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	if fn == nil {
		return appwire.ModelListResponse{}, nil
	}
	return fn(call, workingDir), nil
}

func (f *countLaunchContractSpawner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *countLaunchContractSpawner) contextOf() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCtx
}

// newLaunchModelsTestWeb builds the minimal WebServer fetchLaunchModels needs:
// its cfg (with the spawner and, when asked, a holder whose generation a test
// can bump) and the cache the constructor would have built.
func newLaunchModelsTestWeb(t *testing.T, spawner hubcore.Spawner, withRegistry bool) *WebServer {
	t.Helper()
	cfg := hubcore.WebConfig{Spawner: spawner}
	if withRegistry {
		cfg.Registry = newBumpableProviderRegistry(t)
	}
	web := &WebServer{
		cfg:          cfg,
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.Background(),
	}
	// The constructor wires this; mirror it so a test that drives the RPC path
	// exercises the same cached loader production does.
	if web.cfg.LaunchModels == nil {
		web.cfg.LaunchModels = web.fetchLaunchModels
	}
	return web
}

// newBumpableProviderRegistry returns a holder a test can Reload to bump its
// generation, which is what retires a cached launch model list.
func newBumpableProviderRegistry(t *testing.T) *hubcore.ProviderRegistry {
	t.Helper()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		opts := append([]registry.Option{
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
		}, extra...)
		r, err := registry.Load(opts...)
		return r, nil, err
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return holder
}

// TestFetchLaunchModelsCachesPerWorkingDir: a second read of the same working
// dir is served from the cache instead of spawning another launch check, a
// different working dir is a different key, and a caller mutating what it read
// cannot corrupt the cached entry.
func TestFetchLaunchModelsCachesPerWorkingDir(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
			{Provider: "openai", Model: "gpt-5.5", Warnings: []string{"note"}},
		}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)

	first, err := web.fetchLaunchModels(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	second, err := web.fetchLaunchModels(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times for two reads of one key, want 1", got)
	}
	if !reflect.DeepEqual(first.Data, second.Data) {
		t.Fatalf("cached read differs from the first: %+v vs %+v", first.Data, second.Data)
	}

	second.Data[0].Model = "mutated"
	second.Data[0].Warnings[0] = "mutated"
	third, err := web.fetchLaunchModels(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if third.Data[0].Model != "gpt-5.5" || third.Data[0].Warnings[0] != "note" {
		t.Fatalf("a returned copy wrote through to the cached entry: %+v", third.Data[0])
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times after a mutating read, want 1", got)
	}

	if _, err := web.fetchLaunchModels(context.Background(), "/tmp/other"); err != nil {
		t.Fatalf("fetchLaunchModels(other): %v", err)
	}
	if got := spawner.callCount(); got != 2 {
		t.Fatalf("launch contract called %d times, want 2 after a second working dir", got)
	}
}

// TestFetchLaunchModelsServesStaleThenRefreshesOnGenerationBump: a Reload
// retires the cached entry, but the next read answers from it immediately and
// refreshes in the background rather than blocking on the launch check.
func TestFetchLaunchModelsServesStaleThenRefreshesOnGenerationBump(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(call int, _ string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
			{Provider: "openai", Model: fmt.Sprintf("gen-%d", call)},
		}}
	}}
	reg := newBumpableProviderRegistry(t)
	web := &WebServer{
		cfg:          hubcore.WebConfig{Registry: reg, Spawner: spawner},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.Background(),
	}

	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times, want 1", got)
	}

	if err := reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	stale, err := web.fetchLaunchModels(context.Background(), "")
	if err != nil {
		t.Fatalf("fetchLaunchModels after bump: %v", err)
	}
	if stale.Data[0].Model != "gen-1" {
		t.Fatalf("a generation bump served %q, want the stale gen-1 immediately", stale.Data[0].Model)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := web.fetchLaunchModels(context.Background(), "")
		if err != nil {
			t.Fatalf("fetchLaunchModels: %v", err)
		}
		if resp.Data[0].Model == "gen-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cache never refreshed after a generation bump: last=%q calls=%d", resp.Data[0].Model, spawner.callCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := spawner.callCount(); got != 2 {
		t.Fatalf("launch contract called %d times, want 2 (one load, one refresh)", got)
	}
}

// TestFetchLaunchModelsDoesNotCacheWithoutRegistry: with no holder generation
// to gate on, a cached entry could never be invalidated, so every read asks the
// spawner afresh.
func TestFetchLaunchModelsDoesNotCacheWithoutRegistry(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, false)
	for range 2 {
		if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
			t.Fatalf("fetchLaunchModels: %v", err)
		}
	}
	if got := spawner.callCount(); got != 2 {
		t.Fatalf("launch contract called %d times without a registry, want 2 (caching disabled)", got)
	}
}

// TestStartLaunchModelsPrefetchWarmsTheCache: the startup warm runs the launch
// check once and fills the unscoped entry, so the first picker read after hub
// start does not block on the live listing.
func TestStartLaunchModelsPrefetchWarmsTheCache(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)
	ctx := t.Context()
	go startLaunchModelsPrefetch(ctx, web, func(fn func()) { fn() })

	deadline := time.Now().Add(5 * time.Second)
	for spawner.callCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the startup warm never ran the launch check")
		}
		time.Sleep(2 * time.Millisecond)
	}

	resp, err := web.fetchLaunchModels(ctx, "")
	if err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", resp.Data)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("after the startup warm the first picker read ran the launch check again: calls=%d, want 1", got)
	}
}

// TestStartLaunchModelsPrefetchRunsOnce: the launch warm is one startup pass
// and never a timer (Jesse, 2026-09-30); after it the picker refreshes the
// list when it is opened.
func TestStartLaunchModelsPrefetchRunsOnce(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)
	runs := runStartupPrefetch(t, func(startBackground func(func())) {
		startLaunchModelsPrefetch(t.Context(), web, startBackground)
	})
	if runs != 1 || spawner.callCount() != 1 {
		t.Fatalf("background runs = %d, launch checks = %d; want one of each", runs, spawner.callCount())
	}
}

// TestFetchLaunchModelsCoalescesColdLoads: concurrent reads of one uncached key
// share a single launch check, so a burst of cold picker opens — or a read
// racing the startup warm — spawns one child, not one per reader.
func TestFetchLaunchModelsCoalescesColdLoads(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		// close-once, so both a coalesced run (one entry) and a broken one (many
		// entries) signal exactly once and the test can never block on it.
		enterOnce.Do(func() { close(entered) })
		<-release
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)

	const readers = 8
	var started atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	start := make(chan struct{})
	for range readers {
		wg.Go(func() {
			<-start
			started.Add(1)
			resp, err := web.fetchLaunchModels(context.Background(), "")
			if err != nil {
				errs <- err
				return
			}
			if len(resp.Data) != 1 || resp.Data[0].Model != "gpt-5.5" {
				errs <- fmt.Errorf("models=%+v", resp.Data)
			}
		})
	}
	close(start)

	// The leader is blocked inside the launch check until release, so every
	// reader that has started reaches the shared flight while it is in progress.
	// singleflight exposes no in-flight count, so "all readers started" plus the
	// blocked leader is the observable bound; the assertion below is still exact.
	waitFor(t, func() bool { return started.Load() == readers }, "the readers did not all start")
	waitFor(t, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	}, "no reader entered the launch check")
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cold reader: %v", err)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times for %d concurrent cold readers, want 1", got, readers)
	}
}

// TestLoadLaunchModelsReusesAFreshEntry: the loader re-checks the cache inside
// the shared flight. A caller whose cache snapshot predates a concurrent load's
// store reaches the flight only after that load published its entry — the store
// runs inside the flight, before it clears — so the re-check hands it the entry
// instead of spawning a second launch check. This drives the loader directly,
// since a real fetch caller would have hit the entry at its own snapshot.
func TestLoadLaunchModelsReusesAFreshEntry(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)
	gen, ok := liveModelsGeneration(web)
	if !ok {
		t.Fatal("the test server has no holder generation")
	}

	// The first load fills the unscoped entry.
	if _, err := web.loadLaunchModels(context.Background(), "", gen); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// A second reader that arrived with a stale snapshot is served the entry the
	// first load published rather than running the launch check again.
	if _, err := web.loadLaunchModels(context.Background(), "", gen); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times, want 1 (second reader served the filled entry)", got)
	}
}

// waitFor polls cond until it holds or five seconds pass.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRefreshLaunchModelsUsesTheServerLifetime pins the shutdown-hygiene fix:
// a request-triggered refresh hangs off the server's lifetime context, so hub
// shutdown cancels an outstanding launch check instead of leaving the child to
// outlive the hub. Before the fix the parent was context.Background(), which
// carries no lifetime marker and outlives cancellation.
func TestRefreshLaunchModelsUsesTheServerLifetime(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	reg := newBumpableProviderRegistry(t)
	type lifetimeKey struct{}
	web := &WebServer{
		cfg:          hubcore.WebConfig{Registry: reg, Spawner: spawner},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.WithValue(context.Background(), lifetimeKey{}, "hub-lifetime"),
	}

	// The first read is a cold load on the caller's context; the second, after a
	// Reload retires the entry, is served stale and refreshes behind it.
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels after bump: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for spawner.callCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the stale read never refreshed")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got, _ := spawner.contextOf().Value(lifetimeKey{}).(string); got != "hub-lifetime" {
		t.Fatalf("the refresh's context does not descend from the server lifetime: marker=%q, ctx=%v", got, spawner.contextOf())
	}
}

// TestColdLaunchLoadUsesTheServerLifetime: the shared cold load runs on the
// server lifetime, so a leader whose client disconnected cannot fail the
// readers that joined its singleflight flight.
func TestColdLaunchLoadUsesTheServerLifetime(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	reg := newBumpableProviderRegistry(t)
	type lifetimeKey struct{}
	web := &WebServer{
		cfg:          hubcore.WebConfig{Registry: reg, Spawner: spawner},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.WithValue(context.Background(), lifetimeKey{}, "hub-lifetime"),
	}

	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if got, _ := spawner.contextOf().Value(lifetimeKey{}).(string); got != "hub-lifetime" {
		t.Fatalf("the cold load did not run on the server lifetime: marker=%q", got)
	}
}

// TestStoreLaunchModelsDoesNotClobberANewerEntry: the holder generation only
// advances, so a load that finished late must not overwrite an entry installed
// at a newer generation.
func TestStoreLaunchModelsDoesNotClobberANewerEntry(t *testing.T) {
	t.Parallel()
	web := &WebServer{
		cfg:          hubcore.WebConfig{},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
	}
	newer := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "newer"}}}
	older := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "older"}}}
	web.storeLaunchModels("", 7, newer)
	web.storeLaunchModels("", 3, older)
	if got := web.launchModels.entries[""].resp.Data[0].Model; got != "newer" {
		t.Fatalf("a load from generation 3 clobbered the generation 7 entry: got %q", got)
	}
}

// TestStoreLaunchModelsEvictsPastTheCap: the user-driven working-dir key space
// stays bounded, and the entry dropped is the least-recently-filled one.
func TestStoreLaunchModelsEvictsPastTheCap(t *testing.T) {
	t.Parallel()
	web := &WebServer{
		cfg:          hubcore.WebConfig{},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
	}
	resp := appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	for i := range launchModelsMaxEntries {
		key := fmt.Sprintf("/seed/%d", i)
		web.launchModels.entries[key] = &launchModelsEntry{
			resp:     cloneModelListResponse(resp),
			gen:      1,
			filledAt: time.Unix(int64(i), 0),
		}
	}
	web.storeLaunchModels("/new", 1, resp)

	if got := len(web.launchModels.entries); got != launchModelsMaxEntries {
		t.Fatalf("cache holds %d entries, want the cap %d", got, launchModelsMaxEntries)
	}
	if _, ok := web.launchModels.entries["/seed/0"]; ok {
		t.Fatal("the least-recently-filled entry survived eviction")
	}
	if _, ok := web.launchModels.entries["/new"]; !ok {
		t.Fatal("the newly stored entry is missing")
	}
}

// TestEvictOldestLaunchModelsEntryHandlesTheUnscopedKey: "" is a real key (the
// unscoped list), so the selection must not read it as "nothing seen yet".
func TestEvictOldestLaunchModelsEntryHandlesTheUnscopedKey(t *testing.T) {
	t.Parallel()
	base := time.Now()
	entries := map[string]*launchModelsEntry{
		"":   {gen: 1, filledAt: base.Add(-time.Minute)},
		"/a": {gen: 1, filledAt: base},
	}
	evictOldestLaunchModelsEntry(entries)
	if _, ok := entries[""]; ok {
		t.Fatal("the unscoped key was not evicted as the oldest")
	}
	if _, ok := entries["/a"]; !ok {
		t.Fatal("the newer key was evicted")
	}
}

// TestWarmLaunchModelsUsesTheConfiguredLoader: the startup warm goes through
// WebConfig.LaunchModels, so an embedder's own loader is warmed rather than
// bypassed by a direct evenerLaunchModelList call.
func TestWarmLaunchModelsUsesTheConfiguredLoader(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)
	inner := web.cfg.LaunchModels
	var calls atomic.Int32
	web.cfg.LaunchModels = func(ctx context.Context, workingDir string) (appwire.ModelListResponse, error) {
		calls.Add(1)
		return inner(ctx, workingDir)
	}

	web.warmLaunchModels(context.Background())
	if got := calls.Load(); got != 1 {
		t.Fatalf("the warm called the configured loader %d times, want 1 (it bypassed it)", got)
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("the warm did not fill the cache through the loader: launch contract called %d times", got)
	}
}

// TestWaitLaunchRefreshesAwaitsInFlightRefresh: a request-triggered refresh is
// tracked, so runMain's shutdown can wait for it instead of returning while its
// evener launch-check child is still running.
func TestWaitLaunchRefreshesAwaitsInFlightRefresh(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	spawner := &countLaunchContractSpawner{modelsFn: func(call int, _ string) appwire.ModelListResponse {
		// Only the refresh (call 2) blocks; the initial cold load must return so
		// the test can retire the entry and trigger the refresh.
		if call >= 2 {
			<-release
		}
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	reg := newBumpableProviderRegistry(t)
	web := &WebServer{
		cfg:          hubcore.WebConfig{Registry: reg, Spawner: spawner},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.Background(),
	}

	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels: %v", err)
	}
	if err := reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := web.fetchLaunchModels(context.Background(), ""); err != nil {
		t.Fatalf("fetchLaunchModels after bump: %v", err)
	}
	waitFor(t, func() bool { return spawner.callCount() == 2 }, "the refresh never started")

	waited := make(chan struct{})
	go func() {
		web.waitLaunchRefreshes()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("waitLaunchRefreshes returned while the refresh was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("waitLaunchRefreshes did not return after the refresh finished")
	}
}

// TestStartLaunchRefreshRefusesAfterTheShutdownGate: runMain's defer order
// awaits refreshes before the AppWire server is drained, so a still-open socket
// can serve a model/list during shutdown. The gate must refuse that Add rather
// than mutate the group being Waited on, and must not leave the refresh slot
// claimed when it refuses.
func TestStartLaunchRefreshRefusesAfterTheShutdownGate(t *testing.T) {
	t.Parallel()
	web := &WebServer{
		cfg:          hubcore.WebConfig{},
		launchModels: &launchModelsCache{entries: map[string]*launchModelsEntry{}, refreshing: map[string]bool{}},
		lifetime:     context.Background(),
	}
	web.waitLaunchRefreshes() // nothing in flight: closes the gate and returns

	web.launchModels.refreshing[""] = true
	if web.startLaunchRefresh("", 1, appwire.ModelListResponse{}) {
		t.Fatal("startLaunchRefresh accepted a refresh after the shutdown gate closed")
	}
	if web.launchModels.refreshing[""] {
		t.Fatal("a refused refresh left its slot claimed")
	}
}

// TestHubModelListServesLaunchContractFromCache pins that the model/list RPC
// the picker calls goes through the cache, so opening a picker twice does not
// spawn two launch checks.
func TestHubModelListServesLaunchContractFromCache(t *testing.T) {
	t.Parallel()
	spawner := &countLaunchContractSpawner{modelsFn: func(int, string) appwire.ModelListResponse {
		return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.5"}}}
	}}
	web := newLaunchModelsTestWeb(t, spawner, true)
	for range 2 {
		resp, err := hubModelList(context.Background(), web.cfg, nil, appwire.ModelListParams{})
		if err != nil {
			t.Fatalf("hubModelList: %v", err)
		}
		if len(resp.Data) != 1 || resp.Data[0].Provider != "openai" || resp.Data[0].Model != "gpt-5.5" {
			t.Fatalf("models=%+v", resp.Data)
		}
	}
	if got := spawner.callCount(); got != 1 {
		t.Fatalf("launch contract called %d times for two model/list RPCs, want 1", got)
	}
}
