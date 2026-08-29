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
