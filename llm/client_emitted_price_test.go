package llm

import (
	"context"
	"encoding/json"
	"strings"
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
// advertised count therefore carries no schema for it. The Responses builder
// supplies the open empty object, so the request that dispatches is larger than
// the one the advertised count describes, and an input cap between the two
// prices admits a request over budget.
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

	priced := priceEmittedToolSchemas(req, res)
	if priced.ToolsJSONBytes <= advertised {
		t.Fatalf("priced count = %d, want more than the advertised %d: the builder supplies a schema for a "+
			"parameterless tool, and that is the list the wire carries",
			priced.ToolsJSONBytes, advertised)
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
	if want := ToolsJSONBytes(emitted); priced.ToolsJSONBytes != want {
		t.Fatalf("priced count = %d, want the emitted list's %d", priced.ToolsJSONBytes, want)
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

	strictPriced := priceEmittedToolSchemas(req, responsesRowOn(true))
	if strictPriced.ToolsJSONBytes <= advertised {
		t.Fatalf("strict-capable price = %d, want more than the advertised %d: the projection closes the "+
			"object and states a required list", strictPriced.ToolsJSONBytes, advertised)
	}

	// The control. A route without the capability sends this schema unchanged,
	// so its price is the advertised count and nothing moved.
	nonStrict := priceEmittedToolSchemas(req, responsesRowOn(false))
	if nonStrict.ToolsJSONBytes != advertised {
		t.Fatalf("route without strict enforcement priced the catalog at %d, want the advertised %d: its "+
			"builder sends this schema unchanged", nonStrict.ToolsJSONBytes, advertised)
	}
}

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

	defs := []ToolDefinition{{Name: "parameterless"}}
	baseline := ToolsJSONBytes(defs)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: baseline,
	}
	forResponses := priceEmittedToolSchemas(req, responsesRowOn(true))
	if forResponses.ToolsJSONBytes == baseline {
		t.Fatalf("fixture does not distinguish the targets: the Responses projection left the count at the "+
			"baseline %d, so retargeting cannot show a difference", baseline)
	}

	noDeriver := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	noDeriver.Protocol = protocolID
	got := priceEmittedToolSchemas(forResponses, noDeriver)
	if got.ToolsJSONBytes != baseline {
		t.Fatalf("route without a deriver priced the catalog at %d, want the baseline %d: its builder sends the "+
			"definitions it was handed, so the Responses charge describes a list this route never emits",
			got.ToolsJSONBytes, baseline)
	}
}

// A count produced for one target is not a count for another, even when the
// second target's builder changes nothing.
//
// A caller may build a request, price it against a strict Responses row, and
// then reuse it on a non-strict route whose builder leaves the definitions
// alone. That deriver reports no change, so a rule that keeps the incoming count
// because "nothing changed" retains a charge for a closed schema this route
// never emits. The price has to fall back to the list's own measurement before
// the new decision is applied, and the unchanged-decision branch is exactly
// where that has to happen.
func TestARetargetedRequestRecoversTheBaselinePrice(t *testing.T) {
	t.Parallel()
	defs := []ToolDefinition{{Name: "parameterless"}}
	baseline := ToolsJSONBytes(defs)
	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	req.ToolsJSONBytes = baseline

	forResponses := priceEmittedToolSchemas(req, responsesRowOn(true))
	if forResponses.ToolsJSONBytes == baseline {
		t.Fatalf("fixture does not distinguish the targets: the Responses projection left the count at the "+
			"baseline %d, so retargeting cannot show a difference", baseline)
	}

	// A non-strict Responses route substitutes the open object for this tool and
	// then reports a change, so the retarget is observable as a different price
	// rather than as the baseline. The point is that it is not the strict price.
	forRelaxed := priceEmittedToolSchemas(forResponses, responsesRowOn(false))
	if forRelaxed.ToolsJSONBytes == forResponses.ToolsJSONBytes {
		t.Fatalf("retargeted count = %d, the same as the strict price: the strict charge must not survive a "+
			"retarget", forRelaxed.ToolsJSONBytes)
	}
	// And it is the relaxed route's own answer, which is smaller than the strict
	// one because the closed object and stated required list are gone.
	if forRelaxed.ToolsJSONBytes >= forResponses.ToolsJSONBytes {
		t.Fatalf("relaxed price = %d, want less than the strict %d",
			forRelaxed.ToolsJSONBytes, forResponses.ToolsJSONBytes)
	}
}

// Pricing a request is an admission decision a caller may take early and
// forward, so it has to be idempotent.
//
// The count this replaces described the substituted form, so the emitted price
// differs from it by the difference between the two lists. Carrying that
// difference across a call — adjusting the incoming count instead of assigning
// the measurement — makes the result depend on how many times it was applied:
// middleware that prepriced and forwarded, followed by admission, bills the
// emitted list twice over. A cap between the two counts then refuses a request
// that fits.
func TestPricingTheEmittedListIsIdempotent(t *testing.T) {
	t.Parallel()
	res := responsesRowOn(true)
	defs := parameterlessCatalog(6)
	req := Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: ToolsJSONBytes(defs),
	}

	once := priceEmittedToolSchemas(req, res)
	twice := priceEmittedToolSchemas(once, res)
	if twice.ToolsJSONBytes != once.ToolsJSONBytes {
		t.Fatalf("pricing twice = %d bytes, once = %d: the price must describe the emitted list, not "+
			"accumulate a difference from the count it started with",
			twice.ToolsJSONBytes, once.ToolsJSONBytes)
	}
	// The rule has to have done something, or the agreement above is the
	// agreement of two unchanged counts rather than evidence that applying it
	// twice is safe.
	if once.ToolsJSONBytes == req.ToolsJSONBytes {
		t.Fatalf("prepriced count = %d, unchanged from the advertised count: this route's builder substitutes a "+
			"schema, so pricing it must move the count", once.ToolsJSONBytes)
	}
	// The reading admission takes has to be the same on both paths.
	if a, b := EstimateInputTokensForResolved(res, once).Tokens, EstimateInputTokensForResolved(res, twice).Tokens; a != b {
		t.Fatalf("admission reading after one price = %d, after two = %d: the two must agree", a, b)
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
			got := priceEmittedToolSchemas(req, res)
			marshals := atomic.LoadInt32(&calls) - before

			if got.ToolsJSONBytes != req.ToolsJSONBytes {
				t.Fatalf("priced count = %d, want the count it arrived with, %d: the projection only reorders a "+
					"required list here, so the bytes are the same", got.ToolsJSONBytes, req.ToolsJSONBytes)
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
	if priced := priceEmittedToolSchemas(Request{
		Provider:       "emitted-price",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          changing,
		ToolsJSONBytes: advertisedChanging,
	}, res); priced.ToolsJSONBytes == advertisedChanging {
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
// stood for, so the wire is shorter than the caller's list: pricing the caller's
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

			priced := priceEmittedToolSchemas(req, res)
			if priced.ToolsJSONBytes != want {
				t.Fatalf("priced count = %d, want the wire schema's %d (the caller's count is %d): the builder "+
					"decodes the raw subschema, so the escaped form is not what dispatches",
					priced.ToolsJSONBytes, want, advertised)
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
	wireReading := EstimateInputTokensForResolved(res, priceEmittedToolSchemas(req, res)).Tokens
	escapedReading := EstimateInputTokensForResolved(res, req).Tokens
	if escapedReading <= wireReading {
		t.Fatalf("priced reading = %d, the caller's escaped count = %d: the builder decodes the raw subschema, so "+
			"the priced reading has to be the smaller one", wireReading, escapedReading)
	}
	limit := capBetweenPrices(t, wireReading, escapedReading)

	c, _, bodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, false)
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete under a cap of %d refused a request that dispatches %d input tokens: %v",
			limit, wireReading, err)
	}
	if got := len(bodies()); got != 1 {
		t.Fatalf("requests on the wire = %d, want 1", got)
	}
	if wireBytes := ToolsJSONBytes([]ToolDefinition{{Name: defs[0].Name, Parameters: responsesWireParams(t, bodies()[0])}}); wireBytes >= advertised {
		t.Fatalf("dispatched schema measures %d bytes against the caller's %d: the fixture's premise did not hold",
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
