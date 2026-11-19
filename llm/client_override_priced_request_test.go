package llm

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// Preflight and dispatch have to agree about what an override-served request
// costs.
//
// CountInputTokens drops a recorded emission for an override, because an
// override marshals the definition it is handed and no protocol's emission
// describes its wire list. Admission has to make the same decision: a request
// that arrives already priced for a protocol route and is then dispatched to an
// override carries the protocol's charge, and a cap between that charge and the
// override's own count lets preflight report that the request fits while
// dispatch rejects it.
func TestAnOverrideAdmitsARequestPricedForAProtocolRoute(t *testing.T) {
	t.Parallel()
	defs := parameterlessCatalog(16)
	adapter := &recordingPriceAdapter{name: "emitted-price"}
	// The row is a strict Responses row, so the recorded emission is strictly
	// larger than the list an override marshals.
	c, res, _ := emittedPriceRoute(t, 1_000_000, registry.ProtocolOpenAIResponses, true)
	c.Register(adapter)

	req := Request{
		Provider: "emitted-price",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	// The caller preprices the request for the strict route's builder and then
	// dispatches it, which is what a middleware that prices before forwarding
	// leaves behind.
	priced := priceEmittedToolSchemas(req, res)
	if priced.ToolsJSONBytes <= ToolsJSONBytes(defs) {
		t.Fatalf("fixture does not distinguish the prices: advertised=%d priced=%d",
			ToolsJSONBytes(defs), priced.ToolsJSONBytes)
	}
	advertised := EstimateInputTokensForResolved(res, Request{
		Provider:       req.Provider,
		Model:          req.Model,
		Messages:       req.Messages,
		Tools:          defs,
		ToolsJSONBytes: ToolsJSONBytes(defs),
	}).Tokens
	projected := EstimateInputTokensForResolved(res, priced).Tokens
	if projected <= advertised {
		t.Fatalf("fixture does not distinguish the readings: advertised=%d projected=%d", advertised, projected)
	}
	limit := capBetweenPrices(t, advertised, projected)

	// A cap between the override's own count and the protocol's charge. The
	// request fits the override, and preflight says so.
	capped, _, _ := emittedPriceRoute(t, limit, registry.ProtocolOpenAIResponses, true)
	capped.Register(adapter)
	preflight, err := capped.CountInputTokens(context.Background(), priced)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if preflight.Tokens+SafetyReserveTokens(limit) > limit {
		t.Fatalf("preflight reading %d does not fit the cap %d: the fixture cannot show a disagreement", preflight.Tokens, limit)
	}

	if _, err := capped.Complete(context.Background(), priced); err != nil {
		if budgetErr, ok := errors.AsType[*ContextBudgetError](err); ok {
			t.Fatalf("dispatch refused a request preflight admitted (%d tokens against a cap of %d): %v",
				preflight.Tokens, limit, budgetErr)
		}
		t.Fatalf("Complete: %v", err)
	}
	dispatched := adapter.last()
	if got := dispatched.ToolsJSONBytes; got != ToolsJSONBytes(defs) {
		t.Fatalf("override observed ToolsJSONBytes = %d, want the list's own %d: an override marshals the "+
			"definitions it is handed, and the protocol's charge describes a list it never sends",
			got, ToolsJSONBytes(defs))
	}
}

// An override with no registry row still sends the catalog it was handed.
//
// CountInputTokens restores the catalog's own count for every override because
// an override marshals the definitions it receives. Admission and dispatch have
// to make the same decision on a route the registry cannot resolve at all: the
// protocol's emitted count describes a list the override never sends, so it must
// not be the count the adapter reads. Preflight already reports the catalog's
// count there, and the dispatched request must carry it too.
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
	// A caller that priced the request for a protocol route before dispatch, which
	// is what a middleware that preprices and forwards leaves behind.
	req = priceEmittedToolSchemas(req, responsesRowOn(true))
	if req.ToolsJSONBytes <= advertised {
		t.Fatalf("fixture does not distinguish the prices: the caller's prepricing left the count at %d", advertised)
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
			"definitions it is handed, so a protocol's emitted count describes a list it never sends",
			got, advertised)
	}

	// Both halves of this target price one list, so the reading a caller sizes the
	// request with is the one the adapter's wire form measures.
	if got, want := EstimateInputTokens(dispatched).Tokens, preflight.Tokens; got != want {
		t.Fatalf("dispatched reading = %d, preflight = %d: an override marshals the definitions it is handed, "+
			"so the two describe the same request", got, want)
	}
}
