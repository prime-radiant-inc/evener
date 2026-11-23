package responses

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/providertest"
	"primeradiant.com/evener/llm/registry"
)

// Codex advertises ultra alongside API efforts, but it is a client-side
// delegation preset. Evener must offer only the actual reasoning efforts.
func TestListModelsCodexOmitsDelegationPreset(t *testing.T) {
	t.Parallel()
	for _, defaultEffort := range []string{"medium", "ultra"} {
		t.Run(defaultEffort, func(t *testing.T) {
			srv, _ := server(t, 200, `{"models":[{"slug":"gpt-6-astra","context_window":272000,"max_context_window":872000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}],"default_reasoning_level":"`+defaultEffort+`"}]}`)
			rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].ID != "gpt-6-astra" {
				t.Fatalf("rows = %+v", rows)
			}
			caps := rows[0].Caps
			if !slices.Equal(caps.EffortValues, []string{"low", "medium", "high", "xhigh", "max"}) {
				t.Fatalf("selectable efforts = %v", caps.EffortValues)
			}
			wantDefault := defaultEffort
			if defaultEffort == "ultra" {
				wantDefault = ""
			}
			if registry.StringValue(caps.DefaultEffort) != wantDefault || caps.ContextWindow == nil || *caps.ContextWindow != 272000 {
				t.Fatalf("model facts = %+v", caps)
			}
		})
	}
}

// inklingModels is the /v1/models body a local OpenAI-compatible server
// publishes: the reasoning fact is a plain boolean, the effort ladder and
// its default arrive in the Codex spellings as strings, and the output cap
// lives under a limits object. Captured verbatim from the live listing; the
// data[] reader used to decode the row without any of those facts, because
// it never modeled the fields at all.
const inklingModels = `{"object":"list","data":[{"object":"model","id":"Inkling-2-Mini","created":1795417889,"owned_by":"local","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"input_modalities":["text"],"output_modalities":["text"],"reasoning":true,"supported_reasoning_levels":["none","minimal","low","medium","high","xhigh","max"],"default_reasoning_level":"high","context_length":262144,"limits":{"max_output_tokens":262144},"loom":{"kind":"text","endpoint":"/v1/chat/completions","tokenize":true,"input_tokens":true,"variants":{"selected":1,"count":1},"incarnation":1,"registration":"ready","device":"gpu","pool":"weights"}}]}`

// TestListModelsLoadsInklingMiniRow pins the full decode of that listing
// through the data[] arm: a gateway row whose reasoning facts arrive in
// spellings the public-OpenAI reader never modeled must load with its
// ladder, default effort, output cap and window intact.
func TestListModelsLoadsInklingMiniRow(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, inklingModels)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "Inkling-2-Mini" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true from the boolean verdict", c.Reasoning)
	}
	if got, want := c.EffortValues, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
	if got := registry.StringValue(c.DefaultEffort); got != "high" {
		t.Fatalf("DefaultEffort = %q, want high", got)
	}
	if c.ContextWindow == nil || *c.ContextWindow != 262144 {
		t.Fatalf("ContextWindow = %v, want 262144 from context_length", c.ContextWindow)
	}
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want 262144 from limits.max_output_tokens", c.MaxOutputTokens)
	}
	if got, want := c.InputModalities, []string{"text"}; !slices.Equal(got, want) {
		t.Fatalf("InputModalities = %v, want %v", got, want)
	}
}

// TestListModelsCodexRowAcceptsStringLevels pins the models[] arm's
// tolerance: a server may publish supported_reasoning_levels as plain
// strings where the Codex backend publishes effort objects, and that
// spelling must not fail the listing the way a strict object decode once
// did.
func TestListModelsCodexRowAcceptsStringLevels(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"models":[{"id":"inkling-mini","supported_reasoning_levels":["low","high"],"default_reasoning_level":"high","context_window":262144}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "inkling-mini" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if got, want := c.EffortValues, []string{"low", "high"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
	if got := registry.StringValue(c.DefaultEffort); got != "high" {
		t.Fatalf("DefaultEffort = %q, want high", got)
	}
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true from the ladder", c.Reasoning)
	}
}

// TestListModelsIsolatesUndecodableRows pins the listing's row isolation
// across both arms: a row whose shape the reader cannot decode is dropped
// while its siblings load, so one alien row cannot fail the whole live
// refresh. A row with no id stays silently skipped as before.
func TestListModelsIsolatesUndecodableRows(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[
	 {"id":"alien-row","context_window":"big"},
	 {"object":"model","created":1},
	 {"id":"healthy-row","context_window":8192}
	],"models":[
	 {"context_window":"big"},
	 {"slug":"codex-healthy","context_window":4096}
	]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	if !ids["healthy-row"] || !ids["codex-healthy"] || ids["alien-row"] || len(rows) != 2 {
		t.Fatalf("rows = %+v, want healthy-row and codex-healthy only", rows)
	}
}

// TestListModelsFailsWhenNoRowDecodes keeps the tolerant decode honest: a
// body whose rows are all undecodable is an alien server, and the refresh
// must fail with the row's decode error rather than silently succeed with
// no models.
func TestListModelsFailsWhenNoRowDecodes(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[
	 {"id":"a","context_window":"big"},
	 {"id":"b","max_input_tokens":"lots"}
	]}`)
	_, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err == nil {
		t.Fatal("a listing with no decodable row must fail")
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Fatalf("err = %v, want it to wrap the row's decode error", err)
	}
}

func TestListModelsPlatformAndCodexShapes(t *testing.T) {
	t.Parallel()
	srv, got := server(t, 200, `{"data":[{"id":"gpt-5.5","context_window":1050000,"max_input_tokens":922000,"max_output_tokens":128000},{"id":"text-embedding-3-large"}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil || got.path != "/v1/models" {
		t.Fatalf("err = %v path = %s", err, got.path)
	}
	if len(rows) != 1 || rows[0].ID != "gpt-5.5" || *rows[0].Caps.ContextWindow != 1_050_000 || *rows[0].Caps.MaxInputTokens != 922_000 || *rows[0].Caps.MaxOutputTokens != 128_000 {
		t.Fatalf("rows = %+v", rows)
	}

	srv2, got2 := server(t, 200, `{"models":[{"slug":"gpt-5.6-sol","display_name":"GPT-5.6","context_window":1050000,"max_input_tokens":922000,"max_output_tokens":128000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"default_reasoning_level":"high","input_modalities":["text","image"]}]}`)
	res := liveRes(srv2, nil)
	res.Transport.ModelsEndpoint = "/models?client_version=0.0.0"
	rows, err = (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res)
	if err != nil || got2.path != "/v1/models?client_version=0.0.0" {
		t.Fatalf("err = %v path = %s", err, got2.path)
	}
	c := rows[0].Caps
	if rows[0].ID != "gpt-5.6-sol" || *c.ContextWindow != 1_050_000 || *c.MaxInputTokens != 922_000 || len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || !*c.Reasoning || len(c.InputModalities) != 2 {
		t.Fatalf("codex row = %+v", rows[0])
	}
	// The Codex backend is the one listing that states what the model runs
	// at when the request carries no effort; dropping it costs those models
	// their stated default (spec §7.4).
	if registry.StringValue(c.DefaultEffort) != "high" {
		t.Fatalf("codex default_reasoning_level = %v, want high", c.DefaultEffort)
	}
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res); !errors.Is(err, llm.ErrModelListingUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestListModelsCodexClientVersion(t *testing.T) {
	// Not parallel: this registers the package-wide test Codex authenticator.
	providertest.RegisterStreamingRequiredAuth(t, registry.AuthOAuthOpenAICodex, providertest.StreamingRequiredAuth{})
	for _, tc := range []struct {
		name     string
		auth     string
		endpoint string
		version  string
		wantURI  string
	}{
		{name: "default", auth: registry.AuthOAuthOpenAICodex, endpoint: "/models", version: "0.888.0", wantURI: "/v1/models?client_version=0.888.0"},
		{name: "provider override preserves other query values", auth: registry.AuthOAuthOpenAICodex, endpoint: "/models?region=east&client_version=0.156.1", version: "0.166.0", wantURI: "/v1/models?client_version=0.166.0&region=east"},
		{name: "other Responses providers unchanged", auth: registry.AuthBearer, endpoint: "/models", version: "0.166.0", wantURI: "/v1/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := server(t, 200, `{"models":[{"slug":"gpt-6.2-sol"}]}`)
			res := liveRes(srv, nil)
			res.Transport.Auth = tc.auth
			res.Transport.ModelsEndpoint = tc.endpoint
			res.Transport.CodexClientVersion = tc.version
			if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); err != nil {
				t.Fatal(err)
			}
			if got.path != tc.wantURI {
				t.Fatalf("request URI = %q, want %q", got.path, tc.wantURI)
			}
		})
	}
}

// TestListModelsReadsGatewayReasoningLadder pins lunaroute's per-model effort
// ladder on the Responses listing — the protocol a lunaroute instance resolves
// to, so this is the path a live session's profile is built from. The gateway
// publishes the ladder only under client_compat.pi.thinkingLevelMap, and these
// models are absent from models.dev, so without this read the row's ladder is
// empty: the picker falls back to low/medium/high and the request builder drops
// the effort name.
func TestListModelsReadsGatewayReasoningLadder(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[{"id":"deepseek-4.1-flash","context_window":1048576,"max_output_tokens":262144,"capabilities":{"reasoning":true,"tools":true,"vision":true},"client_compat":{"pi":{"thinkingFormat":"openai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"medium","minimal":null,"off":null,"xhigh":"xhigh"}}}}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	caps := rows[0].Caps
	if want := []string{"low", "medium", "high", "xhigh", "max"}; !slices.Equal(caps.EffortValues, want) {
		t.Fatalf("EffortValues = %v, want %v", caps.EffortValues, want)
	}
	if caps.Reasoning == nil || !*caps.Reasoning {
		t.Fatalf("Reasoning = %v, want true from capabilities.reasoning", caps.Reasoning)
	}
	if caps.ContextWindow == nil || *caps.ContextWindow != 1048576 || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 262144 {
		t.Fatalf("limits = %+v", caps)
	}
}

// TestListModelsGatewayReasoningFollowsTheCapability pins that a gateway
// stating reasoning = false leaves its row non-reasoning even when a stale map
// rides along.
func TestListModelsGatewayReasoningFollowsTheCapability(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[{"id":"no-reasoning","capabilities":{"reasoning":false},"client_compat":{"pi":{"thinkingLevelMap":{"low":"low","high":"high"}}}}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if caps := rows[0].Caps; caps.Reasoning == nil || *caps.Reasoning {
		t.Fatalf("Reasoning = %v, want an explicit false", caps.Reasoning)
	}
}

// TestListModelsPlatformVisionCapability pins that a platform /models row's
// advertised modality facts reach the row: capabilities.vision yields image
// input, and architecture.input_modalities wins when both are present. Without
// this a Responses-recorded gateway that publishes vision under capabilities
// resolves image-blind (#4508).
func TestListModelsPlatformVisionCapability(t *testing.T) {
	t.Parallel()
	if c := capsOf(t, `"capabilities":{"vision":true}`); !c.AcceptsModality("image") {
		t.Fatalf("capabilities.vision must advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(t, `"capabilities":{"vision":false}`); c.AcceptsModality("image") {
		t.Fatalf("vision:false must not advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(t, `"context_window":8192`); c.AcceptsModality("image") {
		t.Fatalf("a row with no modality facts must not advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(t, `"architecture":{"input_modalities":["text"]},"capabilities":{"vision":true}`); c.AcceptsModality("image") || len(c.InputModalities) != 1 {
		t.Fatalf("architecture.input_modalities must win over capabilities.vision: %+v", c.InputModalities)
	}
}

// capsOf serves one /models row body and returns that row's caps.
func capsOf(t *testing.T, facts string) registry.Caps {
	t.Helper()
	srv, _ := server(t, 200, `{"data":[{"id":"gateway-model",`+facts+`}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "gateway-model" {
		t.Fatalf("rows = %+v", rows)
	}
	return rows[0].Caps
}

// TestListModelsReadsGatewayToolsCapability pins that a gateway's per-model
// capabilities.tools reaches the row. The Responses listing carries no
// supported_parameters list to infer tools from, so without this read a
// gateway's tools verdict never reaches the resolved row.
func TestListModelsReadsGatewayToolsCapability(t *testing.T) {
	t.Parallel()
	if c := capsOf(t, `"capabilities":{"tools":true}`); c.Tools == nil || !*c.Tools {
		t.Fatalf("capabilities.tools must advertise tool support: %+v", c.Tools)
	}
	if c := capsOf(t, `"capabilities":{"tools":false}`); c.Tools == nil || *c.Tools {
		t.Fatalf("tools:false must advertise no tool support: %+v", c.Tools)
	}
	if c := capsOf(t, `"context_window":8192`); c.Tools != nil {
		t.Fatalf("a row with no tool facts must leave Tools unset: %+v", c.Tools)
	}
}
