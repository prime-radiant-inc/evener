package responses

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestListModelsPlatformAndCodexShapes(t *testing.T) {
	srv, got := server(t, 200, `{"data":[{"id":"gpt-5.5","context_window":400000,"max_output_tokens":128000},{"id":"text-embedding-3-large"}]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil || got.path != "/v1/models" {
		t.Fatalf("err = %v path = %s", err, got.path)
	}
	if len(rows) != 1 || rows[0].ID != "gpt-5.5" || *rows[0].Caps.ContextWindow != 400000 || *rows[0].Caps.MaxOutputTokens != 128000 {
		t.Fatalf("rows = %+v", rows)
	}

	srv2, got2 := server(t, 200, `{"models":[{"slug":"gpt-5.6-sol","display_name":"GPT-5.6","context_window":272000,"max_output_tokens":128000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"default_reasoning_level":"high","input_modalities":["text","image"]}]}`)
	res := liveRes(srv2, nil)
	res.Transport.ModelsEndpoint = "/models?client_version=0.0.0"
	rows, err = (&Protocol{Client: srv2.Client()}).ListModels(context.Background(), res)
	if err != nil || got2.path != "/v1/models?client_version=0.0.0" {
		t.Fatalf("err = %v path = %s", err, got2.path)
	}
	c := rows[0].Caps
	if rows[0].ID != "gpt-5.6-sol" || *c.ContextWindow != 272000 || len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || !*c.Reasoning || len(c.InputModalities) != 2 {
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
