package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// countedSchemaValue counts how often the encoder marshals it. A tool catalog
// that contains one can be measured twice without the second measurement
// changing the number, so a test that compares counts cannot tell reuse from
// re-measurement. Counting the marshals themselves can.
type countedSchemaValue struct{ calls *int32 }

func (c countedSchemaValue) MarshalJSON() ([]byte, error) {
	atomic.AddInt32(c.calls, 1)
	return []byte(`"counted"`), nil
}

// responsesRowOn returns a resolved row for the Responses protocol with the
// strict-tools capability as given. The row is built here rather than resolved
// from a live instance so the test does not depend on which instance happens to
// be configured.
func responsesRowOn(strict bool) registry.Resolved {
	row := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(strict)
	return row
}

// chatRowOn returns a resolved row for the Chat Completions protocol.
func chatRowOn(strict bool) registry.Resolved {
	row := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	row.Protocol = registry.ProtocolOpenAIChat
	row.Caps.StrictTools = new(strict)
	return row
}

// parameterlessCatalog is a list of tools that declare no parameters at all.
// The Responses builder substitutes a schema for each, so the emitted list is
// strictly larger than the advertised one.
func parameterlessCatalog(n int) []ToolDefinition {
	defs := make([]ToolDefinition, n)
	for i := range defs {
		defs[i] = ToolDefinition{Name: "probe_" + string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	return defs
}

// A parameterless tool's schema is substituted by the builder, so the price has
// to describe the substituted list.
//
// The definition omits Parameters entirely, which is what the caller wrote: the
// catalog's own count therefore carries no schema for it. The Responses builder
// supplies the open empty object, so the request that dispatches is larger than
// the one the catalog describes, and an input cap between the two prices admits
// a request over budget.
func TestThePriceIncludesTheSchemaTheBuilderSubstitutes(t *testing.T) {
	t.Parallel()
	res := responsesRowOn(false)
	defs := parameterlessCatalog(4)

	advertised := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: advertised,
	}

	priced := EffectiveToolsJSONBytes(req, res, false)
	if priced <= advertised {
		t.Fatalf("priced count = %d, want more than the catalog's %d: the builder supplies a schema for a "+
			"parameterless tool, and that is the list the wire carries", priced, advertised)
	}

	// The expected number is the builder's own answer, not a second expression
	// of the rule written here: pricing and emitting must be one decision.
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol registered for %q", res.Protocol)
	}
	deriver, ok := p.(ToolSchemaDeriver)
	if !ok {
		t.Fatalf("protocol %q does not derive tool schemas", res.Protocol)
	}
	emitted, changed := deriver.EmitToolSchemas(defs, res)
	if !changed {
		t.Fatal("the fixture does not make the builder change anything")
	}
	if want := ToolsJSONBytes(emitted); priced != want {
		t.Fatalf("priced count = %d, want the emitted list's %d", priced, want)
	}

	// The estimate bills that count, and the stamped request is left alone: a
	// price is a measurement, not a request edit.
	if got, want := EstimateInputTokensForRoute(res, req, false).Tokens,
		EstimateInputTokensForResolved(res, Request{
			Provider: req.Provider, Model: req.Model, Messages: req.Messages,
			Tools: defs, ToolsJSONBytes: priced,
		}).Tokens; got != want {
		t.Fatalf("route reading = %d, want the emitted list's %d", got, want)
	}
	if req.ToolsJSONBytes != advertised {
		t.Fatalf("the request's own stamp moved to %d, want the catalog's %d", req.ToolsJSONBytes, advertised)
	}
}

// A strict route sends different schema bytes, so the price has to follow it.
//
// A certified schema is projected without growing it, so the difference comes
// from a parameterless declaration: a strict route substitutes the closed empty
// object where every other route sends the open one. The same catalog on the
// same route without the capability is the control: there the builder leaves the
// definitions alone and the price must not move.
func TestAStrictProjectionChangesThePriceAndTheCapabilityDecidesIt(t *testing.T) {
	t.Parallel()
	// A tool prepared from a no-argument declaration. The capability is what
	// decides whether its schema is sent as the open empty object every route
	// emits or the closed one strict mode requires, so this is the difference the
	// price has to follow. A certified schema is deliberately not the fixture: the
	// gate projects it without adding bytes, which is what makes strict mode cost
	// nothing on a fully written schema.
	parameterless := ToolDefinition{Name: "current_time"}
	defs := []ToolDefinition{parameterless.WithNoParametersDeclared()}
	advertised := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: advertised,
	}

	strictPriced := EffectiveToolsJSONBytes(req, responsesRowOn(true), false)
	if strictPriced <= advertised {
		t.Fatalf("strict-capable price = %d, want more than the catalog's %d: the projection closes the "+
			"object and states a required list", strictPriced, advertised)
	}

	// The control. A route without the capability sends this schema unchanged,
	// so its price is the catalog's count and nothing moved.
	if got := EffectiveToolsJSONBytes(req, responsesRowOn(false), false); got != advertised {
		t.Fatalf("route without strict enforcement priced the catalog at %d, want the catalog's %d: its "+
			"builder sends this schema unchanged", got, advertised)
	}
}

// A route with no deriver prices the list it is handed, whatever the request
// carries.
//
// The stand-in has no EmitToolSchemas method, so there is no emitted list to ask
// about and the definitions the route receives are the definitions it sends.
// Every registered protocol derives except this one, so the row is registered
// here rather than resolved from a real instance.
func TestARouteWithNoDeriverPricesTheListItIsHanded(t *testing.T) {
	noDeriverRegistration.Do(func() {
		RegisterProtocol(stubProtocol{id: noDeriverProtocolID})
	})
	defs := []ToolDefinition{{Name: "parameterless"}}
	declared := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: declared,
	}
	noDeriver := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	noDeriver.Protocol = noDeriverProtocolID

	if got := EffectiveToolsJSONBytes(req, noDeriver, false); got != declared {
		t.Fatalf("route without a deriver priced the catalog at %d, want the list it is handed, %d", got, declared)
	}
	strictCharge := EffectiveToolsJSONBytes(req, responsesRowOn(true), false)
	if strictCharge == declared {
		t.Fatalf("fixture does not distinguish the routes: the strict projection left the count at %d", declared)
	}
	// An override marshals the definitions it is handed, so a stamp the caller
	// measured is the answer there; only a route with a builder re-derives.
	stamped := req
	stamped.ToolsJSONBytes = strictCharge
	if got := EffectiveToolsJSONBytes(stamped, noDeriver, true); got != strictCharge {
		t.Fatalf("an override's reading = %d, want the caller's stamp %d: an override marshals the definitions it "+
			"is handed", got, strictCharge)
	}
}

// noDeriverProtocolID names the deriver-less stand-in, and
// noDeriverRegistration keeps the package-wide protocol registry from panicking
// when the binary runs this test more than once in one process.
const noDeriverProtocolID = "test-proto-no-deriver"

var noDeriverRegistration sync.Once

// A price produced for one route is not a price for another.
//
// The same catalog on a strict Responses route and on a relaxed one emits
// different bytes. Because the price is computed from the route rather than
// carried on the request, retargeting a request cannot retain the first route's
// charge.
func TestARetargetedRequestPricesTheSecondRoutesSchemas(t *testing.T) {
	t.Parallel()
	defs := []ToolDefinition{{Name: "parameterless"}}
	advertised := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: advertised,
	}

	strict := EffectiveToolsJSONBytes(req, responsesRowOn(true), false)
	if strict == advertised {
		t.Fatalf("fixture does not distinguish the routes: the strict projection left the count at %d", advertised)
	}
	relaxed := EffectiveToolsJSONBytes(req, responsesRowOn(false), false)
	if relaxed == strict {
		t.Fatalf("relaxed price = %d, the same as the strict %d: each route prices its own emission", relaxed, strict)
	}
	// The relaxed route's substituted open object is smaller than the closed one
	// with its stated required list.
	if relaxed >= strict {
		t.Fatalf("relaxed price = %d, want less than the strict %d", relaxed, strict)
	}
}

// A projection that only reorders a required list must not re-marshal the
// catalog.
//
// The projection sorts every object's required list, so a schema that already
// names exactly those properties is emitted as a permutation of itself: the
// bytes are reordered, not resized. The counter is on the encoder rather than on
// the price, because both the right answer and the wrong one are the same
// number: only the number of marshals tells the two apart.
func TestAByteNeutralProjectionDoesNotMarshalTheCatalog(t *testing.T) {
	t.Parallel()
	var calls int32
	res := responsesRowOn(true)
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{
			name: "reordered required list",
			params: map[string]any{
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
		},
		{
			// A property whose name collides with the keyword the comparator
			// reads as a name list. Its value is a schema, so the name-set
			// reading cannot account for it, and refusing it there reported a
			// byte-neutral projection as a change.
			name: "property named required",
			params: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path":     map[string]any{"type": "string"},
					"required": map[string]any{"type": "boolean"},
					"note":     map[string]any{"type": "string", "description": countedSchemaValue{calls: &calls}},
				},
				"required": []string{"path", "required", "note"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defs := []ToolDefinition{{Name: "write_file", Parameters: tc.params}}
			req := Request{
				Provider:       "emitted-price",
				Model:          "model",
				Messages:       []Message{User("x")},
				Tools:          defs,
				ToolsJSONBytes: ToolsJSONBytes(defs),
			}
			before := atomic.LoadInt32(&calls)
			got := EffectiveToolsJSONBytes(req, res, false)
			marshals := atomic.LoadInt32(&calls) - before

			if got != req.ToolsJSONBytes {
				t.Fatalf("priced count = %d, want the count it arrived with, %d: the projection only reorders a "+
					"required list here, so the bytes are the same", got, req.ToolsJSONBytes)
			}
			if marshals != 0 {
				t.Fatalf("pricing the catalog marshaled it %d time(s), want 0: the projection answers the "+
					"byte-length question itself, and a destination that re-encodes to confirm a reorder costs "+
					"the whole catalog on every pricing call", marshals)
			}
		})
	}

	// The control: a projection that really does change the bytes is still
	// measured, so a comparator that answered "same" for everything cannot pass
	// above. This catalog is certified for strict mode and gains the type its
	// closed value set implies, which is a byte the projection adds, and it keeps
	// the counter.
	changing := []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"note": map[string]any{
					"description": countedSchemaValue{calls: &calls},
					"enum":        []any{"read", "write"},
				},
			},
			"required": []string{"note"},
		},
	}}
	advertisedChanging := ToolsJSONBytes(changing)
	changingBefore := atomic.LoadInt32(&calls)
	if priced := EffectiveToolsJSONBytes(Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          changing,
		ToolsJSONBytes: advertisedChanging,
	}, res, false); priced == advertisedChanging {
		t.Fatal("a catalog whose projection closes the object was priced as unchanged")
	}
	if atomic.LoadInt32(&calls) == changingBefore {
		t.Fatal("a catalog whose projection changes its bytes was never measured")
	}
}

// rawEscapedCatalog is a catalog whose single tool hands one subschema over as
// raw JSON that spells its text with \u escapes. Decoded, each escape is the
// two-byte UTF-8 character, so a builder that decodes the raw value puts fewer
// bytes on the wire than the caller's own list serializes to.
func rawEscapedCatalog() []ToolDefinition {
	return []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": json.RawMessage(`{"description":"` + strings.Repeat(`\u00e9`, 200) + `","type":"string"}`),
			},
			"required": []string{"target"},
		},
	}}
}

// responsesWireToolSchemas is the parameter schema of every tool the Responses
// builder put on the wire, read from a built body of either shape: a top-level
// tools array, or the additional_tools item a Lite route carries in input.
func responsesWireToolSchemas(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	entries := func(list []any) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		for i, entry := range list {
			m, ok := entry.(map[string]any)
			if !ok {
				t.Fatalf("wire tool %d = %#v, want an object", i, entry)
			}
			out = append(out, m)
		}
		return out
	}
	if list, ok := body["tools"].([]any); ok {
		return entries(list)
	}
	if list, ok := body["tools"].([]map[string]any); ok {
		return list
	}
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("wire body carries no tools and no input: %#v", body)
	}
	first, _ := input[0].(map[string]any)
	list, ok := first["tools"].([]any)
	if first["type"] != "additional_tools" || !ok {
		t.Fatalf("first input item = %#v, want the additional_tools entry the Lite route sends", first)
	}
	return entries(list)
}

// A raw subschema is priced as the builder decodes it.
//
// The Responses builder copies each parameter schema before it decides what to
// state on it, and the copy decodes a json.RawMessage subschema so the walk can
// read the nodes inside. The decoded form re-encodes with the text those escapes
// stood for, so the wire is shorter than the caller's list: pricing the catalog's
// count charges bytes the request never carries, and a cap between the two
// prices refuses a request that fits.
//
// The oracle is the builder's own body: the price must equal the serialized
// length of the parameter schema the wire carries, measured with the same
// function the estimate bills by.
func TestARawSubschemaIsPricedAsTheBuilderDecodesIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lite bool
	}{
		{"standard", false},
		{"lite", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := responsesRowOn(false)
			if tc.lite {
				res.Caps.ResponsesLite = new(true)
			}
			defs := rawEscapedCatalog()
			advertised := ToolsJSONBytes(defs)
			req := Request{
				Provider:       "emitted-price",
				Model:          "model",
				Messages:       []Message{User("x")},
				Tools:          defs,
				ToolsJSONBytes: advertised,
			}

			p, ok := ProtocolFor(res.Protocol)
			if !ok {
				t.Fatalf("no protocol registered for %q", res.Protocol)
			}
			body, err := p.BuildBody(ShapeRequest(req, res), res)
			if err != nil {
				t.Fatalf("BuildBody: %v", err)
			}
			wire := responsesWireToolSchemas(t, body)
			if len(wire) != 1 {
				t.Fatalf("wire carries %d tools, want 1", len(wire))
			}
			wireParams, ok := wire[0]["parameters"].(map[string]any)
			if !ok {
				t.Fatalf("wire tool carries parameters %#v, want the schema the builder emitted", wire[0]["parameters"])
			}
			want := ToolsJSONBytes([]ToolDefinition{{Name: defs[0].Name, Parameters: wireParams}})
			if want >= advertised {
				t.Fatalf("fixture does not distinguish the prices: wire %d bytes, caller's %d: the description "+
					"has to lose the escape sequences for this case to prove anything", want, advertised)
			}

			if priced := EffectiveToolsJSONBytes(req, res, false); priced != want {
				t.Fatalf("priced count = %d, want the wire schema's %d (the catalog's count is %d): the builder "+
					"decodes the raw subschema, so the escaped form is not what dispatches",
					priced, want, advertised)
			}
		})
	}
}

// The corrected price is what admission reads, so a cap between it and the
// escaped count admits a request whose bytes fit.
func TestARawSubschemaPriceLetsAFittingRequestThrough(t *testing.T) {
	t.Parallel()
	res := responsesRowOn(false)
	defs := rawEscapedCatalog()
	advertised := ToolsJSONBytes(defs)
	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	wireReading := EstimateInputTokensForRoute(res, req, false).Tokens
	escapedReading := EstimateInputTokensForResolved(res, req).Tokens
	if escapedReading <= wireReading {
		t.Fatalf("priced reading = %d, the catalog's escaped count = %d: the builder decodes the raw subschema, so "+
			"the priced reading has to be the smaller one", wireReading, escapedReading)
	}
	limit := capBetweenPrices(t, wireReading, escapedReading)

	c, _, bodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, false)
	if _, err := c.Complete(context.Background(), req); err != nil {
		if _, budget := errors.AsType[*ContextBudgetError](err); budget {
			t.Fatalf("Complete under a cap of %d refused a request that dispatches %d input tokens: the wire "+
				"list is smaller than the %d-byte catalog", limit, wireReading, advertised)
		}
		t.Fatalf("Complete under a cap of %d: %v", limit, err)
	}
	if got := len(bodies()); got != 1 {
		t.Fatalf("requests on the wire = %d, want 1", got)
	}
	if wireBytes := ToolsJSONBytes([]ToolDefinition{{Name: defs[0].Name, Parameters: responsesWireParams(t, bodies()[0])}}); wireBytes >= advertised {
		t.Fatalf("dispatched schema measures %d bytes against the catalog's %d: the fixture's premise did not hold",
			wireBytes, advertised)
	}
}

// responsesWireParams is the first tool's parameter schema in a Responses body.
func responsesWireParams(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	wire := responsesWireToolSchemas(t, body)
	if len(wire) == 0 {
		t.Fatal("wire carries no tools")
	}
	params, _ := wire[0]["parameters"].(map[string]any)
	return params
}
