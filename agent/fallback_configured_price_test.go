package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// A fallback prices the request it actually sends, provider options included.
//
// The round's request arrives carrying the primary's provider options, and a
// tools value there is a list the primary puts on the wire. A fallback that
// configures its own options replaces that value, so a price taken before the
// configuration bills a list this route never sends. Admission takes the
// maximum of the local estimate and both history stamps, so the stale price
// becomes a floor that refuses a fallback which fits.
func TestTheFallbackPricesTheProviderOptionsItSends(t *testing.T) {
	t.Parallel()
	row := registry.Resolved{Instance: "fallback-options", ModelID: "fallback", WireID: "fallback"}
	row.Protocol = registry.ProtocolOpenAIChat
	row.Caps.StrictTools = new(false)
	fallback := testOpenAICompatProfile("fallback-options", "fallback", 0).WithResolved(row)

	// The primary hands its route a replacement list far larger than the
	// catalog; the fallback sends the catalog, which the Chat builder grows
	// with a synthesized schema per parameterless tool.
	tools := parameterlessTools(24)
	req := llm.Request{
		Provider: "fallback-options", Model: "primary",
		Messages:       []llm.Message{llm.User("hello")},
		Tools:          tools,
		ToolsJSONBytes: llm.ToolsJSONBytes(tools),
		ProviderOptions: map[string]any{
			registry.ProtocolOpenAIChat: map[string]any{"tools": parameterlessTools(400)},
		},
	}
	// The identity the fallback's own request carries, so both prices describe
	// the same target.
	priced := req
	priced.Provider, priced.Model = fallback.ID(), fallback.Model()
	replacedPrice := llm.EstimateInputTokensForRoute(row, priced, false).Tokens
	sent := priced
	sent.ProviderOptions = nil
	sentPrice := llm.EstimateInputTokensForRoute(row, sent, false).Tokens
	if replacedPrice <= sentPrice {
		t.Fatalf("fixture does not distinguish the lists: replacement=%d catalog=%d", replacedPrice, sentPrice)
	}

	// A cap between the two prices admits the fallback and refuses a request
	// billed at the primary's replacement.
	capped := fallback
	res := capped.Resolved()
	res.Caps.MaxInputTokens = new(budgetCapBetween(t, sentPrice, replacedPrice))
	res.Caps.ContextWindow = new(1_000_000)
	capped = capped.WithResolved(res)

	sess := newSession(t, withProfile(capped), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	built, route, ok := sess.responsesContinuationFallbackRequest(capped, req, req.Messages, "")
	if !ok {
		t.Fatal("responsesContinuationFallbackRequest refused a request that carries full history")
	}
	// Both history estimates are the price of the list the fallback sends.
	if got := built.InputTokensEstimate; got != sentPrice {
		t.Fatalf("fallback InputTokensEstimate = %d, want the list it sends: %d (the primary's replacement reads %d)",
			got, sentPrice, replacedPrice)
	}
	if got := built.FullHistoryInputTokensEstimate; got != sentPrice {
		t.Fatalf("fallback FullHistoryInputTokensEstimate = %d, want the list it sends: %d (the primary's replacement reads %d)",
			got, sentPrice, replacedPrice)
	}
	// Those stamps are the measurement admission bills.
	if _, _, err := route.admit(built); err != nil {
		t.Fatalf("the fallback was refused under a cap that admits the list it sends: %v", err)
	}
}
