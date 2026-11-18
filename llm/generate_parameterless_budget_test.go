package llm

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// wireCatalog returns the tool definitions as the built-in adapters send them:
// a tool that declares no parameters is advertised with the open empty object,
// which is the schema every adapter synthesizes for it. It is the reference the
// budget has to agree with, built independently of what prepareTools produces.
func wireCatalog(defs []ToolDefinition) []ToolDefinition {
	out := make([]ToolDefinition, len(defs))
	copy(out, defs)
	for i := range out {
		if out[i].DeclaresNoParameters() || out[i].Parameters == nil {
			out[i].Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	}
	return out
}

// TestParameterlessToolIsPricedByTheSchemaTheAdapterSends is M1: the budget must
// price the schema that actually goes on the wire.
//
// prepareTools used to strip the default schema back to nil before the catalog
// was stamped, while every built-in adapter still emitted the open empty object
// for that tool. The stamp then measured a catalog smaller than the request, and
// admission — which bills the catalog by that stamp — admitted requests near the
// input ceiling that the wire is over the limit for.
//
// The assertion is the admission result. The ceiling is derived from the schema
// the adapters emit, so a request whose catalog is under-priced is admitted
// while it is over the limit, and that is exactly what must not happen.
func TestParameterlessToolIsPricedByTheSchemaTheAdapterSends(t *testing.T) {
	const instance = "priced"
	c := NewClient(WithRegistry(toolStampRegistry(t, instance)))

	_, defs, err := prepareTools([]Tool{
		// A parameterless tool: the definition whose schema was stripped.
		{Definition: ToolDefinition{Name: "current_time"}},
		// A tool with a declared schema, so the catalog is not all one kind.
		{Definition: ToolDefinition{
			Name:       "read_file",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("prepareTools: %v", err)
	}

	newRequest := func(catalog []ToolDefinition) Request {
		return Request{
			Model:          "tool-budget-probe",
			Provider:       instance,
			Messages:       []Message{User("hello")},
			Tools:          catalog,
			ToolsJSONBytes: ToolsJSONBytes(catalog),
		}
	}
	// What the adapter is handed, and what it puts on the wire.
	adapterReq := newRequest(defs)
	wireReq := newRequest(wireCatalog(defs))

	res, err := c.Resolve(instance + "/" + adapterReq.Model)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The reserve scales with the ceiling, so the boundary is read from the
	// budget's own instrument at the ceiling under test rather than assumed.
	generous := 10_000_000
	res.Caps = registry.Caps{MaxInputTokens: &generous}
	wirePrice := EstimateInputTokensForResolved(res, wireReq).Tokens
	tight := wirePrice + SafetyReserveTokens(wirePrice) - 1
	if got := wirePrice + SafetyReserveTokens(tight); got != tight+1 {
		t.Fatalf("reserve at %d makes the wire price %d, want %d: the boundary is not one token under the wire", tight, got, tight+1)
	}
	res.Caps = registry.Caps{MaxInputTokens: &tight}

	// The wire is one token over that ceiling, so it is refused whatever
	// catalog the budget happens to read.
	var budgetErr *ContextBudgetError
	if _, _, err := ApplyTokenBudget(wireReq, res); !errors.As(err, &budgetErr) || budgetErr.Limit != "max_input" {
		t.Fatalf("the wire catalog was not refused under a %d ceiling it is over by one token: err = %v", tight, err)
	}

	// The regression: the catalog the adapter is handed has to price the same.
	// Stripped of the default schema it prices lower, so this call is admitted
	// while the wire it becomes is over the ceiling.
	if _, _, err := ApplyTokenBudget(adapterReq, res); !errors.As(err, &budgetErr) {
		t.Fatalf("the adapter's catalog was admitted under a %d ceiling, but the schema it emits is over it: err = %v", tight, err)
	}

	// The output allowance follows the same count: against one window the two
	// catalogs must leave the same headroom, because they are the same request.
	window := wirePrice + SafetyReserveTokens(tight) + 100
	res.Caps = registry.Caps{ContextWindow: &window}
	_, adapterBudget, err := ApplyTokenBudget(adapterReq, res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget (adapter) at a %d window: %v", window, err)
	}
	_, wireBudget, err := ApplyTokenBudget(wireReq, res)
	if err != nil {
		t.Fatalf("ApplyTokenBudget (wire) at a %d window: %v", window, err)
	}
	if adapterBudget.AdmittedOutput != wireBudget.AdmittedOutput {
		t.Fatalf("admitted output %d for the adapter's catalog, %d for the wire's: the budget priced a different request than the adapter sends",
			adapterBudget.AdmittedOutput, wireBudget.AdmittedOutput)
	}
	if adapterBudget.AdmittedOutput <= 0 {
		t.Fatalf("admitted output = %d, want a positive headroom to compare", adapterBudget.AdmittedOutput)
	}
}
