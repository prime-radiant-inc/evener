package responses

import (
	"context"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Codex advertises ultra alongside API efforts, but it is a client-side
// delegation preset. Evener must offer only the actual reasoning efforts.
func TestListModelsCodexOmitsDelegationPreset(t *testing.T) {
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

func TestListModelsPlatformAndCodexShapes(t *testing.T) {
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

// TestListModelsReadsGatewayReasoningLadder pins lunaroute's per-model effort
// ladder on the Responses listing — the protocol a lunaroute instance resolves
// to, so this is the path a live session's profile is built from. The gateway
// publishes the ladder only under client_compat.pi.thinkingLevelMap, and these
// models are absent from models.dev, so without this read the row's ladder is
// empty: the picker falls back to low/medium/high and the request builder drops
// the effort name.
func TestListModelsReadsGatewayReasoningLadder(t *testing.T) {
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
	capsOf := func(facts string) registry.Caps {
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
	if c := capsOf(`"capabilities":{"vision":true}`); !c.AcceptsModality("image") {
		t.Fatalf("capabilities.vision must advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(`"capabilities":{"vision":false}`); c.AcceptsModality("image") {
		t.Fatalf("vision:false must not advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(`"context_window":8192`); c.AcceptsModality("image") {
		t.Fatalf("a row with no modality facts must not advertise image input: %+v", c.InputModalities)
	}
	if c := capsOf(`"architecture":{"input_modalities":["text"]},"capabilities":{"vision":true}`); c.AcceptsModality("image") || len(c.InputModalities) != 1 {
		t.Fatalf("architecture.input_modalities must win over capabilities.vision: %+v", c.InputModalities)
	}
}
