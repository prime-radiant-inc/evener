package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// A fallback served by an override prices its own history estimates from the
// list the override marshals.
//
// The request arrives carrying the primary's tool count, which describes the
// schemas the primary's builder emits. An override sends the definitions it is
// handed, so that count describes bytes this route never emits — and both
// fallback history estimates must describe the override's list instead.
// Admission takes the maximum of the local estimate and the full-history one, so
// a history priced by the primary refuses a fallback that fits.
func TestTheFallbackToAnOverridePricesItsOwnCatalog(t *testing.T) {
	t.Parallel()
	// The primary is a Responses row whose builder substitutes a schema for a
	// parameterless tool, so its count is the larger one.
	primaryRow := responsesRouteOn("fallback-override", false)
	primary := testOpenAICompatProfile("fallback-override", "primary", 0).WithResolved(primaryRow)

	// The same instance serves the fallback through an override, so the request
	// it receives is the list it marshals.
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "fallback-override"})
	fallbackRow := responsesRouteOn("fallback-override", false)
	fallbackRow.ModelID, fallbackRow.WireID = "fallback", "fallback"
	fallback := testOpenAICompatProfile("fallback-override", "fallback", 0).WithResolved(fallbackRow)
	sess := newSession(t, withClient(client), withProfile(fallback), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()
	if !sess.servesAnOverride(fallback) {
		t.Fatal("the registered adapter does not read as serving this profile, so this test cannot see the " +
			"difference it exists to pin")
	}

	tools := parameterlessTools(24)
	msgs := []llm.Message{llm.User("hello")}
	primaryReq := llm.Request{
		Provider: primary.ID(),
		Model:    primary.Model(),
		Messages: msgs,
		Tools:    tools,
	}
	primaryPrice := llm.EstimateInputTokensForRoute(primaryRow, primaryReq, false).Tokens
	overridePrice := llm.EstimateInputTokensForRoute(fallbackRow, primaryReq, true).Tokens
	if overridePrice >= primaryPrice {
		t.Fatalf("fixture does not distinguish the routes: override=%d primary=%d", overridePrice, primaryPrice)
	}

	built, ok := sess.responsesContinuationFallbackRequest(fallback, primaryReq, msgs)
	if !ok {
		t.Fatal("responsesContinuationFallbackRequest refused a request that carries full history")
	}
	// Both history estimates are the override's own reading.
	if got := built.InputTokensEstimate; got != overridePrice {
		t.Fatalf("local estimate = %d, want the override's own %d (the primary reads %d)",
			got, overridePrice, primaryPrice)
	}
	if got := built.FullHistoryInputTokensEstimate; got != overridePrice {
		t.Fatalf("full-history estimate = %d, want the override's own %d (the primary reads %d)",
			got, overridePrice, primaryPrice)
	}
	// Admission is a measurement, so the request's own stamp is untouched.
	if built.ToolsJSONBytes != primaryReq.ToolsJSONBytes {
		t.Fatalf("fallback carries ToolsJSONBytes = %d, want the caller's %d",
			built.ToolsJSONBytes, primaryReq.ToolsJSONBytes)
	}
}

// The budget a session uses to decide its warning prices an override-served
// route from the list the override receives.
func TestTheSessionBudgetForAnOverrideKeepsTheCatalogCount(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "budget-override"})
	row := responsesRouteOn("budget-override", true)
	profile := testOpenAICompatProfile("budget-override", "test", 0).WithResolved(row)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()

	tools := parameterlessTools(24)
	advertised := llm.ToolsJSONBytes(tools)
	req := llm.Request{
		Messages:       []llm.Message{llm.User("hello")},
		Tools:          tools,
		ToolsJSONBytes: advertised,
	}
	// The route has to be one the deriver would price, or the override's own
	// answer is indistinguishable from it and the test passes for the wrong
	// reason.
	if projected := llm.EstimateInputTokensForRoute(row, req, false).Tokens; projected == advertised {
		t.Fatal("fixture does not distinguish the routes: the deriver left the count unchanged")
	}

	budgeted, budget, err := sess.budgetModelDispatchRequestForSession(profile, req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	overrideWant := llm.EstimateInputTokensForRoute(row, req, true).Tokens
	if got, wantTotal := budget.InputTokens, overrideWant+budget.SafetyTokens; got != wantTotal {
		t.Fatalf("override-served budget priced the input at %d, want the catalog's %d plus the %d reserve = %d: "+
			"an override marshals the definition it was handed", got, overrideWant, budget.SafetyTokens, wantTotal)
	}
	if budgeted.ToolsJSONBytes != advertised {
		t.Fatalf("budgeted request carries ToolsJSONBytes = %d, want the catalog's %d", budgeted.ToolsJSONBytes, advertised)
	}
}
