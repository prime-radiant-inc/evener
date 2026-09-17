package chatcompletions

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const openRouterModels = `{"data":[
 {"id":"anthropic/claude-opus-5","context_length":1000000,"supported_parameters":["tools","reasoning","temperature"],"architecture":{"input_modalities":["text","image"]},"reasoning":{"mandatory":true,"supported_efforts":["low","high","high"]},"pricing":{"prompt":"0.000005","completion":"0.000025"},"top_provider":{"max_completion_tokens":128000}},
 {"id":"plain/model","context_length":8192}
]}`

func TestListModelsMapsAdvertisedFacts(t *testing.T) {
	srv, got := server(t, 200, openRouterModels)
	res := liveRes(srv, nil)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/models" || got.header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
	if len(rows) != 2 || rows[0].ID != "anthropic/claude-opus-5" || rows[1].ID != "plain/model" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if *c.ContextWindow != 1000000 || *c.MaxOutputTokens != 128000 || !*c.Tools || !*c.Reasoning || !*c.ThinkingAlwaysOn {
		t.Fatalf("caps = %+v", c)
	}
	if len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || len(c.InputModalities) != 2 || c.Cost == nil || c.Cost.Input != 5 || c.Cost.Output != 25 {
		t.Fatalf("caps = %+v", c)
	}
	plain := rows[1].Caps
	if *plain.ContextWindow != 8192 || plain.Tools != nil || plain.Reasoning != nil || plain.Cost != nil {
		t.Fatalf("unadvertised facts must stay nil: %+v", plain)
	}
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); !errors.Is(err, llm.ErrModelListingUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

// TestListModelsRejectsNonFiniteCost covers the review finding on
// perTokenCostToPerMillion: strconv.ParseFloat accepts "NaN"/"Inf" without
// error, and neither is < 0, so a non-finite advertised price must be
// rejected explicitly or Caps.Cost ends up NaN/Inf, which then fails
// json.Marshal for the whole listing. Non-finite prompt or completion
// pricing must drop Cost to nil while every other advertised fact on that
// row still maps.
func TestListModelsRejectsNonFiniteCost(t *testing.T) {
	body := `{"data":[
 {"id":"nan-prompt","context_length":4096,"pricing":{"prompt":"NaN","completion":"0.000002"}},
 {"id":"inf-completion","context_length":8192,"pricing":{"prompt":"0.000001","completion":"Inf"}}
]}`
	srv, _ := server(t, 200, body)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]registry.Model{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	nanRow, ok := byID["nan-prompt"]
	if !ok || nanRow.Caps.Cost != nil || nanRow.Caps.ContextWindow == nil || *nanRow.Caps.ContextWindow != 4096 {
		t.Fatalf("nan-prompt row caps = %+v, want Cost=nil ContextWindow=4096", nanRow.Caps)
	}
	infRow, ok := byID["inf-completion"]
	if !ok || infRow.Caps.Cost != nil || infRow.Caps.ContextWindow == nil || *infRow.Caps.ContextWindow != 8192 {
		t.Fatalf("inf-completion row caps = %+v, want Cost=nil ContextWindow=8192", infRow.Caps)
	}
}

// TestListModelsNormalizesSupportedParameters pins the case- and
// whitespace-insensitive match openaicompat used: OpenRouter's
// supported_parameters are matched with strings.EqualFold on the trimmed
// value, so " Tools " still advertises tool support.
func TestListModelsNormalizesSupportedParameters(t *testing.T) {
	body := `{"data":[
 {"id":"padded/tools","supported_parameters":["Tools ","  Reasoning_Effort"]}
]}`
	srv, _ := server(t, 200, body)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if c.Tools == nil || !*c.Tools || c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("caps = %+v, want Tools and Reasoning true", c)
	}
}

// listRow runs the live /models reader against a single-row body and returns
// that row's caps.
func listRow(t *testing.T, row string) registry.Caps {
	t.Helper()
	srv, _ := server(t, 200, `{"data":[`+row+`]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	return rows[0].Caps
}

// TestListModelsMapsGatewayTopLevelLimits pins the lunaroute-shaped row Evener
// depends on: the gateway advertises its limits at the top level, with no
// top_provider object and no supported_parameters. Dropping
// max_input_tokens/max_output_tokens here left Profile.MaxOutputTokens() at 0,
// so llm.ApplyTokenBudget fell back to asking for the whole remaining window.
func TestListModelsMapsGatewayTopLevelLimits(t *testing.T) {
	c := listRow(t, `{"id":"deepseek-4.1-flash","object":"model","created":0,"owned_by":"lunaroute","context_window":1048576,"context_length":1048576,"max_input_tokens":1048576,"max_output_tokens":262144,"max_completion_tokens":262144,"capabilities":{"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"streaming":true}}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1048576 {
		t.Fatalf("ContextWindow = %v, want 1048576", c.ContextWindow)
	}
	if c.MaxInputTokens == nil || *c.MaxInputTokens != 1048576 {
		t.Fatalf("MaxInputTokens = %v, want 1048576", c.MaxInputTokens)
	}
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want 262144", c.MaxOutputTokens)
	}
}

// TestListModelsPrefersContextLengthOverContextWindow keeps OpenRouter's
// ordering: context_length wins when both spellings are present.
func TestListModelsPrefersContextLengthOverContextWindow(t *testing.T) {
	c := listRow(t, `{"id":"both-windows","context_length":1000000,"context_window":1048576}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1000000 {
		t.Fatalf("ContextWindow = %v, want context_length 1000000", c.ContextWindow)
	}
}

// TestListModelsPrefersTopProviderMaxCompletionTokens keeps OpenRouter's
// precedence: the per-provider cap beats every top-level output spelling.
func TestListModelsPrefersTopProviderMaxCompletionTokens(t *testing.T) {
	c := listRow(t, `{"id":"provider-cap","top_provider":{"max_completion_tokens":128000},"max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 128000 {
		t.Fatalf("MaxOutputTokens = %v, want top_provider 128000", c.MaxOutputTokens)
	}
}

// TestListModelsPrefersTopLevelMaxCompletionTokens pins the top-level order:
// max_completion_tokens before max_output_tokens.
func TestListModelsPrefersTopLevelMaxCompletionTokens(t *testing.T) {
	c := listRow(t, `{"id":"two-spellings","max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want max_completion_tokens 262144", c.MaxOutputTokens)
	}
}

// TestListModelsLeavesLimitsNilWhenUnadvertised pins that only positive values
// become caps: absent, zero and negative spellings all leave the pointer nil.
func TestListModelsLeavesLimitsNilWhenUnadvertised(t *testing.T) {
	for _, row := range []string{
		`{"id":"absent"}`,
		`{"id":"nonpositive","context_length":0,"context_window":-1,"max_input_tokens":0,"max_completion_tokens":0,"max_output_tokens":-5,"top_provider":{"max_completion_tokens":0}}`,
	} {
		c := listRow(t, row)
		if c.ContextWindow != nil || c.MaxInputTokens != nil || c.MaxOutputTokens != nil {
			t.Fatalf("%s: limits must stay nil: %+v", row, c)
		}
	}
}

// TestListModelsReadsContextWindowAlone pins the top-level context_window as a
// window source on its own. The other tests that touch it also set
// context_length, so without this one a regression that dropped the
// context_window field, or its use in row(), would still pass every test while
// a gateway publishing only that spelling lost its window: ApplyTokenBudget
// would then skip the total-context clamp and allocate output with no ceiling.
func TestListModelsReadsContextWindowAlone(t *testing.T) {
	c := listRow(t, `{"id":"window-only","context_window":1048576}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1048576 {
		t.Fatalf("ContextWindow = %v, want 1048576", c.ContextWindow)
	}
}
