package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// probeDefinitions is the tool list the replacement fixtures are rendered from:
// a name, a description, and a schema, so a wire entry and a catalog definition
// describe the same tool.
func probeDefinitions(n int) []ToolDefinition {
	defs := make([]ToolDefinition, n)
	for i := range defs {
		defs[i] = ToolDefinition{
			Name:        "probe_" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Description: "a tool the caller hands over through the protocol's options",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			},
		}
	}
	return defs
}

// replacementList renders defs as the tools array a caller supplies through
// protocol's ProviderOptions: the shape that protocol's builder writes.
func replacementList(defs []ToolDefinition, protocol string) []map[string]any {
	switch protocol {
	case registry.ProtocolOpenAIChat:
		out := make([]map[string]any, len(defs))
		for i, d := range defs {
			out[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        d.Name,
					"description": d.Description,
					"parameters":  d.Parameters,
				},
			}
		}
		return out
	case registry.ProtocolOpenAIResponses:
		out := make([]map[string]any, len(defs))
		for i, d := range defs {
			out[i] = map[string]any{
				"type":        "function",
				"name":        d.Name,
				"description": d.Description,
				"parameters":  d.Parameters,
			}
		}
		return out
	case registry.ProtocolAnthropic:
		out := make([]map[string]any, len(defs))
		for i, d := range defs {
			out[i] = map[string]any{
				"name":         d.Name,
				"description":  d.Description,
				"input_schema": d.Parameters,
			}
		}
		return out
	case registry.ProtocolGoogle:
		decls := make([]map[string]any, len(defs))
		for i, d := range defs {
			decls[i] = map[string]any{
				"name":        d.Name,
				"description": d.Description,
				"parameters":  d.Parameters,
			}
		}
		return []map[string]any{{"functionDeclarations": decls}}
	default:
		return nil
	}
}

// A protocol-native route reads its tools option, so the option is the list to
// price.
//
// The Anthropic and Google builders copy their protocol's options over the body,
// and protocolhttp.Prepare applies the transport's body constants after both, so
// a caller that hands the whole tool list through that option sends tools the
// catalog does not name. A price taken from the empty catalog charges nothing for
// those bytes, and an input cap below their cost admits a request over budget.
func TestANativeRoutePricesTheToolsItWritesFromItsOptions(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolAnthropic, registry.ProtocolGoogle} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			option := replacementList(probeDefinitions(24), protocol)
			measured, err := json.Marshal(option)
			if err != nil || len(measured) < 4 {
				t.Fatalf("fixture does not bill anything: %d bytes, %v", len(measured), err)
			}
			req := Request{
				Provider: "emitted-price",
				Model:    "model",
				Messages: []Message{User("x")},
				ProviderOptions: map[string]any{
					protocol: map[string]any{"tools": option},
				},
			}
			res := nativeRow(protocol)

			if got := EffectiveToolsJSONBytes(req, res, false); got != len(measured) {
				t.Fatalf("priced count = %d for a request whose only tools arrive through the %q option (%d "+
					"bytes): the route writes them into the body, so they are part of what dispatches",
					got, protocol, len(measured))
			}

			// The local reading bills that price, so a cap between a request
			// with no tools and this one refuses a request that dispatches the
			// option.
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
				t.Fatalf("error = %v, want *ContextBudgetError: the request dispatches tools under a cap of %d, "+
					"and %d request(s) reached the wire", err, limit, len(bodies()))
			}
			if got := len(bodies()); got != 0 {
				t.Fatalf("requests on the wire = %d, want 0: a blocked request must not dispatch", got)
			}

			// Under an ample cap the option is what the wire carries, which is
			// the premise every count above rests on.
			open, _, openBodies := emittedPriceRoute(t, 1_000_000, protocol, false)
			if _, err := open.Complete(context.Background(), req); err != nil {
				t.Fatalf("Complete under an ample cap: %v", err)
			}
			if got := len(openBodies()); got != 1 {
				t.Fatalf("requests on the wire = %d, want 1", got)
			}
			if _, ok := openBodies()[0]["tools"]; !ok {
				t.Fatalf("wire body carries no tools: the option did not reach the wire, so the premise of this "+
					"case does not hold: %#v", openBodies()[0])
			}
		})
	}
}

// A transport tools constant runs after the option on a native route too, so it
// is the list the wire carries.
//
// protocolhttp.Prepare applies the row's body constants after the builder and
// after the ProviderOptions overlay, so a row that pins tools overrides the
// option. Pricing the option charges bytes the request never sends: under an
// input cap between the two prices, admission refuses a request that fits. The
// route without the constant is the control: there the option is the wire list
// and its price stands.
func TestANativeRoutePrefersTheTransportToolsConstant(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolAnthropic, registry.ProtocolGoogle} {
		t.Run(protocol, func(t *testing.T) {
			t.Parallel()
			pinned := replacementList(probeDefinitions(2), protocol)
			large := replacementList(probeDefinitions(24), protocol)
			pinnedJSON, err := json.Marshal(pinned)
			if err != nil {
				t.Fatalf("measure the pinned list: %v", err)
			}
			largeJSON, err := json.Marshal(large)
			if err != nil {
				t.Fatalf("measure the option's list: %v", err)
			}
			if len(largeJSON) <= len(pinnedJSON) {
				t.Fatalf("fixture does not separate the two prices: pinned=%d option=%d bytes",
					len(pinnedJSON), len(largeJSON))
			}
			req := Request{
				Provider: "emitted-price",
				Model:    "model",
				Messages: []Message{User("x")},
				ProviderOptions: map[string]any{
					protocol: map[string]any{"tools": large},
				},
			}

			res := nativeRow(protocol)
			res.Transport.Body = map[string]any{"tools": pinned}
			priced := EffectiveToolsJSONBytes(req, res, false)
			unpinned := EffectiveToolsJSONBytes(req, nativeRow(protocol), false)
			if priced != len(pinnedJSON) {
				t.Fatalf("priced count under a pinned list = %d, want the constant's %d: the constant runs after "+
					"the option", priced, len(pinnedJSON))
			}
			if unpinned != len(largeJSON) {
				t.Fatalf("route without the constant priced the option at %d bytes, want %d: the option is that "+
					"route's wire list", unpinned, len(largeJSON))
			}

			// Admission takes the constant's price: under a cap between the two
			// readings the request fits.
			lower := EstimateInputTokensForRoute(res, req, false).Tokens
			upper := EstimateInputTokensForRoute(nativeRow(protocol), req, false).Tokens
			limit := capBetweenPrices(t, lower, upper)
			c, _, bodies := emittedPriceRouteWithTransportBody(t, limit, protocol, false, map[string]any{"tools": pinned})
			if _, err := c.Complete(context.Background(), req); err != nil {
				if _, budget := errors.AsType[*ContextBudgetError](err); budget {
					t.Fatalf("Complete under a cap of %d refused a request whose wire list is the %d-byte pinned "+
						"one: %v", limit, len(pinnedJSON), err)
				}
				t.Fatalf("Complete under a cap of %d: %v", limit, err)
			}
			if got := len(bodies()); got != 1 {
				t.Fatalf("requests on the wire = %d, want 1", got)
			}
		})
	}
}

// nativeRow is a resolved row for one of the protocols whose options carry
// tools, with no capability the deriver reads beyond the protocol itself.
func nativeRow(protocol string) registry.Resolved {
	row := registry.Resolved{Instance: "emitted-price", ModelID: "model", WireID: "model"}
	row.Protocol = protocol
	return row
}

// A tool with no ToolDefinition equivalent is still counted by its own bytes.
//
// A replacement is measured where it lies, as a wire value, so a provider-native
// tool — web_search, computer_use, google_search — is an object the provider
// recognizes and its bytes are bytes the request dispatches. Dropping them
// because they do not fit the catalog's definition shape would let a cap below
// their cost admit a request over budget.
func TestAProviderNativeToolIsCountedByItsOwnBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		protocol string
		native   map[string]any
	}{
		{"chat web_search", registry.ProtocolOpenAIChat, map[string]any{"type": "web_search"}},
		{"responses web_search", registry.ProtocolOpenAIResponses, map[string]any{"type": "web_search"}},
		{"anthropic web_search", registry.ProtocolAnthropic, map[string]any{
			"type": "web_search_20250305", "name": "web_search",
		}},
		{"google google_search", registry.ProtocolGoogle, map[string]any{
			"google_search": map[string]any{},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nativeJSON, err := json.Marshal(tc.native)
			if err != nil {
				t.Fatalf("measure the native tool: %v", err)
			}
			for _, n := range []int{2, 5} {
				option := replacementList(probeDefinitions(n), tc.protocol)
				withNative := append(append([]map[string]any{}, option...), tc.native)
				res := nativeRow(tc.protocol)

				bare := EffectiveToolsJSONBytes(Request{
					Provider: "emitted-price", Model: "model", Messages: []Message{User("x")},
					ProviderOptions: map[string]any{tc.protocol: map[string]any{"tools": option}},
				}, res, false)
				withNativePriced := EffectiveToolsJSONBytes(Request{
					Provider: "emitted-price", Model: "model", Messages: []Message{User("x")},
					ProviderOptions: map[string]any{tc.protocol: map[string]any{"tools": withNative}},
				}, res, false)
				if bare <= 0 {
					t.Fatalf("fixture does not bill the option's list: %d bytes", bare)
				}
				if want := len(nativeJSON) + 1; withNativePriced-bare != want {
					t.Fatalf("native tool added %d bytes to the price of %d tools, want its own %d plus the "+
						"separator: a tool with no definition equivalent is still bytes the wire carries",
						withNativePriced-bare, n, want)
				}
			}
		})
	}
}
