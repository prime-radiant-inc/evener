package agent

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// responsesRouteOn returns a resolved Responses row with the strict-tools
// capability as given. The row is built here rather than taken from the live
// registry so the test does not depend on which instance happens to be
// configured.
func responsesRouteOn(instance string, strict bool) registry.Resolved {
	row := registry.Resolved{Instance: instance, ModelID: "test", WireID: "test"}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(strict)
	return row
}

// strictGrowthTools is a catalog whose schemas gain bytes under a strict
// projection: each object is closed and a required list is stated.
func strictGrowthTools(n int) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, n)
	for i := range defs {
		defs[i] = llm.ToolDefinition{
			Name: "write_file_" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
			},
		}
	}
	return defs
}

// parameterlessTools is a catalog whose tools declare no parameters, so the
// builder substitutes a schema for each and the emitted list is larger than the
// advertised one.
func parameterlessTools(n int) []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, n)
	for i := range defs {
		defs[i] = llm.ToolDefinition{Name: "probe_" + string(rune('a'+i%26)) + string(rune('0'+i/26))}
	}
	return defs
}

// The budget a session uses to decide its warning has to price the tool list the
// route dispatches.
//
// A built-in builder substitutes a schema for a parameterless tool, and those
// bytes occupy context. Budgeting the advertised catalog answers with fewer
// input tokens, so the admitted output comes out larger here than the client
// later admits: the user is warned about an allocation bigger than the one
// dispatched, or not warned at all when the larger reading hides the clamp.
func TestTheSessionBudgetPricesTheEmittedToolSchema(t *testing.T) {
	t.Parallel()
	row := responsesRouteOn("budget-emitted", false)
	defs := parameterlessTools(24)
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}
	// Stamped with the advertised count, as a caller that measured its own list
	// would. Without the fix that is the number the budget keeps and charges.
	req.ToolsJSONBytes = llm.ToolsJSONBytes(defs)

	priced := llm.PriceToolSchemas(req, row)
	if priced.ToolsJSONBytes <= llm.ToolsJSONBytes(defs) {
		t.Fatalf("fixture does not distinguish the prices: advertised=%d priced=%d",
			llm.ToolsJSONBytes(defs), priced.ToolsJSONBytes)
	}
	// The budget function under test is the session's, so build a session on this
	// row and compare its reading against the two prices.
	profile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withProfile(profile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	budgeted, _, err := sess.budgetModelDispatchRequestForSession(profile, req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if budgeted.ToolsJSONBytes != priced.ToolsJSONBytes {
		t.Fatalf("session budget priced the catalog at %d, want the emitted %d: the warning it drives would "+
			"describe an allocation the dispatch never had",
			budgeted.ToolsJSONBytes, priced.ToolsJSONBytes)
	}
}

// A strict projection has to change the price the session budgets with, and the
// route without the capability must be the unchanged control.
//
// This is the pair that shows the price follows the emitted bytes rather than
// growing every request: the same request, two routes, one capability apart.
func TestTheSessionBudgetPricesTheStrictProjectionOnlyOnACapableRoute(t *testing.T) {
	t.Parallel()
	defs := strictGrowthTools(24)
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}
	advertised := llm.ToolsJSONBytes(defs)
	req.ToolsJSONBytes = advertised

	strictRow := responsesRouteOn("budget-emitted", true)
	strictProfile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(strictRow)
	strictSess := newSession(t, withProfile(strictProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer strictSess.Close()

	budgeted, _, err := strictSess.budgetModelDispatchRequestForSession(strictProfile, req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if budgeted.ToolsJSONBytes <= advertised {
		t.Fatalf("strict-capable budget priced the catalog at %d, want more than the advertised %d: the "+
			"projection closes every object and states a required list",
			budgeted.ToolsJSONBytes, advertised)
	}
	want := llm.PriceToolSchemas(req, strictRow).ToolsJSONBytes
	if budgeted.ToolsJSONBytes != want {
		t.Fatalf("strict budget priced the catalog at %d, want the emitted %d", budgeted.ToolsJSONBytes, want)
	}

	// The control: the same catalog on a route without the capability is sent
	// unchanged, so its price must be the advertised count.
	relaxedRow := responsesRouteOn("budget-emitted", false)
	relaxedProfile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(relaxedRow)
	relaxedSess := newSession(t, withProfile(relaxedProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer relaxedSess.Close()

	relaxedReq := req
	relaxedReq.ToolsJSONBytes = advertised
	relaxed, _, err := relaxedSess.budgetModelDispatchRequestForSession(relaxedProfile, relaxedReq)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if relaxed.ToolsJSONBytes != advertised {
		t.Fatalf("route without strict enforcement priced the catalog at %d, want the advertised %d: its "+
			"builder sends this catalog unchanged", relaxed.ToolsJSONBytes, advertised)
	}
}

// An override-served route must not be priced through a protocol's deriver.
//
// An override marshals the definition it was handed, so its received list is its
// wire list. Pricing it through a built-in deriver bills an emitted list the
// override never sends, and an input cap between that figure and the override's
// own estimate rejects a request the override could dispatch.
func TestTheSessionBudgetLeavesAnOverrideServedRouteUnpriced(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "budget-emitted"})
	row := responsesRouteOn("budget-emitted", true)
	profile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()

	if !sess.servesAnOverride(profile) {
		t.Fatal("the registered adapter does not read as serving this profile, so this test cannot see the " +
			"difference it exists to pin")
	}
	defs := parameterlessTools(8)
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}
	advertised := llm.ToolsJSONBytes(defs)
	req.ToolsJSONBytes = advertised

	// The route has to be one the deriver would price, or the guard above is
	// invisible and the test passes for the wrong reason.
	if priced := llm.PriceToolSchemas(req, row); priced.ToolsJSONBytes == advertised {
		t.Fatal("fixture does not distinguish the prices: the deriver left the count unchanged")
	}

	budgeted, _, err := sess.budgetModelDispatchRequestForSession(profile, req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if budgeted.ToolsJSONBytes != advertised {
		t.Fatalf("override-served budget priced the catalog at %d, want the advertised %d: an override "+
			"marshals the definition it was handed, so a built-in deriver bills a list the wire never carries",
			budgeted.ToolsJSONBytes, advertised)
	}
}

// The full-history shadow is taken before the delta is built, and the reading it
// produces is carried past that build: admission takes the larger of the local
// estimate and this one. A route whose builder substitutes a schema therefore has
// to be billed over the emitted list here, or the full-history figure describes a
// catalog the builder has already replaced and the smaller reading is the one
// that governs.
//
// The stub observes the request it is handed rather than the count, because the
// count is what the fix changes.
func TestContinuationShadowBillsTheSchemasTheRouteEmits(t *testing.T) {
	t.Parallel()
	row := responsesRouteOn("budget-emitted", false)
	profile := testOpenAICompatProfile("shadow-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withProfile(profile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	tools := parameterlessTools(24)
	req := llm.Request{
		Provider:    profile.ID(),
		Model:       profile.Model(),
		Messages:    []llm.Message{llm.User("hello")},
		Tools:       tools,
		HistoryMode: llm.HistoryModeResponsesDelta,
	}
	advertised := llm.ToolsJSONBytes(tools)

	var observed []llm.Request
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(r llm.Request) (int, bool) {
		observed = append(observed, r)
		return llm.EstimateInputTokensForResolved(profile.Resolved(), r).Tokens, true
	}
	got := sess.applyResponsesContinuationShadowEstimate(profile, req)

	if len(observed) != 1 {
		t.Fatalf("shadow estimate ran %d times, want once", len(observed))
	}
	if observed[0].ToolsJSONBytes <= advertised {
		t.Fatalf("shadow billed ToolsJSONBytes = %d, want more than the advertised count %d: the route's "+
			"builder substitutes a schema for a parameterless tool, so the full-history reading must bill the "+
			"emitted list", observed[0].ToolsJSONBytes, advertised)
	}
	// The reading actually reaches the field admission takes the maximum of.
	if got.FullHistoryInputTokensEstimate <= 0 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want the priced reading", got.FullHistoryInputTokensEstimate)
	}
	priced := llm.EstimateInputTokensForResolved(profile.Resolved(), llm.PriceToolSchemas(req, profile.Resolved())).Tokens
	if got.FullHistoryInputTokensEstimate < priced {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want at least the emitted-list price %d",
			got.FullHistoryInputTokensEstimate, priced)
	}
}

// The shadow is priced through a protocol's deriver only when a protocol serves
// the request.
//
// An override marshals the definition it was handed, so its received list is its
// wire list. Pricing it through a built-in deriver bills an emitted list the
// override never sends, and an input cap between that figure and the override's
// own estimate rejects a request the override could dispatch. The inflated
// reading also outlives the case that produced it, because it is recorded on a
// request that falls back to full history when the override has no continuation
// planner.
func TestContinuationShadowLeavesAnOverrideServedRouteUnpriced(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "shadow-override"})
	row := responsesRouteOn("shadow-override", true)
	profile := testOpenAICompatProfile("shadow-override", "test", 0).WithResolved(row)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()

	if !sess.servesAnOverride(profile) {
		t.Fatalf("the registered adapter does not read as serving this profile, so this test cannot see the " +
			"difference it exists to pin")
	}
	tools := parameterlessTools(8)
	req := llm.Request{
		Provider:    profile.ID(),
		Model:       profile.Model(),
		Messages:    []llm.Message{llm.User("hello")},
		Tools:       tools,
		HistoryMode: llm.HistoryModeResponsesDelta,
	}
	advertised := llm.ToolsJSONBytes(tools)

	var observed []llm.Request
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(r llm.Request) (int, bool) {
		observed = append(observed, r)
		return llm.EstimateInputTokensForResolved(profile.Resolved(), r).Tokens, true
	}
	sess.applyResponsesContinuationShadowEstimate(profile, req)

	if len(observed) != 1 {
		t.Fatalf("shadow estimate ran %d times, want once", len(observed))
	}
	if got := observed[0].ToolsJSONBytes; got != req.ToolsJSONBytes {
		t.Fatalf("override-served shadow billed ToolsJSONBytes = %d, want the advertised %d: an override "+
			"marshals the definition it was handed, so pricing it through a built-in deriver bills a list "+
			"the wire never carries", got, req.ToolsJSONBytes)
	}
	// The guard has to be doing something: the same request priced through the
	// deriver is larger than the advertised count.
	if priced := llm.PriceToolSchemas(req, row); priced.ToolsJSONBytes <= advertised {
		t.Fatal("fixture does not distinguish the prices: the deriver left the count unchanged")
	}
}

// A fallback is priced for its own route before its history estimates are taken.
//
// The request arrives carrying the PRIMARY's tool count. When the primary's
// builder substitutes a schema and the fallback sends the same catalog unchanged,
// the two counts differ, and both fallback estimates are computed from that
// field. Pricing after them corrects the count the dispatch carries but leaves
// the two readings describing the primary's catalog — and admission takes the
// maximum of the local estimate, the delta estimate and the full-history one, so
// a cap between the two prices refuses a fallback that fits.
func TestTheFallbackPricesItsOwnCatalogBeforeItsHistoryEstimates(t *testing.T) {
	t.Parallel()
	// The primary is a Responses row whose builder substitutes a schema for a
	// parameterless tool, so its count is the larger one.
	primaryRow := registry.Resolved{Instance: "fallback-priced", ModelID: "primary", WireID: "primary"}
	primaryRow.Protocol = registry.ProtocolOpenAIResponses
	primaryRow.Caps.StrictTools = new(false)
	primary := testOpenAICompatProfile("fallback-priced", "primary", 0).WithResolved(primaryRow)

	// The fallback is a Chat route with no strict enforcement, so it sends the
	// catalog exactly as advertised.
	fallbackRow := registry.Resolved{Instance: "fallback-priced", ModelID: "fallback", WireID: "fallback"}
	fallbackRow.Protocol = registry.ProtocolOpenAIChat
	fallbackRow.Caps.StrictTools = new(false)
	fallback := testOpenAICompatProfile("fallback-priced", "fallback", 0).WithResolved(fallbackRow)

	tools := parameterlessTools(24)
	msgs := []llm.Message{llm.User("hello")}
	primaryPrice := llm.EstimateInputTokensForResolved(primaryRow,
		llm.PriceToolSchemas(llm.Request{Messages: msgs, Tools: tools}, primaryRow)).Tokens
	fallbackPrice := llm.EstimateInputTokensForResolved(fallbackRow,
		llm.PriceToolSchemas(llm.Request{Messages: msgs, Tools: tools}, fallbackRow)).Tokens
	if primaryPrice <= fallbackPrice {
		t.Fatalf("fixture does not distinguish the two routes: primary=%d fallback=%d", primaryPrice, fallbackPrice)
	}

	// The request as the fallback loop receives it: the primary's catalog, priced
	// by the primary.
	req := llm.PriceToolSchemas(llm.Request{
		Provider: primary.ID(),
		Model:    primary.Model(),
		Messages: msgs,
		Tools:    tools,
	}, primaryRow)

	// A cap strictly between the two prices. The fallback fits; the primary's
	// reading does not.
	applyCap := func(p *provider.Profile, cap int) *provider.Profile {
		res := p.Resolved()
		res.Caps.MaxInputTokens = new(cap)
		res.Caps.ContextWindow = new(1_000_000)
		return p.WithResolved(res)
	}

	uncapped := applyCap(fallback, 1_000_000)
	sess := newSession(t, withProfile(uncapped), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	built, ok := sess.responsesContinuationFallbackRequest(uncapped, req, msgs)
	if !ok {
		t.Fatal("responsesContinuationFallbackRequest refused a request that carries full history")
	}
	// Pricing the fallback's own catalog is what makes its count its own price.
	if built.ToolsJSONBytes != llm.ToolsJSONBytes(tools) {
		t.Fatalf("fallback carries ToolsJSONBytes = %d, want its own catalog's %d: the Chat builder sends "+
			"this catalog unchanged, so the primary's larger count describes a list this route never emits",
			built.ToolsJSONBytes, llm.ToolsJSONBytes(tools))
	}
	// The two admitted readings are built here rather than read back from the
	// function under test, so a wrong ordering cannot make both sides agree: one
	// form carries the fallback's own catalog price, the other the primary's. The
	// budget then supplies the safety reserve and the maximum-of-three rule, so
	// this test does not restate either.
	ownReq := responsesContinuationFullHistoryWithInputEstimate(fallbackRow, llm.PriceToolSchemas(req, fallbackRow))
	staleReq := responsesContinuationFullHistoryWithInputEstimate(fallbackRow, req)
	_, ownBudget, err := llm.ApplyTokenBudget(ownReq, fallbackRow)
	if err != nil {
		t.Fatalf("budget on the fallback's own price: %v", err)
	}
	_, staleBudget, err := llm.ApplyTokenBudget(staleReq, fallbackRow)
	if err != nil {
		t.Fatalf("budget on the primary's price: %v", err)
	}
	if staleBudget.InputTokens <= ownBudget.InputTokens {
		t.Fatalf("fixture does not distinguish the two readings: own=%d stale=%d",
			ownBudget.InputTokens, staleBudget.InputTokens)
	}

	limit := ownBudget.InputTokens + (staleBudget.InputTokens-ownBudget.InputTokens)/2
	_, _, budgetErr := sess.budgetModelDispatchRequestForSession(applyCap(fallback, limit), built)
	if budgetErr != nil {
		t.Fatalf("fallback refused under a cap between the two prices (%d): %v", limit, budgetErr)
	}
	// The fallback's own limit is still enforced: the same request under a cap
	// below its price is refused rather than dispatched.
	_, _, tightErr := sess.budgetModelDispatchRequestForSession(applyCap(fallback, fallbackPrice-1), built)
	if tightErr == nil {
		t.Fatalf("fallback admitted under a cap of %d below its own price %d: the estimate stopped pricing "+
			"the catalog", fallbackPrice-1, fallbackPrice)
	}
}
