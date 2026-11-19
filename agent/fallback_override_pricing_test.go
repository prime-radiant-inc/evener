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
// handed, so that count describes a catalog this route never emits — and both
// fallback history estimates are computed from that field. Admission takes the
// maximum of the local estimate and the full-history one, so leaving the primary's
// count standing refuses a fallback that fits.
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
	// The request as the fallback loop receives it: the primary's catalog, priced
	// by the primary's builder.
	req := llm.PriceToolSchemas(llm.Request{
		Provider: primary.ID(),
		Model:    primary.Model(),
		Messages: msgs,
		Tools:    tools,
	}, primaryRow)
	advertised := llm.ToolsJSONBytes(tools)
	if req.ToolsJSONBytes <= advertised {
		t.Fatalf("fixture does not distinguish the prices: advertised=%d priced=%d",
			advertised, req.ToolsJSONBytes)
	}

	built, ok := sess.responsesContinuationFallbackRequest(fallback, req, msgs)
	if !ok {
		t.Fatal("responsesContinuationFallbackRequest refused a request that carries full history")
	}
	if built.ToolsJSONBytes != advertised {
		t.Fatalf("fallback carries ToolsJSONBytes = %d, want the override's own catalog %d: an override marshals "+
			"the definitions it is handed, so the primary's larger count describes a list this route never sends",
			built.ToolsJSONBytes, advertised)
	}
	// Both history estimates are computed from the count above, so both have to
	// describe the override's list rather than the primary's.
	ownReq := req
	ownReq.ToolsJSONBytes = advertised
	own := llm.EstimateInputTokensForResolved(fallbackRow, ownReq)
	if built.InputTokensEstimate != own.Tokens {
		t.Fatalf("local estimate = %d, want the override's own %d", built.InputTokensEstimate, own.Tokens)
	}
	if built.FullHistoryInputTokensEstimate != own.Tokens {
		t.Fatalf("full-history estimate = %d, want the override's own %d",
			built.FullHistoryInputTokensEstimate, own.Tokens)
	}
}

// The budget a session uses to decide its warning prices an override-served
// route from the list the override receives.
func TestTheSessionBudgetForAnOverrideRestoresTheBaseline(t *testing.T) {
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
	// A request that arrives priced for the built-in builder the row names: the
	// charge the override never incurs.
	priced := llm.PriceToolSchemas(llm.Request{
		Messages:       []llm.Message{llm.User("hello")},
		Tools:          tools,
		ToolsJSONBytes: advertised,
	}, row)
	if priced.ToolsJSONBytes == advertised {
		t.Fatal("fixture does not distinguish the prices: the deriver left the count unchanged")
	}

	budgeted, _, err := sess.budgetModelDispatchRequestForSession(profile, priced)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if budgeted.ToolsJSONBytes != advertised {
		t.Fatalf("override-served budget priced the catalog at %d, want the advertised %d: an override marshals "+
			"the definition it was handed", budgeted.ToolsJSONBytes, advertised)
	}
}
