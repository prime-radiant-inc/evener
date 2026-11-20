package llm

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// Preflight and dispatch have to agree about what an override-served request
// costs. An override marshals the definition it is handed, so its own list is
// its wire list and no protocol's emission describes it.
//
// The same request on the same row costs the protocol's emitted list when the
// built-in builder serves it, and the catalog when the override does. A cap
// between those two counts admits the override, so both halves of its route have
// to read the smaller count: a dispatch that priced the built-in emission would
// refuse a request preflight admitted.
func TestAnOverrideAdmitsARequestPricedForAProtocolRoute(t *testing.T) {
	t.Parallel()
	defs := parameterlessCatalog(16)
	adapter := &recordingPriceAdapter{name: "emitted-price"}
	// The row is a strict Responses row, so the route's emission is strictly
	// larger than the list an override marshals.
	c, res, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, true)
	c.Register(adapter)

	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	catalog := ToolsJSONBytes(defs)
	if catalog <= 0 {
		t.Fatalf("fixture does not bill the catalog: %d bytes", catalog)
	}
	protocolReading := EstimateInputTokensForRoute(res, req, false).Tokens
	overrideReading := EstimateInputTokensForRoute(res, req, true).Tokens
	if overrideReading >= protocolReading {
		t.Fatalf("fixture does not distinguish the routes: override=%d strict=%d",
			overrideReading, protocolReading)
	}
	limit := capBetweenPrices(t, overrideReading, protocolReading)

	// A cap between the override's own count and the protocol's charge. The
	// request fits the override, and preflight says so.
	capped, _, _ := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, true)
	capped.Register(adapter)
	preflight, err := capped.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if preflight.Tokens != overrideReading {
		t.Fatalf("preflight reading = %d, want the override's own %d: an override marshals the definitions it is "+
			"handed, so the protocol's emission describes a list it never sends",
			preflight.Tokens, overrideReading)
	}

	if _, err := capped.Complete(context.Background(), req); err != nil {
		if budgetErr, ok := errors.AsType[*ContextBudgetError](err); ok {
			t.Fatalf("dispatch refused a request preflight admitted (%d tokens against a cap of %d): %v",
				preflight.Tokens, limit, budgetErr)
		}
		t.Fatalf("Complete: %v", err)
	}
	dispatched := adapter.last()
	// The caller set no stamp, so the adapter observes none: pricing and
	// admission are measurements, and neither writes one onto the request.
	if got := dispatched.ToolsJSONBytes; got != 0 {
		t.Fatalf("override observed ToolsJSONBytes = %d, want the 0 the caller set: admission must not stamp a "+
			"count onto the request", got)
	}
	if got := ToolsJSONBytes(dispatched.Tools); got != catalog {
		t.Fatalf("override received a list measuring %d bytes, want the catalog's %d", got, catalog)
	}
}

// An override with no registry row still sends the catalog it was handed.
//
// An override marshals the definitions it receives, so its own catalog count is
// its wire count. A route the registry cannot resolve at all is priced the same
// way: preflight reports the catalog's count there, and the dispatched request
// must carry it.
func TestAnOverrideWithoutARegistryRowDispatchesTheCatalogCount(t *testing.T) {
	t.Parallel()
	defs := parameterlessCatalog(16)
	advertised := ToolsJSONBytes(defs)
	adapter := &recordingPriceAdapter{name: "unlisted-override"}

	// No registry row: the client cannot resolve the instance, so nothing shapes
	// or prices the request for the instance before the override is reached.
	c := NewClient()
	c.Register(adapter)
	req := Request{
		Provider:       adapter.name,
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: advertised,
	}

	preflight, err := c.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	dispatched := adapter.last()
	if got := dispatched.ToolsJSONBytes; got != advertised {
		t.Fatalf("override observed ToolsJSONBytes = %d, want the catalog's own %d: the override marshals the "+
			"definitions it is handed, so no protocol's emitted count describes its wire list",
			got, advertised)
	}

	// Both halves of this target price one list, so the reading a caller sizes the
	// request with is the one the adapter's wire form measures.
	if got, want := EstimateInputTokens(dispatched).Tokens, preflight.Tokens; got != want {
		t.Fatalf("dispatched reading = %d, preflight = %d: an override marshals the definitions it is handed, "+
			"so the two describe the same request", got, want)
	}
}
