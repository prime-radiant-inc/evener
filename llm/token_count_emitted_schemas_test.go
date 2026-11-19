package llm

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// schemaLosingCatalog is a catalog whose advertised schema is larger than the
// schema the route puts on the wire: every tool carries fields its protocol
// strips before sending. The advertised bytes are therefore an overcharge, which
// a cap between the two readings turns into a refusal of a request that fits.
func schemaLosingCatalog(n int, params func() map[string]any) []ToolDefinition {
	defs := make([]ToolDefinition, n)
	for i := range defs {
		defs[i] = ToolDefinition{
			Name:        "probe_" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Description: "a tool that carries fields its protocol strips",
			Parameters:  params(),
		}
	}
	return defs
}

// jsonSchemaSuffix is the schema tail Google rejects: JSON Schema document
// metadata and additionalProperties have no field in its Schema proto, so
// sanitizeGeminiSchema drops them and the wire is smaller than the catalog.
func jsonSchemaSuffix() map[string]any {
	return map[string]any{
		"type":                 "object",
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "the file to read",
				"$comment":    "documented for readers of the schema",
			},
		},
		"required": []string{"path"},
	}
}

// topLevelCombinatorSchema is the shape Anthropic rejects at the root of an
// input_schema: the builder strips anyOf/oneOf/allOf entirely, so the rest of
// the schema is what dispatches.
func topLevelCombinatorSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"required": []string{"path"}},
			map[string]any{"required": []string{"url"}},
		},
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "the file to read"},
		},
	}
}

// A route that strips fields from a tool schema must be priced by the schema it
// emits, not by the one the caller advertised.
//
// Both builders rewrite the parameter schema before sending it: Google drops the
// JSON-Schema document metadata and additionalProperties its Schema proto has no
// field for, and Anthropic strips a top-level combinator its endpoint rejects.
// The advertised list therefore measures more than the request that dispatches,
// and a cap between the two readings refuses a request that fits. The oracle is
// the wire body itself: the price has to be the length of the definitions the
// server received.
func TestThePriceIsTheSchemaTheProtocolEmits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		schemaKey string
		params    func() map[string]any
	}{
		{
			name:      "google strips schema document metadata",
			protocol:  registry.ProtocolGoogle,
			schemaKey: "parameters",
			params:    jsonSchemaSuffix,
		},
		{
			name:      "anthropic strips a top-level combinator",
			protocol:  registry.ProtocolAnthropic,
			schemaKey: "input_schema",
			params:    topLevelCombinatorSchema,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defs := schemaLosingCatalog(24, tc.params)
			req := Request{
				Provider:       "emitted-price",
				Model:          "model",
				Messages:       []Message{User("x")},
				Tools:          defs,
				ToolsJSONBytes: ToolsJSONBytes(defs),
			}

			// The emitted list, read from the wire, is what the request costs.
			ample, ampleRes, ampleBodies := declaredEmissionRoute(t, 1_000_000, tc.protocol)
			if _, err := ample.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete under an ample cap: %v", err)
			}
			dispatched := ampleBodies()
			if len(dispatched) != 1 {
				t.Fatalf("requests on the wire = %d, want 1", len(dispatched))
			}
			wire := ToolsJSONBytes(wireToolDefs(t, dispatched[0], tc.schemaKey))
			advertised := ToolsJSONBytes(defs)
			if wire >= advertised {
				t.Fatalf("fixture does not distinguish the prices: wire %d bytes, advertised %d: the protocol "+
					"has to strip fields for this case to prove anything", wire, advertised)
			}

			priced := PriceToolSchemas(req, ampleRes)
			if priced.ToolsJSONBytes != wire {
				t.Fatalf("preflight price = %d bytes, want the wire list's %d (the caller advertised %d): the "+
					"builder strips fields from this schema, so the advertised list is not what dispatches",
					priced.ToolsJSONBytes, wire, advertised)
			}
			// The local reading a caller sizes the request with is the wire
			// reading, measured over the wire list itself.
			wireReq := req
			wireReq.ToolsJSONBytes = wire
			lower := EstimateInputTokensForResolved(ampleRes, wireReq).Tokens
			if got := EstimateInputTokensForResolved(ampleRes, priced).Tokens; got != lower {
				t.Fatalf("preflight reading = %d input tokens, want the wire list's %d", got, lower)
			}

			// A cap between the advertised reading and the emitted one: the
			// request fits, so admission must let it through.
			upper := EstimateInputTokensForResolved(ampleRes, req).Tokens
			if upper <= lower {
				t.Fatalf("fixture does not distinguish the readings: wire=%d advertised=%d", lower, upper)
			}
			limit := capBetweenPrices(t, lower, upper)
			c, _, bodies := declaredEmissionRoute(t, limit, tc.protocol)
			if _, err := c.Complete(context.Background(), req); err != nil {
				if _, budget := errors.AsType[*ContextBudgetError](err); budget {
					t.Fatalf("Complete under a cap of %d refused a request that dispatches %d input tokens: the "+
						"advertised reading %d prices a schema the wire never carries", limit, lower, upper)
				}
				t.Fatalf("Complete under a cap of %d: %v", limit, err)
			}
			if got := len(bodies()); got != 1 {
				t.Fatalf("requests on the wire = %d, want 1", got)
			}
		})
	}
}

// A route that strips nothing must keep the price it already had.
//
// The derivers report an unchanged list for a schema every field of which
// survives emission, so the count the request arrived with is reused. The
// control is the same catalog through the same route, so a price that moved for
// every request would show here.
func TestAnUnstrippedSchemaKeepsTheAdvertisedPrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		protocol string
	}{
		{"google", registry.ProtocolGoogle},
		{"anthropic", registry.ProtocolAnthropic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defs := []ToolDefinition{{
				Name:        "read_file",
				Description: "read a file",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "the file to read"},
					},
					"required": []string{"path"},
				},
			}}
			advertised := ToolsJSONBytes(defs)
			req := Request{Tools: defs, ToolsJSONBytes: advertised}
			res := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
			res.Protocol = tc.protocol

			priced := PriceToolSchemas(req, res)
			if priced.ToolsJSONBytes != advertised {
				t.Fatalf("priced count = %d, want the advertised %d: this schema survives emission byte for "+
					"byte, so the count it arrived with describes the wire", priced.ToolsJSONBytes, advertised)
			}
		})
	}
}
