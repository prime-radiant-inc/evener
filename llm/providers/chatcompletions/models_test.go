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
