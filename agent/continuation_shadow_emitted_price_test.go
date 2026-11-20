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

// budgetCapBetween returns an input cap strictly between two admissible input
// readings, settling the safety reserve the cap itself decides: the lower price
// fits and the upper one does not.
func budgetCapBetween(t *testing.T, lower, upper int) int {
	t.Helper()
	if upper <= lower {
		t.Fatalf("fixture does not straddle a cap: lower=%d upper=%d", lower, upper)
	}
	// The reserve grows with the cap, so walk the cap up until its own reserve
	// leaves room for the lower price and no room for the upper one.
	limit := lower
	for range 128 {
		next := lower + llm.SafetyReserveTokens(limit) + (upper-lower)/2
		if next == limit {
			break
		}
		limit = next
	}
	if safety := llm.SafetyReserveTokens(limit); lower+safety >= limit {
		t.Fatalf("cap %d does not admit the lower price %d (reserve %d)", limit, lower, safety)
	} else if upper+safety <= limit {
		t.Fatalf("cap %d does not refuse the upper price %d (reserve %d)", limit, upper, safety)
	}
	return limit
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

// preparedParameterlessTools is the same catalog in the form preparation leaves
// it: each declaration carries the open empty object the adapters put on the
// wire, so only the strict decision can replace it. A direct declaration has no
// schema at all, and every route synthesizes one for it, which would make a
// non-strict route grow here and stop this catalog from isolating the
// capability.
func preparedParameterlessTools(n int) []llm.ToolDefinition {
	defs := parameterlessTools(n)
	for i := range defs {
		defs[i] = defs[i].WithNoParametersDeclared()
	}
	return defs
}

// The budget a session uses to decide its warning has to price the tool list the
// route dispatches.
//
// A built-in builder substitutes a schema for a parameterless tool, and those
// bytes occupy context. Budgeting the catalog answers with fewer input tokens,
// so the admitted output comes out larger here than the client later admits:
// the user is warned about an allocation bigger than the one dispatched, or not
// warned at all when the larger reading hides the clamp.
func TestTheSessionBudgetPricesTheEmittedToolSchema(t *testing.T) {
	t.Parallel()
	row := responsesRouteOn("budget-emitted", false)
	defs := parameterlessTools(24)
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}
	// Stamped with the catalog's count, as a caller that measured its own list
	// would.
	advertised := llm.ToolsJSONBytes(defs)
	req.ToolsJSONBytes = advertised

	profile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withProfile(profile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	// The budget function under test is the session's, so compare its reading
	// against the route's own price.
	want := llm.EstimateInputTokensForRoute(row, req, false).Tokens
	if want <= llm.EstimateInputTokensForResolved(row, req).Tokens {
		t.Fatalf("fixture does not distinguish the readings: the route's price %d is not above the catalog's",
			want)
	}
	budgeted, budget, err := sess.priceRoundRoute(profile, req).admit(req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	// The budget adds its own reserve on top of the reading, and it reports that
	// reserve, so the two halves pin the price without restating the rule.
	if budget.SafetyTokens <= 0 {
		t.Fatalf("budget reported no safety reserve: %+v", budget)
	}
	if got, wantTotal := budget.InputTokens, want+budget.SafetyTokens; got != wantTotal {
		t.Fatalf("session budget priced the input at %d, want the emitted list's %d plus the %d reserve = %d: "+
			"the warning it drives would describe an allocation the dispatch never had",
			got, want, budget.SafetyTokens, wantTotal)
	}
	// Admission is a measurement: the caller's catalog stamp survives it.
	if budgeted.ToolsJSONBytes != advertised {
		t.Fatalf("budgeted request carries ToolsJSONBytes = %d, want the caller's %d", budgeted.ToolsJSONBytes, advertised)
	}
}

// A strict route's schema has to change the price the session budgets with, and
// the route without the capability must be the unchanged control.
//
// This is the pair that shows the price follows the emitted bytes rather than
// growing every request: the same request, two routes, one capability apart.
func TestTheSessionBudgetPricesTheStrictProjectionOnlyOnACapableRoute(t *testing.T) {
	t.Parallel()
	// Prepared parameterless declarations, which is where the capability changes
	// these bytes: a certified schema projects without growing, so only the closed
	// empty object a strict route substitutes costs more than the open one every
	// route sends.
	defs := preparedParameterlessTools(24)
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}
	advertised := llm.ToolsJSONBytes(defs)
	req.ToolsJSONBytes = advertised

	strictRow := responsesRouteOn("budget-emitted", true)
	strictProfile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(strictRow)
	strictSess := newSession(t, withProfile(strictProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer strictSess.Close()

	strictWant := llm.EstimateInputTokensForRoute(strictRow, req, false).Tokens
	if strictWant <= llm.EstimateInputTokensForResolved(strictRow, req).Tokens {
		t.Fatalf("fixture does not distinguish the readings: the projected price %d is not above the catalog's",
			strictWant)
	}
	_, strictBudget, err := strictSess.priceRoundRoute(strictProfile, req).admit(req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if got, wantTotal := strictBudget.InputTokens, strictWant+strictBudget.SafetyTokens; got != wantTotal {
		t.Fatalf("strict budget priced the input at %d, want the projection's %d plus the %d reserve = %d",
			got, strictWant, strictBudget.SafetyTokens, wantTotal)
	}

	// The control: the same catalog on a route without the capability is sent
	// unchanged, so its price must be the catalog's count.
	relaxedRow := responsesRouteOn("budget-emitted", false)
	relaxedProfile := testOpenAICompatProfile("budget-emitted", "test", 0).WithResolved(relaxedRow)
	relaxedSess := newSession(t, withProfile(relaxedProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer relaxedSess.Close()

	relaxedWant := llm.EstimateInputTokensForRoute(relaxedRow, req, false).Tokens
	if relaxedWant >= strictWant {
		t.Fatalf("fixture does not separate the routes: relaxed=%d strict=%d", relaxedWant, strictWant)
	}
	_, relaxedBudget, err := relaxedSess.priceRoundRoute(relaxedProfile, req).admit(req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	if got, wantTotal := relaxedBudget.InputTokens, relaxedWant+relaxedBudget.SafetyTokens; got != wantTotal {
		t.Fatalf("route without strict enforcement priced the input at %d, want the catalog's %d plus the %d "+
			"reserve = %d: its builder sends this catalog unchanged",
			got, relaxedWant, relaxedBudget.SafetyTokens, wantTotal)
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
	if projected := llm.EstimateInputTokensForRoute(row, req, false).Tokens; projected == advertised {
		t.Fatal("fixture does not distinguish the routes: the deriver left the count unchanged")
	}

	budgeted, budget, err := sess.priceRoundRoute(profile, req).admit(req)
	if err != nil {
		t.Fatalf("budgetModelDispatchRequestForSession: %v", err)
	}
	overrideWant := llm.EstimateInputTokensForRoute(row, req, true).Tokens
	if got, wantTotal := budget.InputTokens, overrideWant+budget.SafetyTokens; got != wantTotal {
		t.Fatalf("override-served budget priced the input at %d, want the catalog's %d plus the %d reserve = %d: "+
			"an override marshals the definition it was handed, so a built-in deriver bills a list the wire never "+
			"carries", got, overrideWant, budget.SafetyTokens, wantTotal)
	}
	// Admission is a measurement: the caller's catalog stamp survives it.
	if budgeted.ToolsJSONBytes != advertised {
		t.Fatalf("budgeted request carries ToolsJSONBytes = %d, want the catalog's %d", budgeted.ToolsJSONBytes, advertised)
	}
}

// The full-history shadow is taken before the delta is built, and the reading it
// produces is carried past that build: admission takes the larger of the local
// estimate and this one. A route whose builder substitutes a schema therefore has
// to be billed over the emitted list here, or the full-history figure describes a
// catalog the builder has already replaced and the smaller reading is the one
// that governs.
//
// The estimator override is deliberately not installed: this test runs the
// production path, so a shadow that charged the declared catalog would fail
// here. The fixture is a catalog the builder really does grow, and the two
// readings are checked to differ before the assertion so a route that started
// emitting the catalog unchanged cannot make this pass for the wrong reason.
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

	// The route's price and the catalog's are different numbers, which is what
	// makes the assertion below able to tell the two paths apart.
	emittedPrice := llm.EstimateInputTokensForRoute(profile.Resolved(), req, false).Tokens
	catalogPrice := llm.EstimateInputTokensForResolved(profile.Resolved(), req).Tokens
	if emittedPrice <= catalogPrice {
		t.Fatalf("fixture does not distinguish the readings: the route's price %d is not above the catalog's %d",
			emittedPrice, catalogPrice)
	}

	got := sess.applyResponsesContinuationShadowEstimate(sess.priceRoundRoute(profile, req), req)
	if got.FullHistoryInputTokensEstimate != emittedPrice {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want the emitted-list price %d (the catalog reads %d): "+
			"the shadow has to bill the schemas the serving builder emits",
			got.FullHistoryInputTokensEstimate, emittedPrice, catalogPrice)
	}
}

// The shadow is priced through a protocol's deriver only when a protocol serves
// the request.
//
// An override marshals the definition it was handed, so its received list is its
// wire list. Pricing it through a built-in deriver bills an emitted list the
// override never sends, and an input cap between that figure and the override's
// own estimate rejects a request the override could dispatch.
//
// The production path decides this, with no estimator override installed: the
// route's own builder would grow this catalog, and the assertion is that the
// override's reading does not.
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

	// The guard has to be doing something: the same request priced through the
	// deriver is larger than the catalog.
	if projected := llm.EstimateInputTokensForRoute(row, req, false).Tokens; projected <= advertised {
		t.Fatal("fixture does not distinguish the prices: the deriver left the count unchanged")
	}

	got := sess.applyResponsesContinuationShadowEstimate(sess.priceRoundRoute(profile, req), req)

	if got.FullHistoryInputTokensEstimate != llm.EstimateInputTokensForRoute(row, req, true).Tokens {
		t.Fatalf("override-served shadow reading = %d, want the catalog's own %d: an override marshals the "+
			"definition it was handed", got.FullHistoryInputTokensEstimate,
			llm.EstimateInputTokensForRoute(row, req, true).Tokens)
	}
}

// A fallback is priced for its own route before its history estimates are taken.
//
// The request arrives carrying the PRIMARY's tool count. When the primary's
// builder substitutes a schema and the fallback sends the same catalog unchanged,
// the two counts differ, and both fallback estimates are computed for the
// fallback's own route. A fallback priced by the primary's catalog would let a
// cap between the two prices refuse a fallback that fits.
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

	// Enough tools that the two routes' readings differ by more than the
	// minimum safety reserve, so a cap can admit one and refuse the other.
	tools := parameterlessTools(200)
	msgs := []llm.Message{llm.User("hello")}
	bare := llm.Request{Messages: msgs, Tools: tools}
	primaryPrice := llm.EstimateInputTokensForRoute(primaryRow, bare, false).Tokens
	fallbackPrice := llm.EstimateInputTokensForRoute(fallbackRow, bare, false).Tokens
	if primaryPrice <= fallbackPrice {
		t.Fatalf("fixture does not distinguish the two routes: primary=%d fallback=%d", primaryPrice, fallbackPrice)
	}

	// The request as the fallback loop receives it: the catalog the primary
	// priced, stamped by the primary.
	req := bare
	req.Provider = primary.ID()
	req.Model = primary.Model()

	uncapped := fallback
	sess := newSession(t, withProfile(uncapped), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	built, _, ok := sess.responsesContinuationFallbackRequest(uncapped, req, msgs, "")
	if !ok {
		t.Fatal("responsesContinuationFallbackRequest refused a request that carries full history")
	}
	// Both history estimates are the fallback route's own reading: the Chat
	// builder sends this catalog unchanged, so nothing of the primary's price
	// can reach them.
	if got, want := built.InputTokensEstimate, fallbackPrice; got != want {
		t.Fatalf("fallback InputTokensEstimate = %d, want its own route's %d (the primary reads %d)",
			got, want, primaryPrice)
	}
	if got, want := built.FullHistoryInputTokensEstimate, fallbackPrice; got != want {
		t.Fatalf("fallback FullHistoryInputTokensEstimate = %d, want its own route's %d (the primary reads %d)",
			got, want, primaryPrice)
	}
	// Admission is a measurement: the caller's stamp on the request survives it.
	if built.ToolsJSONBytes != req.ToolsJSONBytes {
		t.Fatalf("fallback carries ToolsJSONBytes = %d, want the caller's %d", built.ToolsJSONBytes, req.ToolsJSONBytes)
	}

	// A cap strictly between the two prices admits the fallback and would refuse
	// a dispatch billed at the primary's reading.
	applyCap := func(p *provider.Profile, limit int) *provider.Profile {
		res := p.Resolved()
		res.Caps.MaxInputTokens = new(limit)
		res.Caps.ContextWindow = new(1_000_000)
		return p.WithResolved(res)
	}
	limit := budgetCapBetween(t, fallbackPrice, primaryPrice)
	if _, _, err := sess.priceRoundRoute(applyCap(fallback, limit), built).admit(built); err != nil {
		t.Fatalf("fallback refused under a cap between the two prices (%d): %v", limit, err)
	}
	// The fallback's own limit is still enforced: the same request under a cap
	// below its price is refused rather than dispatched.
	if _, _, err := sess.priceRoundRoute(applyCap(fallback, fallbackPrice-1), built).admit(built); err == nil {
		t.Fatalf("fallback admitted under a cap of %d below its own price %d: the estimate stopped pricing the "+
			"catalog", fallbackPrice-1, fallbackPrice)
	}
}
