package agent

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// typelessConstCatalog is one closed tool whose required argument states a value
// set and no type. A Responses builder states the type the value set implies, so
// this catalog prices larger there than on a route that sends it unchanged —
// which is what makes a route change visible in the count.
func typelessConstCatalog() []llm.ToolDefinition {
	return []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
}

// The budget a session uses to decide its warning has to price the tool list the
// route dispatches.
//
// A built-in builder states a type on a closed typeless value set, and those
// bytes occupy context. Budgeting the advertised catalog answers with fewer
// input tokens, so the admitted output comes out larger here than the client
// later admits: the user is warned about an allocation bigger than the one
// dispatched, or not warned at all when the larger reading hides the clamp.
func TestTheSessionBudgetPricesTheEmittedToolSchema(t *testing.T) {
	t.Parallel()
	// A Responses row that requires explicit types, which is the route whose
	// builder adds them.
	row := registry.Resolved{Instance: "budget-emitted", ModelID: "test", WireID: "test"}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(false)

	defs := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
	req := llm.Request{Messages: []llm.Message{llm.User("hello")}, Tools: defs}

	priced := llm.PriceToolSchemas(req, row)
	if priced.ToolsJSONBytes <= llm.ToolsJSONBytes(defs) {
		t.Fatalf("fixture does not distinguish the prices: advertised=%d priced=%d",
			llm.ToolsJSONBytes(defs), priced.ToolsJSONBytes)
	}
	// The budget function under test is the session's, so build a session on
	// this row and compare its reading against the two prices.
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
	// The row is a Responses route that requires explicit types on every node —
	// the route whose builder states one — so pricing it through the deriver is
	// observable. It is built here rather than taken from the live registry so
	// the test does not depend on which instance happens to be configured.
	row := registry.Resolved{
		Instance: "shadow-override",
		ModelID:  "test",
		WireID:   "test",
	}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(false)
	profile := testOpenAICompatProfile("shadow-override", "test", 0).WithResolved(row)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
	defer sess.Close()

	if !sess.servesAnOverride(profile) {
		t.Fatalf("the registered adapter does not read as serving this profile, so this test cannot see the " +
			"difference it exists to pin")
	}
	// The route has to be one the deriver would price, or the guard above is
	// invisible and the test passes for the wrong reason.
	if priced := llm.PriceToolSchemas(llm.Request{Tools: nil}, row); priced.ToolsJSONBytes != 0 {
		t.Fatalf("an empty catalog was priced, so the fixture is wrong about what the deriver does")
	}

	// A closed typeless value set: a built-in Responses builder states a type on
	// it, which is what makes the protocol price differ from the advertised one.
	tools := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
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
	if observed[0].ToolsJSONBytes == advertised+1 {
		t.Fatalf("fixture is degenerate: the advertised count is the only one in play")
	}
}

// The full-history shadow is taken before the delta is built, and the reading it
// produces is carried past that build: admission takes the larger of the local
// estimate and this one. A route whose builder rewrites tool schemas therefore
// has to be billed over the emitted list here, or the full-history figure
// describes a catalog the builder has already replaced and the smaller reading
// is the one that governs.
//
// The stub observes the request it is handed rather than the count, because the
// count is what the fix changes.
func TestContinuationShadowBillsTheSchemasTheRouteEmits(t *testing.T) {
	t.Parallel()
	//
	// The row is a Responses route that requires explicit types on every node,
	// which is the route whose builder states one. It is built here rather than
	// taken from the live registry so the test does not depend on which
	// instance happens to be configured.
	row := registry.Resolved{
		Instance: "shadow-emitted",
		ModelID:  "test",
		WireID:   "test",
	}
	row.Protocol = registry.ProtocolOpenAIResponses
	row.Caps.StrictTools = new(false)
	profile := testOpenAICompatProfile("shadow-emitted", "test", 0).WithResolved(row)
	sess := newSession(t, withProfile(profile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()

	// A typeless closed value set: the builder states a type on it, which is
	// exactly the difference between the advertised count and the emitted one.
	tools := []llm.ToolDefinition{{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}}
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
		t.Fatalf("shadow billed ToolsJSONBytes = %d, want more than the advertised count %d: the route's builder "+
			"states a type on the closed value set, so the full-history reading must bill the emitted list",
			observed[0].ToolsJSONBytes, advertised)
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

// A fallback is priced for its own route before its history estimates are taken.
//
// The request arrives carrying the PRIMARY's tool count. When the primary adds a
// type and the fallback sends the same catalog unchanged, the two counts differ,
// and both fallback estimates are computed from that field. Pricing after them
// corrects the count the dispatch carries but leaves the two readings describing
// the primary's catalog — and admission takes the maximum of the local estimate,
// the delta estimate and the full-history one, so a cap between the two prices
// refuses a fallback that fits.
//
// The cap is set strictly between the two prices, so the test proves the
// admission direction the finding names and, in the same fixture, that the
// fallback's own limit is still enforced: the larger cap in the second half is
// the same request admitted.
func TestTheFallbackPricesItsOwnCatalogBeforeItsHistoryEstimates(t *testing.T) {
	t.Parallel()
	// The primary is a strict-capable Responses row with a typeless value set in
	// its catalog: its builder states the type, so its count is the larger one.
	primaryRow := registry.Resolved{Instance: "fallback-priced", ModelID: "primary", WireID: "primary"}
	primaryRow.Protocol = registry.ProtocolOpenAIResponses
	primaryRow.Caps.StrictTools = new(false)
	primary := testOpenAICompatProfile("fallback-priced", "primary", 0).WithResolved(primaryRow)

	// The fallback is a same-surface Chat route with no strict enforcement, so it
	// sends the catalog exactly as advertised.
	fallbackRow := registry.Resolved{Instance: "fallback-priced", ModelID: "fallback", WireID: "fallback"}
	fallbackRow.Protocol = registry.ProtocolOpenAIChat
	fallbackRow.Caps.StrictTools = new(false)
	fallback := testOpenAICompatProfile("fallback-priced", "fallback", 0).WithResolved(fallbackRow)

	tools := typelessConstCatalog()
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
	sess := newSession(t, withProfile(uncapped), withDir(t.TempDir()),
		withConfig(SessionConfig{}))
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
