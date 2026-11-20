package llm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

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

// A catalog the caller replaced is priced as the list the request carries.
//
// A caller may hand one catalog to a route, replace it, and dispatch the
// replacement. The price is computed from the request as it stands, so it
// describes the replacement and the discarded catalog never contributes.
func TestAReplacementCatalogIsPricedAsTheListThatDispatches(t *testing.T) {
	t.Parallel()
	replacement := parameterlessCatalog(4)
	req := Request{
		Provider: "emitted-price", Model: "model", Messages: []Message{User("x")},
		Tools: replacement,
	}

	// Untouched by the caller and on a route whose builder leaves this list
	// alone, the price is the catalog's own count.
	if got, want := EffectiveToolsJSONBytes(req, chatRowOn(false), false), ToolsJSONBytes(replacement); got != want {
		t.Fatalf("priced count = %d, want the catalog's own %d", got, want)
	}
	// A route whose builder substitutes a schema prices the substituted list,
	// whatever the caller measured before.
	stamped := req
	stamped.ToolsJSONBytes = 9_999
	if got := EffectiveToolsJSONBytes(stamped, responsesRowOn(false), false); got == 9_999 {
		t.Fatal("priced count reused the caller's stamp: a route prices its own emission, not a number on the request")
	}
}

// A ProviderOptions tools value is the list those routes dispatch.
//
// The Chat Completions builder writes the request's tools and then copies the
// protocol's options over the body, so an explicit tools option replaces the
// projected catalog. Pricing the discarded catalog charges bytes the wire never
// carries: a cap between the two counts can reject a request that sends a
// smaller replacement.
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
			if got := EffectiveToolsJSONBytes(req, tc.res, false); got != advertised {
				t.Fatalf("priced count = %d, want the replacement's %d: the option replaces the catalog on this "+
					"route, so the discarded catalog is not what dispatches", got, advertised)
			}
		})
	}
}

// Without an option the catalog's own count is the price.
//
// The replacement is decided from the request each time, so a request that never
// carried one cannot be priced as though it did.
func TestARequestWithoutAToolsOptionPricesItsCatalog(t *testing.T) {
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
	withOption := EffectiveToolsJSONBytes(req, res, false)
	if withOption == ToolsJSONBytes(catalog) {
		t.Fatal("fixture does not distinguish the prices: the option left the catalog's count")
	}

	req.ProviderOptions = nil
	if got, want := EffectiveToolsJSONBytes(req, res, false), ToolsJSONBytes(catalog); got != want {
		t.Fatalf("priced count without the option = %d, want the catalog's %d", got, want)
	}
}

// A tools value in ProviderOptions is the whole tool list when the request
// carries no catalog.
//
// The Chat and standard Responses builders write the request's own tools and
// then copy the protocol's options over the body, so a caller that hands the
// list through that option and leaves Request.Tools empty sends the replacement
// alone. Pricing the empty catalog charges nothing for tools the wire carries,
// and a cap below their cost admits a request over budget.
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
			measured, err := json.Marshal(replacement)
			if err != nil {
				t.Fatalf("measure the replacement: %v", err)
			}
			if len(measured) < 4 {
				t.Fatalf("fixture does not bill anything: %d bytes", len(measured))
			}
			req := Request{
				Provider: "emitted-price",
				Model:    "model",
				Messages: []Message{User("x")},
				ProviderOptions: map[string]any{
					protocol: map[string]any{"tools": replacement},
				},
			}
			if got := EffectiveToolsJSONBytes(req, res, false); got != len(measured) {
				t.Fatalf("priced count = %d, want the replacement's %d: the option is the only tool list this "+
					"request sends", got, len(measured))
			}

			// The local reading bills that price. A cap between the reading of a
			// request with no tools and this one has to refuse a request that
			// dispatches the replacement.
			lower := EstimateInputTokensForResolved(res, Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
			}).Tokens
			upper := EstimateInputTokensForRoute(res, req, false).Tokens
			if upper <= lower {
				t.Fatalf("fixture does not distinguish the readings: without tools=%d with the option=%d", lower, upper)
			}
			limit := capBetweenPrices(t, lower, upper)
			c, _, bodies := emittedPriceRoute(t, limit, protocol, false)
			_, err = c.Complete(context.Background(), req)
			if _, ok := errors.AsType[*ContextBudgetError](err); !ok {
				t.Fatalf("error = %v, want *ContextBudgetError: the request dispatches %d bytes of tools under a cap "+
					"of %d, and %d request(s) reached the wire", err, len(measured), limit, len(bodies()))
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

			// A request that sends no tools and names none bills nothing.
			if got := EffectiveToolsJSONBytes(Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
			}, res, false); got != 0 {
				t.Fatalf("priced count for a request with no tools = %d, want 0", got)
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
// list, and its price stands.
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
			largeJSON, err := json.Marshal(large)
			if err != nil {
				t.Fatalf("measure the option's list: %v", err)
			}
			pinnedBytes, largeBytes := len(pinnedJSON), len(largeJSON)
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

			if got := EffectiveToolsJSONBytes(req, res, false); got != pinnedBytes {
				t.Fatalf("priced count = %d, want the transport's %d: the constant runs after the option, so the "+
					"option's %d bytes never reach the wire", got, pinnedBytes, largeBytes)
			}

			// The option's own price, taken on the route that does not pin a
			// list. It is the inflated reading the constant must replace, and
			// it pins that a route which leaves tools alone still prices the
			// option.
			unpinned := res
			unpinned.Transport = registry.Transport{}
			if got := EffectiveToolsJSONBytes(req, unpinned, false); got != largeBytes {
				t.Fatalf("route without the constant priced the option at %d bytes, want %d", got, largeBytes)
			}

			without := Request{Provider: req.Provider, Model: req.Model, Messages: req.Messages}
			lower := EstimateInputTokensForResolved(res, without).Tokens
			upper := EstimateInputTokensForRoute(unpinned, req, false).Tokens
			limit := capBetweenPrices(t, lower, upper)

			c, _, bodies := emittedPriceRouteWithTransportBody(t, limit, protocol, false, map[string]any{"tools": pinned})
			if _, err := c.Complete(context.Background(), req); err != nil {
				if _, budget := errors.AsType[*ContextBudgetError](err); budget {
					t.Fatalf("Complete under a cap of %d refused a request that dispatches the %d-byte pinned list "+
						"(the option's list is %d): %v", limit, pinnedBytes, largeBytes, err)
				}
				t.Fatalf("Complete under a cap of %d: %v", limit, err)
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
				if _, budget := errors.AsType[*ContextBudgetError](err); budget {
					t.Fatalf("Stream under a cap of %d refused a request that dispatches the pinned list: %v",
						limit, err)
				}
				t.Fatalf("Stream under a cap of %d: %v", limit, err)
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
			withCatalog := Request{
				Provider: req.Provider, Model: req.Model, Messages: req.Messages,
				Tools: catalog, ToolsJSONBytes: ToolsJSONBytes(catalog),
			}
			if got := EffectiveToolsJSONBytes(withCatalog, res, false); got != pinnedBytes {
				t.Fatalf("priced count for a catalog under a transport constant = %d, want the constant's %d",
					got, pinnedBytes)
			}
		})
	}
}
