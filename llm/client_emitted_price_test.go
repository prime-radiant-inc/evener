package llm

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A count produced for one target is not a count for another, and that holds
// even when the second target has no deriver at all.
//
// A caller may price a request for a strict Responses row and then send the same
// request through a route whose protocol does not derive tool schemas. That
// route marshals the definitions it was handed, so the list's own measurement is
// its price. Leaving the Responses charge standing reports an input reading the
// route never carries: an inflated local count, and an input cap between the two
// refuses a request that fits.
func TestARouteWithNoDeriverRecoversTheBaselinePrice(t *testing.T) {
	t.Parallel()
	// The stand-in has no EmitToolSchemas method, which is what makes it the
	// case under test: every registered protocol derives, so this row is
	// registered here rather than resolved from a real instance.
	const protocolID = "test-proto-no-deriver"
	RegisterProtocol(stubProtocol{id: protocolID})

	strictRow := registry.Resolved{Instance: "prepared-budget", ModelID: "model", WireID: "model"}
	strictRow.Protocol = registry.ProtocolOpenAIResponses
	strictRow.Caps.StrictTools = new(true)

	defs := []ToolDefinition{{Name: "parameterless"}}
	baseline := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: baseline,
	}
	forResponses := priceEmittedToolSchemas(req, strictRow)
	if forResponses.ToolsJSONBytes == baseline {
		t.Fatalf("fixture does not distinguish the targets: the Responses projection left the count at the "+
			"baseline %d, so retargeting cannot show a difference", baseline)
	}
	if _, derives := ProtocolFor(protocolID); !derives {
		t.Fatalf("the stand-in protocol %q is not registered", protocolID)
	}

	noDeriver := registry.Resolved{Instance: "prepared-budget", ModelID: "model", WireID: "model"}
	noDeriver.Protocol = protocolID
	got := priceEmittedToolSchemas(forResponses, noDeriver)
	if got.ToolsJSONBytes != baseline {
		t.Fatalf("route without a deriver priced the catalog at %d, want the baseline %d: its builder sends the "+
			"definitions it was handed, so the Responses charge describes a list this route never emits",
			got.ToolsJSONBytes, baseline)
	}
}

// An override-served route recovers the baseline price too.
//
// An override marshals the definition it was handed, so its received count is
// its wire count. A request priced for a built-in route and then counted on an
// override keeps that route's charge, because an override is not a protocol and
// never reaches a deriver to correct it. The inflation is the same one the
// no-deriver case describes: an unrelated target's price standing in for this
// one's.
func TestAnOverrideServedCountDropsThePreviousTargetsPrice(t *testing.T) {
	t.Parallel()
	adapter := &countingAdapter{}
	c, res := preparedBudgetClient(t, adapter, 1_000_000)
	// The client resolves the instance to a built-in row, so the request under
	// test is priced for that row and then handed to the registered override.
	strictRow := res
	strictRow.Protocol = registry.ProtocolOpenAIResponses
	strictRow.Caps.StrictTools = new(true)

	defs := []ToolDefinition{{Name: "parameterless"}}
	baseline := ToolsJSONBytes(defs)
	req := Request{
		Provider: "prepared-budget",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	forResponses := priceEmittedToolSchemas(req, strictRow)
	if forResponses.ToolsJSONBytes == baseline {
		t.Fatalf("fixture does not distinguish the targets: the Responses projection left the count at the "+
			"baseline %d", baseline)
	}

	got, err := c.CountInputTokens(context.Background(), forResponses)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	// The override's exact counter answers with its own number, so the local
	// reading is not what comes back; what has to hold is that the request the
	// counter is handed carries no other target's charge.
	if len(adapter.counted) != 1 {
		t.Fatalf("counter ran %d time(s), want once", len(adapter.counted))
	}
	if adapter.counted[0].ToolsJSONBytes != baseline {
		t.Fatalf("override counter saw ToolsJSONBytes = %d, want the baseline %d: an override marshals the "+
			"definitions it is handed, so another route's emitted count describes a list it never sends",
			adapter.counted[0].ToolsJSONBytes, baseline)
	}
	if got.Tokens == 0 {
		t.Fatal("CountInputTokens returned no count")
	}
}

// The projection reports a required-list reorder as costing no bytes, so a
// pricing call reuses the count it already holds instead of encoding the catalog
// twice to discover that the two encodings are the same length.
//
// The counter is on the encoder rather than on the price, because both the right
// answer and the wrong one are the same number: only the number of marshals
// tells the two apart.
func TestAReorderOnlyProjectionDoesNotMarshalTheCatalog(t *testing.T) {
	t.Parallel()
	var calls int32
	// The projection sorts every object's required list, so a schema that
	// already names exactly those properties is emitted as a permutation of
	// itself. The counter rides in a property's description, which the projection
	// copies through without reading, so every marshal of the catalog increments
	// it. An unreadable *schema* cannot carry it: the gate refuses a properties
	// map whose child it cannot decode, so the catalog would be sent non-strict
	// and never projected at all.
	defs := []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
				"note":    map[string]any{"type": "string", "description": countedSchemaValue{calls: &calls}},
			},
			// Deliberately unsorted: the projection sorts it, so the emitted
			// schema differs structurally from this one while serializing to the
			// same length. A sorted list here would make DeepEqual short-circuit
			// and the comparison under test would never run.
			"required": []string{"path", "note", "content"},
		},
	}}
	_, res := budgetClientOnRoute(t, &preparedBudgetAdapter{}, 1_000_000, registry.ProtocolOpenAIResponses,
		registry.Caps{StrictTools: new(true)})

	// The count is already exact: this catalog's projection reorders its required
	// list and changes nothing else.
	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: ToolsJSONBytes(defs),
	}
	before := atomic.LoadInt32(&calls)
	got := priceEmittedToolSchemas(req, res)
	marshals := atomic.LoadInt32(&calls) - before

	if got.ToolsJSONBytes != req.ToolsJSONBytes {
		t.Fatalf("priced count = %d, want the count it arrived with, %d: the projection only reorders a "+
			"required list here, so the bytes are the same", got.ToolsJSONBytes, req.ToolsJSONBytes)
	}
	if marshals != 0 {
		t.Fatalf("pricing the catalog marshaled it %d time(s), want 0: the projection answers the byte-length "+
			"question itself, and a destination that re-encodes to confirm a reorder costs the whole catalog "+
			"on every pricing call", marshals)
	}

	// The control: a projection that really does change the bytes is still
	// measured, so a comparator that answered "same" for everything cannot pass
	// above. The catalog keeps the counter and gains a typeless closed value set,
	// which the projection states a type on.
	changing := []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note": map[string]any{"type": "string", "description": countedSchemaValue{calls: &calls}},
				"mode": map[string]any{"const": "fixed"},
			},
			"required": []string{"note", "mode"},
		},
	}}
	changingBefore := atomic.LoadInt32(&calls)
	if priced := priceEmittedToolSchemas(Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          changing,
		ToolsJSONBytes: ToolsJSONBytes(changing),
	}, res); priced.ToolsJSONBytes == ToolsJSONBytes(changing) {
		t.Fatal("a catalog whose projection states a type was priced as unchanged")
	}
	if atomic.LoadInt32(&calls) == changingBefore {
		t.Fatal("a catalog whose projection changes its bytes was never measured")
	}
}
