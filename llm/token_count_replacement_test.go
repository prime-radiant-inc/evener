package llm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// A recorded baseline belongs to a catalog, not to a target.
//
// A caller may price a request for a strict Responses route, then replace the
// catalog with a smaller one and stamp the replacement, and dispatch it to a
// relaxed Chat route. The recorded emission is a count over the catalog the
// caller discarded; restoring it over the replacement's stamp prices a list this
// request no longer carries, so admission can reject a smaller catalog that fits
// or admit a larger one that exceeds the cap.
func TestAReplacementCatalogKeepsItsOwnStampAcrossTargets(t *testing.T) {
	t.Parallel()
	// The catalog the caller priced for the strict route.
	pricedCatalog := parameterlessCatalog(24)
	// The smaller replacement the caller sends instead, stamped the way a caller
	// that measured its own list stamps it.
	replacement := parameterlessCatalog(4)

	priced := priceEmittedToolSchemas(Request{
		Tools:          pricedCatalog,
		ToolsJSONBytes: ToolsJSONBytes(pricedCatalog),
	}, responsesRowOn(true))
	if priced.ToolsJSONBytes == ToolsJSONBytes(pricedCatalog) {
		t.Fatal("fixture does not distinguish the prices: the strict route left the count at the advertised one")
	}

	req := priced
	req.Tools = replacement
	req.ToolsJSONBytes = ToolsJSONBytes(replacement)
	// The relaxed Chat route's builder leaves this catalog as it is, so the
	// replacement's own stamp is the price.
	got := priceEmittedToolSchemas(req, chatRowOn(false))
	if got.ToolsJSONBytes != ToolsJSONBytes(replacement) {
		t.Fatalf("priced count = %d, want the replacement's own %d: the recorded baseline describes the catalog "+
			"the caller replaced, not the list this request carries",
			got.ToolsJSONBytes, ToolsJSONBytes(replacement))
	}
}

// A replacement the caller did not measure is measured by whoever prices it,
// rather than answered from another catalog's record.
func TestAnUnstampedReplacementCatalogIsMeasured(t *testing.T) {
	t.Parallel()
	pricedCatalog := parameterlessCatalog(24)
	priced := priceEmittedToolSchemas(Request{
		Tools:          pricedCatalog,
		ToolsJSONBytes: ToolsJSONBytes(pricedCatalog),
	}, responsesRowOn(true))

	replacement := parameterlessCatalog(4)
	req := priced
	req.Tools = replacement
	req.ToolsJSONBytes = 0

	got := priceEmittedToolSchemas(req, chatRowOn(false))
	if want := ToolsJSONBytes(replacement); got.ToolsJSONBytes != want {
		t.Fatalf("priced count = %d, want the replacement's own %d: an unstamped replacement is measured, not "+
			"answered from the catalog it replaced", got.ToolsJSONBytes, want)
	}
}

// A ProviderOptions tools value is the list those routes dispatch.
//
// The Chat Completions builder writes the request's tools and then copies the
// protocol's options over the body, so an explicit tools option replaces the
// projected catalog. Pricing the discarded catalog charges bytes the wire never
// carries: a cap between the advertised and projected counts can reject a
// request that sends a smaller replacement.
func TestAProviderToolsOptionIsTheListThatIsPriced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		res   registry.Resolved
		small []ToolDefinition
	}{
		{"chat", chatRowOn(false), parameterlessCatalog(4)},
		{"responses", responsesRowOn(false), parameterlessCatalog(4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := parameterlessCatalog(24)
			// The replacement the caller puts on the wire: a smaller list of the
			// same definitions, which measures less than the catalog.
			encoded, err := json.Marshal(tc.small)
			if err != nil {
				t.Fatalf("measure the replacement: %v", err)
			}
			advertised := len(encoded)
			req := Request{
				Tools:          catalog,
				ToolsJSONBytes: ToolsJSONBytes(catalog),
				ProviderOptions: map[string]any{
					tc.res.Protocol: map[string]any{"tools": tc.small},
				},
			}
			got := priceEmittedToolSchemas(req, tc.res)
			if got.ToolsJSONBytes != advertised {
				t.Fatalf("priced count = %d, want the replacement's %d: the option replaces the catalog on this "+
					"route, so the discarded catalog is not what dispatches",
					got.ToolsJSONBytes, advertised)
			}
		})
	}
}

// The replacement is re-decided on every price: an option that is gone leaves the
// catalog's own count, not the price of a list the request no longer carries.
func TestAGoneToolsOptionRestoresTheCatalogPrice(t *testing.T) {
	t.Parallel()
	catalog := parameterlessCatalog(24)
	res := chatRowOn(false)
	req := Request{
		Tools:          catalog,
		ToolsJSONBytes: ToolsJSONBytes(catalog),
		ProviderOptions: map[string]any{
			res.Protocol: map[string]any{"tools": parameterlessCatalog(2)},
		},
	}
	withOption := priceEmittedToolSchemas(req, res)
	if withOption.ToolsJSONBytes == ToolsJSONBytes(catalog) {
		t.Fatal("fixture does not distinguish the prices: the option left the catalog's count")
	}

	withOption.ProviderOptions = nil
	got := priceEmittedToolSchemas(withOption, res)
	if want := ToolsJSONBytes(catalog); got.ToolsJSONBytes != want {
		t.Fatalf("priced count after the option was removed = %d, want the catalog's %d: the replacement's "+
			"price describes a list this request no longer carries", got.ToolsJSONBytes, want)
	}
}

// providerToolDefinitions is the tool set the replacement fixtures are rendered
// from, so a wire entry and a catalog definition describe the same tool.
func providerToolDefinitions(n int) []ToolDefinition {
	defs := make([]ToolDefinition, n)
	for i := range defs {
		defs[i] = ToolDefinition{
			Name:        "probe_" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Description: "a tool the caller hands over through the protocol's options",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}
	}
	return defs
}

// A tools value in ProviderOptions is the whole tool list when the request
// carries no catalog.
//
// The Chat and standard Responses builders write the request's own tools and
// then copy the protocol's options over the body, so a caller that hands the
// list through that option and leaves Request.Tools empty sends the replacement
// alone. Pricing the empty catalog charges nothing for tools the wire carries,
// and a cap below their cost admits a request over budget.
//
// The price is the catalog measurement of the same tools, which is what makes
// the two paths that supply one wire array agree (see
// replacementToolListJSONBytes). The control is the zero charge a request that
// sends no tools keeps: a count stamped on an empty catalog with no such record
// bills nothing.
func TestAProviderToolsOptionPricesARequestWithNoCatalog(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			res := chatRowOn(false)
			if protocol == registry.ProtocolOpenAIResponses {
				res = responsesRowOn(false)
			}
			defs := providerToolDefinitions(24)
			replacement := replacementList(defs, protocol)
			measured := ToolsJSONBytes(defs)
			if measured < 4 {
				t.Fatalf("fixture does not bill anything: %d bytes", measured)
			}
			req := Request{
				Provider: "emitted-price",
				Model:    "model",
				Messages: []Message{User("x")},
				ProviderOptions: map[string]any{
					protocol: map[string]any{"tools": replacement},
				},
			}
			priced := priceEmittedToolSchemas(req, res)
			if priced.ToolsJSONBytes != measured {
				t.Fatalf("priced count = %d, want the replacement's %d: the option is the only tool list this "+
					"request sends", priced.ToolsJSONBytes, measured)
			}

			// The local reading bills that price. A cap between the reading of a
			// request with no tools and this one has to refuse a request that
			// dispatches the replacement.
			lower := EstimateInputTokensForResolved(res, Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
			}).Tokens
			upper := EstimateInputTokensForResolved(res, priced).Tokens
			if upper <= lower {
				t.Fatalf("fixture does not distinguish the readings: without tools=%d with the option=%d", lower, upper)
			}
			limit := capBetweenPrices(t, lower, upper)
			c, _, bodies := emittedPriceRoute(t, limit, protocol, false)
			_, err := c.Complete(context.Background(), req)
			if _, ok := errors.AsType[*ContextBudgetError](err); !ok {
				t.Fatalf("error = %v, want *ContextBudgetError: the request dispatches %d bytes of tools under a cap "+
					"of %d, and %d request(s) reached the wire", err, measured, limit, len(bodies()))
			}
			if got := len(bodies()); got != 0 {
				t.Fatalf("requests on the wire = %d, want 0: a blocked request must not dispatch", got)
			}

			// Under an ample cap the replacement is what the wire carries, which is
			// the premise every count above rests on.
			open, _, openBodies := emittedPriceRoute(t, 1_000_000, protocol, false)
			if _, err := open.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete under an ample cap: %v", err)
			}
			if got := len(openBodies()); got != 1 {
				t.Fatalf("requests on the wire = %d, want 1", got)
			}
			wire, ok := openBodies()[0]["tools"].([]any)
			if !ok || len(wire) != len(replacement) {
				t.Fatalf("wire tools = %#v, want the %d replacement entries", openBodies()[0]["tools"], len(replacement))
			}

			// A stamp with no catalog and no recorded replacement bills nothing:
			// the zero charge a request that sends no tools keeps.
			stamped := Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
				ToolsJSONBytes: measured,
			}
			if got, want := EstimateInputTokensForResolved(res, stamped).Tokens, lower; got != want {
				t.Fatalf("reading for an empty catalog with a stamp = %d, want the no-tools reading %d", got, want)
			}
		})
	}
}

// A transport tools constant is the list the wire carries.
//
// The Chat and standard Responses builders write the request's tools and then
// copy the protocol's options over the body; the transport's body constants run
// after both (protocolhttp.Prepare), so a row that pins tools overrides the
// option. Pricing the option charges bytes the request never sends: under an
// input cap between the two prices, admission refuses a request that fits and
// the local CountInputTokens fallback reports the inflated count.
//
// The route without the constant is the control: there the option is the wire
// list, and its price stands. Both lists are measured the way a catalog of the
// same tools is, so the comparison is between the two routes' wire lists rather
// than between two spellings of one.
func TestATransportToolsConstantIsTheListThatIsPriced(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			pinnedDefs := providerToolDefinitions(2)
			largeDefs := providerToolDefinitions(24)
			pinned := replacementList(pinnedDefs, protocol)
			large := replacementList(largeDefs, protocol)
			pinnedJSON, err := json.Marshal(pinned)
			if err != nil {
				t.Fatalf("measure the pinned list: %v", err)
			}
			pinnedBytes, largeBytes := ToolsJSONBytes(pinnedDefs), ToolsJSONBytes(largeDefs)
			if pinnedBytes == 0 || largeBytes <= pinnedBytes {
				t.Fatalf("fixture does not separate the two prices: pinned=%d option=%d bytes",
					pinnedBytes, largeBytes)
			}

			res := chatRowOn(false)
			if protocol == registry.ProtocolOpenAIResponses {
				res = responsesRowOn(false)
			}
			res.Transport.Body = map[string]any{"tools": pinned}
			req := Request{
				Provider: "emitted-price",
				Model:    "model",
				Messages: []Message{User("x")},
				ProviderOptions: map[string]any{
					protocol: map[string]any{"tools": large},
				},
			}

			priced := priceEmittedToolSchemas(req, res)
			if priced.ToolsJSONBytes != pinnedBytes {
				t.Fatalf("priced count = %d, want the transport's %d: the constant runs after the option, so the "+
					"option's %d bytes never reach the wire", priced.ToolsJSONBytes, pinnedBytes, largeBytes)
			}

			// The option's own price, taken on the route that does not pin a
			// list. It is the inflated reading the constant must replace, and
			// it pins that a route which leaves tools alone still prices the
			// option.
			unpinned := res
			unpinned.Transport = registry.Transport{}
			if got := priceEmittedToolSchemas(req, unpinned).ToolsJSONBytes; got != largeBytes {
				t.Fatalf("route without the constant priced the option at %d bytes, want %d", got, largeBytes)
			}

			without := Request{Provider: req.Provider, Model: req.Model, Messages: req.Messages}
			lower := EstimateInputTokensForResolved(res, without).Tokens
			upper := EstimateInputTokensForResolved(unpinned, priceEmittedToolSchemas(req, unpinned)).Tokens
			limit := capBetweenPrices(t, lower, upper)

			c, _, bodies := emittedPriceRouteWithTransportBody(t, limit, protocol, false, map[string]any{"tools": pinned})
			if _, err := c.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete under a cap of %d: %v: the wire list costs %d bytes and the option's costs %d, "+
					"so this request fits", limit, err, pinnedBytes, largeBytes)
			}
			got := bodies()
			if len(got) != 1 {
				t.Fatalf("requests on the wire = %d, want 1", len(got))
			}
			var wantTools any
			if err := json.Unmarshal(pinnedJSON, &wantTools); err != nil {
				t.Fatalf("decode the pinned list: %v", err)
			}
			if !reflect.DeepEqual(got[0]["tools"], wantTools) {
				t.Fatalf("wire tools = %#v, want the pinned list %#v", got[0]["tools"], wantTools)
			}

			// Stream admits the request through the same budget, so the
			// inflated reading would refuse it there too. This asserts the
			// admission decision only: a fitting request is not refused, and
			// nothing reaches the wire before the decision.
			st, err := c.Stream(context.Background(), req)
			if err != nil {
				t.Fatalf("Stream under a cap of %d: %v: the wire list costs %d bytes and the option's costs %d",
					limit, err, pinnedBytes, largeBytes)
			}
			// The wiring is what this assertion is about, not the decoding: the
			// shared server answers JSON rather than SSE, so the stream is
			// closed once it exists rather than read.
			_ = st.Close()
			if got := len(bodies()); got != 2 {
				t.Fatalf("requests on the wire after Stream = %d, want 2", got)
			}

			// The local reading is the count a caller sizes the request with,
			// so it must not describe the option either.
			counted, err := c.CountInputTokens(context.Background(), req)
			if err != nil {
				t.Fatalf("CountInputTokens: %v", err)
			}
			if counted.Tokens >= upper {
				t.Fatalf("CountInputTokens = %d, want less than the option's reading %d: the request sends %d bytes "+
					"of tools, not %d", counted.Tokens, upper, pinnedBytes, largeBytes)
			}

			// A request that also carries a catalog is priced the same way: the
			// constant overrides the body's tools whatever the builder wrote
			// there, so the catalog is not the wire list either.
			catalog := strictGrowthCatalog(24)
			withCatalog := priceEmittedToolSchemas(Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
				Tools: catalog, ToolsJSONBytes: ToolsJSONBytes(catalog),
			}, res)
			if withCatalog.ToolsJSONBytes != pinnedBytes {
				t.Fatalf("priced count for a catalog under a transport constant = %d, want the constant's %d",
					withCatalog.ToolsJSONBytes, pinnedBytes)
			}
		})
	}
}

// A rebuilt catalog keeps its own stamp even when it measures exactly what the
// previous route emitted.
//
// A relaxed Responses route supplies the open empty schema for a tool that
// declares no parameters. A caller that rebuilds the catalog from that emission
// and stamps it produces a count equal to the recorded emission, so a record
// keyed by its count alone accepts the rebuilt catalog as the one it measured
// and restores the smaller baseline. Chat sends the rebuilt schemas, so the
// price must be their own measurement rather than the catalog without them.
func TestARebuiltCatalogStampSurvivesAnEqualRecordedCount(t *testing.T) {
	t.Parallel()
	defs := parameterlessCatalog(6)
	advertised := ToolsJSONBytes(defs)
	priced := priceEmittedToolSchemas(Request{
		Provider: "emitted-price", Model: "model", Messages: []Message{User("x")},
		Tools: defs, ToolsJSONBytes: advertised,
	}, responsesRowOn(false))
	if priced.ToolsJSONBytes == advertised {
		t.Fatalf("fixture does not distinguish the prices: the relaxed route left the count at the advertised %d",
			advertised)
	}

	// The rebuilt catalog: the same definitions with the schema the relaxed
	// Responses builder supplied already in place.
	rebuilt := make([]ToolDefinition, len(defs))
	for i := range defs {
		rebuilt[i] = defs[i]
		rebuilt[i].Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	stamp := ToolsJSONBytes(rebuilt)
	if stamp != priced.ToolsJSONBytes {
		t.Fatalf("fixture does not reproduce the collision: rebuilt stamp %d, recorded emission %d",
			stamp, priced.ToolsJSONBytes)
	}

	req := priced
	req.Tools = rebuilt
	req.ToolsJSONBytes = stamp
	got := priceEmittedToolSchemas(req, chatRowOn(false))
	if got.ToolsJSONBytes != stamp {
		t.Fatalf("priced count = %d, want the rebuilt catalog's own %d: the record measured the catalog the caller "+
			"discarded, and only its byte count happens to match", got.ToolsJSONBytes, stamp)
	}

	// Admission takes the same price. Chat sends the rebuilt schemas, so a cap
	// between the discarded catalog's price and theirs must refuse the request.
	lower := EstimateInputTokensForResolved(chatRowOn(false), Request{
		Provider: req.Provider, Model: req.Model, Messages: req.Messages,
		Tools: defs, ToolsJSONBytes: advertised,
	}).Tokens
	upper := EstimateInputTokensForResolved(chatRowOn(false), got).Tokens
	if upper <= lower {
		t.Fatalf("fixture does not distinguish the readings: discarded catalog=%d rebuilt=%d", lower, upper)
	}
	limit := capBetweenPrices(t, lower, upper)
	c, _, bodies := emittedPriceRoute(t, limit, registry.ProtocolOpenAIChat, false)
	_, err := c.Complete(context.Background(), req)
	if _, ok := errors.AsType[*ContextBudgetError](err); !ok {
		t.Fatalf("error = %v, want *ContextBudgetError: the request sends %d bytes of rebuilt schemas under a cap "+
			"of %d, and %d request(s) reached the wire", err, stamp, limit, len(bodies()))
	}
	if got := len(bodies()); got != 0 {
		t.Fatalf("requests on the wire = %d, want 0: a blocked request must not dispatch", got)
	}
}
