package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// countedSchemaValue counts how often the encoder marshals it. A tool catalog
// that contains one can be measured twice without the second measurement
// changing the number, so a test that compares counts cannot tell reuse from
// re-measurement. Counting the marshals themselves can.
type countedSchemaValue struct{ calls *int32 }

func (c countedSchemaValue) MarshalJSON() ([]byte, error) {
	atomic.AddInt32(c.calls, 1)
	return []byte(`"counted"`), nil
}

// preparedBudgetAdapter counts dispatches for the prepared-parameterless
// budget test. It answers successfully so a request the budget should have
// blocked shows up as a call rather than as an adapter error.
type preparedBudgetAdapter struct {
	mu    sync.Mutex
	calls int
}

func (*preparedBudgetAdapter) Name() string { return "prepared-budget" }

func (a *preparedBudgetAdapter) Complete(context.Context, Request) (Response, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return Response{Message: Assistant("ok")}, nil
}

func (a *preparedBudgetAdapter) Stream(context.Context, Request) (Stream, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	s := NewChanStream(func() {})
	go func() {
		defer s.CloseSend()
		s.Send(StreamEvent{Type: StreamEventStreamStart})
		s.Send(StreamEvent{Type: StreamEventFinish, Response: &Response{Message: Assistant("ok")}})
	}()
	return s, nil
}

func (a *preparedBudgetAdapter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// preparedBudgetClient builds a client whose instance carries the given
// max-input cap, registers adapter, and returns the row the client dispatches
// to.
func preparedBudgetClient(t *testing.T, adapter ProviderAdapter, maxInput int) (*Client, registry.Resolved) {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"prepared-budget": {
				Base:   "openai",
				APIKey: "unused",
				Caps: registry.Caps{
					ContextWindow:   new(1_000_000),
					MaxInputTokens:  new(maxInput),
					MaxOutputTokens: new(4_096),
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	c := NewClient(WithRegistry(r))
	res, err := c.Resolve("prepared-budget/model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Caps.MaxInputTokens == nil || *res.Caps.MaxInputTokens != maxInput {
		t.Fatalf("resolved max_input = %v, want %d", res.Caps.MaxInputTokens, maxInput)
	}
	c.Register(adapter)
	return c, res
}

// budgetClientOnRoute builds a client whose instance carries maxInput and the
// given capability overrides. It exists so a test can name the route it prices:
// a row without strict enforcement still reaches the same Responses endpoint,
// which requires an explicit type on every node.
func budgetClientOnRoute(t *testing.T, adapter ProviderAdapter, maxInput int, protocol string, caps registry.Caps) (*Client, registry.Resolved) {
	t.Helper()
	caps.ContextWindow = new(1_000_000)
	caps.MaxInputTokens = new(maxInput)
	caps.MaxOutputTokens = new(4_096)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"prepared-budget": {Base: "openai", Protocol: protocol, APIKey: "unused", Caps: caps},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	c := NewClient(WithRegistry(r))
	res, err := c.Resolve("prepared-budget/model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Caps.MaxInputTokens == nil || *res.Caps.MaxInputTokens != maxInput {
		t.Fatalf("resolved max_input = %v, want %d", res.Caps.MaxInputTokens, maxInput)
	}
	c.Register(adapter)
	return c, res
}

// typelessConstTool is a closed tool whose required argument states a value set
// and no type: the shape the endpoint rejects on the way in even when strict
// enforcement is off.
func typelessConstTool() ToolDefinition {
	return ToolDefinition{
		Name: "set_mode",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		},
	}
}

// preparedToolLists returns the same tool names twice: once as preparation
// leaves a parameterless tool (nil Parameters), and once as the override
// boundary restores it (the open empty schema).
func preparedToolLists(toolCount int) (parameterless, restored []ToolDefinition) {
	parameterless = make([]ToolDefinition, toolCount)
	restored = make([]ToolDefinition, toolCount)
	for i := range parameterless {
		name := fmt.Sprintf("probe_%02d", i)
		parameterless[i] = ToolDefinition{Name: name}
		restored[i] = ToolDefinition{Name: name, Parameters: defaultToolParameters()}
	}
	return parameterless, restored
}

func preparedRequest(defs []ToolDefinition) Request {
	req := Request{
		Provider: "prepared-budget",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	req.ToolsJSONBytes = ToolsJSONBytes(defs)
	req.preparedTools = true
	return req
}

// TestPreparedParameterlessToolIsPricedIntoAdmission pins the order of the two
// steps an override request passes before dispatch: the parameterless fallback
// restores the open empty schema, and only then does the token budget price
// the tool list.
//
// A tool that declares no parameters reaches preparation with a nil
// Parameters, and preparation keeps it (see prepareTools). The override
// boundary restores the open empty schema, which adds bytes to the list the
// estimate bills. Admitting the request on a count that omits those bytes
// dispatches a request the budget would have rejected: it is priced as one
// list and sent as a larger one. The cap below sits strictly between the two
// prices, so the request is admitted only if the restore is priced.
func TestPreparedParameterlessToolIsPricedIntoAdmission(t *testing.T) {
	const toolCount = 24
	parameterless, restored := preparedToolLists(toolCount)

	// Prices are properties of the message history and the tool bytes, not of
	// the cap, so the row used to measure them does not matter; the assertions
	// below re-measure against the capped row before relying on it.
	_, probeRow := preparedBudgetClient(t, &preparedBudgetAdapter{}, 1_000_000)
	unrestoredInput := EstimateInputTokensForResolved(probeRow, preparedRequest(parameterless)).Tokens
	restoredInput := EstimateInputTokensForResolved(probeRow, preparedRequest(restored)).Tokens

	delta := restoredInput - unrestoredInput
	if want := (ToolsJSONBytes(restored) - ToolsJSONBytes(parameterless)) / 4; delta != want {
		t.Fatalf("restoring the open schema moved the estimate by %d tokens, want %d: "+
			"the difference must be the tool-byte difference alone, with the message share identical on both sides",
			delta, want)
	}
	if delta < 8 {
		t.Fatalf("fixture too small to straddle a cap: the restore moves the price by %d tokens", delta)
	}

	// Choose a cap that fits the unrestored price and not the restored one.
	// The safety reserve depends on the cap it is added under, so settle the
	// two together.
	limit := unrestoredInput + minimumTokenSafetyReserve + delta/2
	for range 16 {
		next := unrestoredInput + SafetyReserveTokens(limit) + delta/2
		if next == limit {
			break
		}
		limit = next
	}
	safety := SafetyReserveTokens(limit)
	if safety != SafetyReserveTokens(limit) {
		t.Fatalf("safety reserve unstable at cap %d", limit)
	}
	if unrestoredInput+safety >= limit {
		t.Fatalf("cap %d does not admit the unrestored price %d", limit, unrestoredInput+safety)
	}
	if restoredInput+safety <= limit {
		t.Fatalf("cap %d does not refuse the restored price %d", limit, restoredInput+safety)
	}

	tests := []struct {
		name string
		call func(*Client, Request) error
	}{
		{
			name: "complete",
			call: func(c *Client, req Request) error {
				_, err := c.Complete(context.Background(), req)
				return err
			},
		},
		{
			name: "stream",
			call: func(c *Client, req Request) error {
				st, err := c.Stream(context.Background(), req)
				if err != nil {
					return err
				}
				for range st.Events() {
				}
				return st.Close()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &preparedBudgetAdapter{}
			c, res := preparedBudgetClient(t, adapter, limit)

			// The capped row prices the list exactly as the probe row did.
			if got := EstimateInputTokensForResolved(res, preparedRequest(parameterless)).Tokens; got != unrestoredInput {
				t.Fatalf("unrestored price under the capped row = %d, want %d", got, unrestoredInput)
			}
			if got := EstimateInputTokensForResolved(res, preparedRequest(restored)).Tokens; got != restoredInput {
				t.Fatalf("restored price under the capped row = %d, want %d", got, restoredInput)
			}

			err := tt.call(c, preparedRequest(parameterless))
			var budgetErr *ContextBudgetError
			if !errors.As(err, &budgetErr) {
				t.Fatalf("error = %v, want *ContextBudgetError: the request was admitted on %d input tokens, "+
					"the price of the list before the open schema is restored (%d once restored), "+
					"and the adapter was dispatched %d time(s)",
					err, unrestoredInput+safety, restoredInput+safety, adapter.callCount())
			}
			if budgetErr.Limit != "max_input" || budgetErr.Maximum != limit {
				t.Fatalf("budget error = %+v, want max_input limit %d", budgetErr, limit)
			}
			if got := adapter.callCount(); got != 0 {
				t.Fatalf("adapter calls = %d, want 0: a blocked request must not dispatch", got)
			}
			if want := restoredInput + safety; budgetErr.InputTokens != want {
				t.Fatalf("admitted input = %d, want %d: the count without the restored schema is %d",
					budgetErr.InputTokens, want, unrestoredInput+safety)
			}
		})
	}
}

// TestRestoringTheOpenSchemaReusesThePreparedPricing pins that the override
// boundary does not re-serialize the catalog it was handed.
//
// Preparation measures the tool list once, over the form the adapters send
// (the open empty schema substituted for a parameterless definition), and
// stamps that count on the request. Restoring the same substitution over the
// same list reproduces exactly those bytes, so re-measuring repeats a marshal
// the stamp exists to avoid — once per tool round and once per retry.
//
// The measurement is counted at the encoder rather than compared as a number:
// a second marshal of the same list yields the same byte count, so an
// assertion on the count cannot distinguish reuse from re-measurement. The
// schema value below reports every marshal, and the expected number is one —
// the stamp preparation already took, which this test takes itself so the
// fixture's own measurement is not confused with the boundary's.
//
// The catalog holds a parameterless tool as well, because that is what makes
// the boundary act at all: a list with no nil Parameters is returned untouched
// and would pass this test without measuring anything.
func TestRestoringTheOpenSchemaReusesThePreparedPricing(t *testing.T) {
	t.Parallel()
	var marshals int32
	defs := []ToolDefinition{
		{Name: "parameterless"},
		{
			Name: "probe",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"payload": countedSchemaValue{calls: &marshals}},
			},
		},
	}
	req := Request{Tools: defs, preparedTools: true}
	// Preparation measures the substituted form, so this is the count it stamps.
	req.ToolsJSONBytes = ToolsJSONBytes(asSentToolDefs(defs))
	req = MarkPreparedToolBytes(req)
	if got := atomic.LoadInt32(&marshals); got != 1 {
		t.Fatalf("measurement marshals = %d, want 1: the fixture must be priced once before the boundary runs", got)
	}

	restored := withOpenParameterlessSchemas(req)
	if got := atomic.LoadInt32(&marshals); got != 1 {
		t.Fatalf("marshals after restoring the open schema = %d, want 1: "+
			"the boundary re-measured a catalog preparation had already priced, and that count describes "+
			"the same substituted list (%d bytes)", got, req.ToolsJSONBytes)
	}
	if got, want := restored.ToolsJSONBytes, req.ToolsJSONBytes; got != want {
		t.Fatalf("restored count = %d, want %d: the reused count must be the one preparation stamped", got, want)
	}
	if restored.Tools[0].Parameters == nil {
		t.Fatal("the boundary must still restore the open schema, or this test would pass without acting")
	}

	// A count that does not describe the substituted list is not reusable: it is
	// replaced, because trusting it would price a list other than the one sent.
	unpriced := Request{Tools: defs, preparedTools: true}
	unpriced.ToolsJSONBytes = ToolsJSONBytes(defs)
	restored = withOpenParameterlessSchemas(unpriced)
	if got := atomic.LoadInt32(&marshals); got != 3 {
		t.Fatalf("marshals after an unmarked count = %d, want 3: a count that was not measured over the "+
			"substituted list must be replaced rather than trusted", got)
	}
	if want := ToolsJSONBytes(asSentToolDefs(defs)); restored.ToolsJSONBytes != want {
		t.Fatalf("remeasured count = %d, want %d", restored.ToolsJSONBytes, want)
	}
}

// TestAReplacedCatalogIsRepricedAfterTheOpenSchemaIsRestored covers the stamp
// that survives a catalog replacement.
//
// Middleware may replace a request's Tools and refresh ToolsJSONBytes, as the
// field's contract allows. The prepared-tools marker stays set, because the
// request still came from preparation. A count refreshed over the replacement
// list is measured before the override boundary substitutes the open schema, so
// reusing it prices the request smaller than what dispatches — and an input cap
// between the two prices admits a request the budget would have refused.
//
// The count is tied to the list it was measured over, so a replacement never
// matches and is always repriced.
func TestAReplacedCatalogIsRepricedAfterTheOpenSchemaIsRestored(t *testing.T) {
	t.Parallel()
	original := []ToolDefinition{{Name: "parameterless"}}
	req := Request{Tools: original, preparedTools: true}
	req.ToolsJSONBytes = ToolsJSONBytes(asSentToolDefs(original))
	req = MarkPreparedToolBytes(req)

	// Middleware swaps in a larger catalog and refreshes the count over it as
	// documented, before the open schema is substituted for its parameterless
	// members.
	replacement := []ToolDefinition{
		{Name: "parameterless"},
		{Name: "also_parameterless"},
		{Name: "third", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
	}
	req.Tools = replacement
	req.ToolsJSONBytes = ToolsJSONBytes(replacement)
	understated := req.ToolsJSONBytes

	restored := withOpenParameterlessSchemas(req)
	want := ToolsJSONBytes(asSentToolDefs(replacement))
	if restored.ToolsJSONBytes != want {
		t.Fatalf("restored count = %d, want %d: a count refreshed over the replacement catalog was measured "+
			"before the open schema was substituted, and reusing it prices the request at %d",
			restored.ToolsJSONBytes, want, understated)
	}
	if restored.ToolsJSONBytes <= understated {
		t.Fatalf("restored count = %d, must exceed the pre-substitution count %d", restored.ToolsJSONBytes, understated)
	}
}

// TestStrictCapableRoutePricesTheClosedParameterlessSchema covers the other end
// of the same undercount.
//
// Preparation stamps a parameterless tool over the open empty schema, because
// that is what a route without strict enforcement sends. A strict-capable
// built-in route sends the larger closed object instead — the one shape strict
// mode accepts for a tool with no arguments — so admitting the request on the
// prepared count prices it smaller than what dispatches. With enough such tools
// and a cap between the two prices, the request goes out over budget.
//
// What this test proves is the price and the capability split: the delta is the
// two schemas' difference, an opted-out tool does not carry it, and a route
// without the capability keeps the open price. It does not drive a built-in
// adapter through dispatch, because the test client here registers an override
// and the override branch correctly keeps the open price; the call site on the
// built-in branch is the one line in Complete and StreamGenerate's dispatch
// that applies this helper.
func TestStrictCapableRoutePricesTheClosedParameterlessSchema(t *testing.T) {
	t.Parallel()
	const toolCount = 24
	defs := make([]ToolDefinition, toolCount)
	for i := range defs {
		defs[i] = ToolDefinition{Name: fmt.Sprintf("probe_%02d", i)}
	}
	openBytes := ToolsJSONBytes(asSentToolDefs(defs))

	// The prepared-budget row builds on the openai base, which declares strict
	// tools: the route that sends the closed form.
	_, res := preparedBudgetClient(t, &preparedBudgetAdapter{}, 1_000_000)
	if !registry.BoolValue(res.Caps.StrictTools) {
		t.Fatal("fixture route does not declare strict tools; the priced case needs a strict-capable row")
	}
	// The closed price is what the route's own builder emits, not a second
	// expression of the closed schema written here: the point of the rule is
	// that the price comes from the builder.
	closed, changed := emitToolSchemas(t, res, defs)
	if !changed {
		t.Fatal("fixture route does not change a parameterless tool's schema; the priced case needs the closed form")
	}
	closedBytes := ToolsJSONBytes(closed)
	if closedBytes <= openBytes {
		t.Fatalf("fixture does not distinguish the two prices: open=%d closed=%d", openBytes, closedBytes)
	}

	req := preparedRequest(defs)
	req.ToolsJSONBytes = openBytes
	if got := priceEmittedToolSchemas(req, res); got.ToolsJSONBytes != closedBytes {
		t.Fatalf("strict-capable count = %d, want %d: admission must price the closed schema the route emits",
			got.ToolsJSONBytes, closedBytes)
	}

	// An opted-out tool keeps the open schema, so it must not be priced closed.
	optedOut := make([]ToolDefinition, len(defs))
	copy(optedOut, defs)
	optedOut[0].Strict = new(false)
	optedOutReq := preparedRequest(optedOut)
	optedOutReq.ToolsJSONBytes = openBytes
	optedOutSent, _ := emitToolSchemas(t, res, optedOut)
	wantOptedOut := ToolsJSONBytes(optedOutSent)
	if wantOptedOut >= closedBytes {
		t.Fatalf("fixture does not distinguish the opt-out: with one opt-out %d, all closed %d",
			wantOptedOut, closedBytes)
	}
	if got := priceEmittedToolSchemas(optedOutReq, res); got.ToolsJSONBytes != wantOptedOut {
		t.Fatalf("count with one opt-out = %d, want %d: an opted-out tool is sent open and must not be "+
			"priced closed", got.ToolsJSONBytes, wantOptedOut)
	}

	// The control: the same catalog on a route without the capability keeps the
	// open price, because that is the schema such a route sends.
	off := res
	off.Caps.StrictTools = nil
	if got := priceEmittedToolSchemas(req, off); got.ToolsJSONBytes != openBytes {
		t.Fatalf("capability-off count = %d, want %d: a route without strict enforcement sends the open schema",
			got.ToolsJSONBytes, openBytes)
	}
}

// emitToolSchemas asks the route's protocol what its builder will emit, which
// is the only expression of that answer the tests are allowed to price against.
func emitToolSchemas(t *testing.T, res registry.Resolved, baseline []ToolDefinition) ([]ToolDefinition, bool) {
	t.Helper()
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol registered for %q", res.Protocol)
	}
	deriver, ok := p.(ToolSchemaDeriver)
	if !ok {
		t.Fatalf("protocol %q does not derive tool schemas; the priced case needs a route whose builder does", res.Protocol)
	}
	return deriver.EmitToolSchemas(baseline, res, true)
}

// TestARawToolCountIsRepricedToTheStrictForm covers the count that preparation
// never measured.
//
// ToolsJSONBytes is documented as a count a caller may stamp from its own tool
// list, so a direct Complete or Stream call can arrive with a count taken over
// the definitions as written. parameters is omitempty, so a parameterless tool
// contributes nothing to that count at all, while the builder emits a schema for
// it. A count like that undercounts the dispatched list, and near the input cap
// it admits a request that dispatches larger than it was billed.
//
// The rule is the same for both counts because the protocol answers for both:
// the deriver is handed the form the count covered and answers with the form the
// wire carries, so a raw count and a prepared count reach the same price. The
// open price stays in the test as the control, because a rule that priced the
// received definitions would land there.
func TestARawToolCountIsRepricedToTheStrictForm(t *testing.T) {
	t.Parallel()
	const toolCount = 24
	defs := make([]ToolDefinition, toolCount)
	for i := range defs {
		defs[i] = ToolDefinition{Name: fmt.Sprintf("probe_%02d", i)}
	}
	rawBytes := ToolsJSONBytes(defs)
	openBytes := ToolsJSONBytes(asSentToolDefs(defs))
	if rawBytes >= openBytes {
		t.Fatalf("fixture does not distinguish a raw count from an open one: raw=%d open=%d", rawBytes, openBytes)
	}

	_, res := preparedBudgetClient(t, &preparedBudgetAdapter{}, 1_000_000)
	if !registry.BoolValue(res.Caps.StrictTools) {
		t.Fatal("fixture route does not declare strict tools; the priced case needs a strict-capable row")
	}
	strictSent, changed := emitToolSchemas(t, res, defs)
	if !changed {
		t.Fatal("fixture route does not change a parameterless tool's schema; the priced case needs the closed form")
	}
	strictBytes := ToolsJSONBytes(strictSent)
	if strictBytes <= openBytes {
		t.Fatalf("fixture does not distinguish the open and closed forms: open=%d closed=%d", openBytes, strictBytes)
	}

	// A direct call: no preparation, so the count is the caller's own and no
	// prepared list is recorded for it.
	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: rawBytes,
	}
	if got := priceEmittedToolSchemas(req, res); got.ToolsJSONBytes != strictBytes {
		t.Fatalf("raw count priced to %d, want %d: a count taken over the definitions as written excludes "+
			"the parameterless schema the builder emits (raw %d, open %d)",
			got.ToolsJSONBytes, strictBytes, rawBytes, openBytes)
	}

	// A prepared count reaches the same price: the deriver is handed the form
	// the count covered, so the two entry points cannot disagree.
	prepared := req
	prepared.ToolsJSONBytes = openBytes
	prepared = MarkPreparedToolBytes(prepared)
	if got := priceEmittedToolSchemas(prepared, res); got.ToolsJSONBytes != strictBytes {
		t.Fatalf("prepared count priced to %d, want %d: an open count plus the emitted closed form is the "+
			"same list the raw count reaches", got.ToolsJSONBytes, strictBytes)
	}

	// The control: a route without the capability emits the open schema, so the
	// count it is handed already describes what dispatches and must not move.
	off := res
	off.Caps.StrictTools = nil
	if got := priceEmittedToolSchemas(prepared, off); got.ToolsJSONBytes != openBytes {
		t.Fatalf("capability-off count = %d, want %d: a route that sends the open schema prices the open list",
			got.ToolsJSONBytes, openBytes)
	}

	// The price is only worth asserting because admission reads it. A cap
	// between the raw price and the closed price has to refuse, and it can only
	// do so if the price reaches admission from inside it.
	rawInput := EstimateInputTokensForResolved(res, req).Tokens
	strictPriced := req
	strictPriced.ToolsJSONBytes = strictBytes
	strictInput := EstimateInputTokensForResolved(res, strictPriced).Tokens
	if strictInput <= rawInput {
		t.Fatalf("fixture does not straddle a cap: raw price %d, closed price %d", rawInput, strictInput)
	}
	limit := rawInput + minimumTokenSafetyReserve + (strictInput-rawInput)/2
	for range 16 {
		next := rawInput + SafetyReserveTokens(limit) + (strictInput-rawInput)/2
		if next == limit {
			break
		}
		limit = next
	}
	safety := SafetyReserveTokens(limit)
	if rawInput+safety >= limit {
		t.Fatalf("cap %d does not admit the raw price %d", limit, rawInput)
	}
	if strictInput+safety <= limit {
		t.Fatalf("cap %d does not refuse the closed price %d", limit, strictInput)
	}
	_, cappedRes := preparedBudgetClient(t, &preparedBudgetAdapter{}, limit)
	if _, err := shapeAndApplyTokenBudget(req, cappedRes, true); err == nil {
		t.Fatalf("admission accepted a request billed at %d under a cap of %d: the closed form costs %d "+
			"and the raw count %d does not include it", rawInput+safety, limit, strictInput+safety, rawInput+safety)
	}
}

// TestARouteWithoutStrictEnforcementPricesTheTypesItAdds covers the count on a
// route that never enters strict mode.
//
// The explicit-type requirement belongs to the schema dialect, not to the
// strict-tools capability: the same Responses endpoint checks it with strict
// enforcement off, and a tool carrying a typeless closed value set is rejected
// along with the whole request. The builder supplies the type on that path, so
// the dispatched list is larger than the one the count measured, and a cap
// between the two prices admits a request over budget.
//
// The discriminating case is a prepared count. Preparation measured the catalog
// before any builder ran, so the count describes the open form and the missing
// bytes are exactly the supplied type. A rule keyed on the strict capability
// instead of the dialect would leave this count alone and admit the request.
func TestARouteWithoutStrictEnforcementPricesTheTypesItAdds(t *testing.T) {
	t.Parallel()
	const toolCount = 16
	defs := make([]ToolDefinition, toolCount)
	for i := range defs {
		defs[i] = typelessConstTool()
		defs[i].Name = fmt.Sprintf("set_mode_%02d", i)
	}
	_, res := budgetClientOnRoute(t, &preparedBudgetAdapter{}, 1_000_000, "", registry.Caps{StrictTools: new(false)})
	if registry.BoolValue(res.Caps.StrictTools) {
		t.Fatal("fixture route declares strict tools; the case under test is the route that does not")
	}
	emitted, changed := emitToolSchemas(t, res, defs)
	if !changed {
		t.Fatal("fixture route adds nothing to the tool schema; the priced case needs the supplied type")
	}
	measuredBytes := ToolsJSONBytes(asSentToolDefs(defs))
	emittedBytes := ToolsJSONBytes(emitted)
	if emittedBytes <= measuredBytes {
		t.Fatalf("fixture does not distinguish the two prices: measured=%d emitted=%d", measuredBytes, emittedBytes)
	}

	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: measuredBytes,
	}
	req = MarkPreparedToolBytes(req)
	if got := priceEmittedToolSchemas(req, res); got.ToolsJSONBytes != emittedBytes {
		t.Fatalf("count = %d, want %d: the builder supplies a type on this route, so the dispatched list is "+
			"larger than the one preparation measured", got.ToolsJSONBytes, emittedBytes)
	}

	// A cap between the two prices: admission must read the priced count, so it
	// has to refuse the request the unpriced count would have admitted.
	measuredInput := EstimateInputTokensForResolved(res, req).Tokens
	priced := req
	priced.ToolsJSONBytes = emittedBytes
	pricedInput := EstimateInputTokensForResolved(res, priced).Tokens
	if pricedInput <= measuredInput {
		t.Fatalf("fixture does not straddle a cap: measured price %d, emitted price %d", measuredInput, pricedInput)
	}
	limit := measuredInput + minimumTokenSafetyReserve + (pricedInput-measuredInput)/2
	for range 16 {
		next := measuredInput + SafetyReserveTokens(limit) + (pricedInput-measuredInput)/2
		if next == limit {
			break
		}
		limit = next
	}
	safety := SafetyReserveTokens(limit)
	if measuredInput+safety >= limit {
		t.Fatalf("cap %d does not admit the measured price %d", limit, measuredInput)
	}
	if pricedInput+safety <= limit {
		t.Fatalf("cap %d does not refuse the emitted price %d", limit, pricedInput)
	}
	_, cappedRes := budgetClientOnRoute(t, &preparedBudgetAdapter{}, limit, "", registry.Caps{StrictTools: new(false)})
	if _, err := shapeAndApplyTokenBudget(req, cappedRes, true); err == nil {
		t.Fatalf("admission accepted a request billed at %d under a cap of %d: the emitted list costs %d "+
			"and the measured count does not include the supplied type",
			measuredInput+safety, limit, pricedInput+safety)
	}
}

// TestADirectChatCallIsPricedAsTheChatBuilderEmitsIt covers the route whose
// builder omits a parameterless tool's schema entirely.
//
// Chat Completions substitutes an open empty parameters object only when the
// request came through preparation, because only then does the local validator
// compile the tool against that schema. A direct Complete or Stream call keeps
// the caller's nil, and ToChatTools omits the key, so the raw count over the
// definitions as written is already the dispatched price. Repricing that count
// from a parameterless tool's schema would charge for bytes the wire never
// carries, and a cap between the two estimates would refuse a request that
// fits — including for a tool that opted out, whose parameters key is omitted
// on this route either way.
func TestADirectChatCallIsPricedAsTheChatBuilderEmitsIt(t *testing.T) {
	t.Parallel()
	// The catalog is the shapes the two rules disagree about. A strict-capable
	// Chat route sends a parameterless tool with the closed empty schema even on
	// a direct call, and it sends a schema the gate refuses with the types that
	// schema implies. The opted-out tool keeps the omission a direct call has
	// always had. The count therefore has to move for two of the three, and the
	// expectation below is read from BuildBody rather than written here.
	defs := []ToolDefinition{
		{Name: "strict_one", Strict: new(true)},
		{Name: "opted_out", Strict: new(false)},
		{Name: "typed_gate", Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"payload": map[string]any{"const": "fixed"},
			},
			"required": []string{"payload"},
		}},
	}
	rawBytes := ToolsJSONBytes(defs)

	_, res := budgetClientOnRoute(t, &preparedBudgetAdapter{}, 1_000_000, registry.ProtocolOpenAIChat,
		registry.Caps{StrictTools: new(true)})
	if res.Protocol != registry.ProtocolOpenAIChat {
		t.Fatalf("fixture route resolves to %q, want the Chat Completions protocol", res.Protocol)
	}
	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: rawBytes,
	}
	// The oracle is the builder's own body. The agreement this test exists to
	// check is that the price describes the schemas BuildBody carries: a price
	// written here would be a second opinion about the same question, which is
	// the defect the test was added to catch.
	assertChatWireMatchesPrice(t, req, res)
	emitted := pricedChatDefs(t, req, res)
	emittedBytes := ToolsJSONBytes(emitted)
	if emittedBytes <= rawBytes {
		t.Fatalf("fixture does not distinguish the two prices: advertised=%d emitted=%d: a strict-capable "+
			"Chat route has to change something for this test to mean anything", rawBytes, emittedBytes)
	}
	if got := priceEmittedToolSchemas(req, res); got.ToolsJSONBytes != emittedBytes {
		t.Fatalf("priced count = %d, want %d: admission must price the schemas BuildBody emits "+
			"(advertised count %d)", got.ToolsJSONBytes, emittedBytes, rawBytes)
	}

	// The opted-out tool is the half that must not move: the builder omits its
	// parameters key on a direct call, so it contributes the bytes it always
	// did. A rule that substituted a schema for it would fail this.
	optOnly := Request{Provider: "prepared-budget", Model: "model", Messages: req.Messages,
		Tools: defs[1:2], ToolsJSONBytes: ToolsJSONBytes(defs[1:2])}
	if got := priceEmittedToolSchemas(optOnly, res); got.ToolsJSONBytes != optOnly.ToolsJSONBytes {
		t.Fatalf("opted-out direct call priced to %d, want %d: the builder omits its parameters key, so the "+
			"advertised count is the dispatched price", got.ToolsJSONBytes, optOnly.ToolsJSONBytes)
	}

	// A cap between the two prices: admission reads the priced count, so the
	// request it must refuse is the one the advertised count would have admitted.
	advertisedInput := EstimateInputTokensForResolved(res, req).Tokens
	priced := req
	priced.ToolsJSONBytes = emittedBytes
	pricedInput := EstimateInputTokensForResolved(res, priced).Tokens
	if pricedInput <= advertisedInput {
		t.Fatalf("fixture does not straddle a cap: advertised price %d, emitted price %d",
			advertisedInput, pricedInput)
	}
	limit := advertisedInput + minimumTokenSafetyReserve + (pricedInput-advertisedInput)/2
	for range 16 {
		next := advertisedInput + SafetyReserveTokens(limit) + (pricedInput-advertisedInput)/2
		if next == limit {
			break
		}
		limit = next
	}
	safety := SafetyReserveTokens(limit)
	if advertisedInput+safety >= limit {
		t.Fatalf("cap %d does not admit the advertised price %d", limit, advertisedInput)
	}
	if pricedInput+safety <= limit {
		t.Fatalf("cap %d does not refuse the emitted price %d", limit, pricedInput)
	}
	_, cappedRes := budgetClientOnRoute(t, &preparedBudgetAdapter{}, limit, registry.ProtocolOpenAIChat,
		registry.Caps{StrictTools: new(true)})
	if _, err := shapeAndApplyTokenBudget(req, cappedRes, true); err == nil {
		t.Fatalf("admission accepted a request billed at %d under a cap of %d: the emitted list costs %d "+
			"and the advertised count does not include the schemas the builder supplies",
			advertisedInput+safety, limit, pricedInput+safety)
	}
}

// assertChatWireMatchesPrice fails unless every schema the Chat builder puts on
// the wire is the schema the deriver prices for the same tool, and unless the
// builder's own presence-or-absence of the parameters key agrees with the
// deriver's. That is the property, and it is checked pairwise rather than by
// re-serializing the body: the wire entry and the definition list are different
// JSON shapes, so their byte counts are not comparable, but their schemas are.
func assertChatWireMatchesPrice(t *testing.T, req Request, res registry.Resolved) {
	t.Helper()
	wire := chatWireToolParams(t, req, res)
	emitted := pricedChatDefs(t, req, res)
	if len(wire) != len(emitted) {
		t.Fatalf("wire carries %d tools, price covers %d", len(wire), len(emitted))
	}
	for i := range wire {
		wireParams := wire[i]
		pricedParams := emitted[i].Parameters
		if (wireParams != nil) != (pricedParams != nil) {
			t.Fatalf("tool %d (%s): builder emits parameters present=%v, price describes present=%v",
				i, emitted[i].Name, wireParams != nil, pricedParams != nil)
		}
		if wireParams == nil {
			continue
		}
		if !reflect.DeepEqual(wireParams, pricedParams) {
			t.Fatalf("tool %d (%s): builder emits %v, price describes %v",
				i, emitted[i].Name, wireParams, pricedParams)
		}
	}
}

// A prepared request's opt-out is a statement about enforcement, not about which
// schema the validator saw.
//
// Preparation compiles a parameterless tool against the open empty object, so a
// prepared opt-out still has to carry that schema on the wire. The opt-out
// decides only that it does not ride strict enforcement. Resolving the opt-out
// before choosing the parameterless fallback reads it the other way: the
// builder omits the key, the validator keeps compiling against the open object,
// and when the opted-out tool is the whole catalog, admission bills a cached
// open-schema charge for bytes the builder never sends.
func TestAPreparedOptOutStillCarriesTheValidatedSchema(t *testing.T) {
	t.Parallel()
	defs := []ToolDefinition{{Name: "parameterless", Strict: new(false)}}
	req := Request{
		Provider:       "prepared-budget",
		Model:          "model",
		Messages:       []Message{User("x")},
		Tools:          defs,
		ToolsJSONBytes: ToolsJSONBytes(defs),
	}
	req = MarkPreparedTools(req)
	req = MarkPreparedToolBytes(req)

	_, res := budgetClientOnRoute(t, &preparedBudgetAdapter{}, 1_000_000, registry.ProtocolOpenAIChat,
		registry.Caps{StrictTools: new(true)})
	openBytes := ToolsJSONBytes(asSentToolDefs(defs))

	// The wire is the oracle: the builder has to send the schema the validator
	// compiled against, and the price has to describe that same schema.
	assertChatWireMatchesPrice(t, req, res)
	wire := chatWireToolParams(t, req, res)
	if len(wire) != 1 {
		t.Fatalf("wire carries %d tools, want 1", len(wire))
	}
	if wire[0] == nil {
		t.Fatalf("builder omitted the parameters key for a prepared opt-out: the validator compiled this tool " +
			"against the open empty object, so the wire has to carry it")
	}
	if got := priceEmittedToolSchemas(req, res); got.ToolsJSONBytes != openBytes {
		t.Fatalf("priced count = %d, want %d, the open object the builder sends: a count that kept the "+
			"advertised price would bill bytes the wire does not carry", got.ToolsJSONBytes, openBytes)
	}
}

// Pricing a prepared request is an admission decision a caller may take early
// and forward, so it has to be idempotent: admission prices the request it is
// handed, and a second application of the same rule must not move the count.
//
// The count this replaces described the substituted form, so the emitted price
// differs from it by the difference between the two lists. Carrying that
// difference across a call — adjusting the incoming count instead of assigning
// the measurement — makes the result depend on how many times it was applied:
// middleware that prepriced and forwarded, followed by admission, bills the
// emitted list twice over. A cap between the two counts then refuses a request
// that fits.
func TestPrePricingAPreparedRequestIsIdempotent(t *testing.T) {
	t.Parallel()
	parameterless, _ := preparedToolLists(6)
	prepared := preparedRequest(parameterless)
	// Preparation stamps the count it measured and the list it measured it over,
	// and that stamp is what authorizes the differential path: without it a
	// count is simply replaced, so a test that skipped it would pass against a
	// rule that accumulates. Stamp it here for the same reason preparation does.
	prepared = MarkPreparedToolBytes(prepared)

	_, res := preparedBudgetClient(t, &preparedBudgetAdapter{}, 1_000_000)

	once := priceEmittedToolSchemas(prepared, res)
	twice := priceEmittedToolSchemas(once, res)
	if twice.ToolsJSONBytes != once.ToolsJSONBytes {
		t.Fatalf("pricing twice = %d bytes, once = %d: the price must describe the emitted list, not "+
			"accumulate a difference from the count it started with",
			twice.ToolsJSONBytes, once.ToolsJSONBytes)
	}
	// The reading admission takes has to be the same on both paths.
	if a, b := EstimateInputTokensForResolved(res, once).Tokens, EstimateInputTokensForResolved(res, twice).Tokens; a != b {
		t.Fatalf("admission reading after one price = %d, after two = %d: the two must agree",
			a, b)
	}
	// The rule has to have done something, or the agreement above is the
	// agreement of two unchanged counts rather than evidence that applying it
	// twice is safe.
	if once.ToolsJSONBytes == prepared.ToolsJSONBytes {
		t.Fatalf("prepriced count = %d, unchanged from the advertised count: this route's builder rewrites "+
			"the catalog, so pricing it must move the count", once.ToolsJSONBytes)
	}
}

// A Chat catalog whose only difference from its projection is the order of a
// required list emits the same bytes, so it must not be reported as a change.
//
// Reporting it would re-marshal the whole catalog on every dispatch against the
// cached count that exists to avoid exactly that marshal, and admission would
// pay the cost of the comparison plus a full measurement for a schema whose
// serialized form it already holds.
func TestAByteNeutralChatProjectionReusesTheCount(t *testing.T) {
	t.Parallel()
	// `required` out of sorted order: the projection sorts it, so the two schemas
	// differ structurally while serializing to the same bytes.
	defs := []ToolDefinition{{
		Name: "write_file",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
		Strict: new(true),
	}}
	_, res := budgetClientOnRoute(t, &preparedBudgetAdapter{}, 1_000_000, registry.ProtocolOpenAIChat,
		registry.Caps{StrictTools: new(true)})

	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol for %q", res.Protocol)
	}
	deriver, ok := p.(ToolSchemaDeriver)
	if !ok {
		t.Fatalf("%q does not derive tool schemas", res.Protocol)
	}
	emitted, changed := deriver.EmitToolSchemas(defs, res, false)
	if changed {
		t.Fatalf("deriver reported a change for a schema whose projection only reorders its required list: "+
			"the count it already holds is exact, and reporting a change re-measures the catalog every dispatch "+
			"(emitted %v)", emitted)
	}
}

// A caller sizes a request from CountInputTokens and then dispatches it, so the
// local reading has to describe the body that dispatches.
//
// A strict-capable Chat route's builder closes a parameterless tool's schema, and
// that schema is larger than the nil the caller declared. A reading over the
// advertised list is smaller than the request that goes out, so a caller that
// fits its request to the reading meets the shortfall as a context-budget
// rejection from Complete. A route with an exact provider counter is unaffected:
// that answer is the provider's own and needs no local correction.
func TestCountInputTokensPricesWhatTheRouteEmits(t *testing.T) {
	t.Parallel()
	defs := []ToolDefinition{{Name: "parameterless"}}
	req := Request{
		Provider: "prepared-budget",
		Model:    "model",
		Messages: []Message{User("x")},
		Tools:    defs,
	}
	// No override is registered: an override supplies its own exact counter or
	// none at all, and the route under test is a built-in one with no counter,
	// which is the case the local reading is the answer for.
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"prepared-budget": {Base: "openai", Protocol: registry.ProtocolOpenAIChat, APIKey: "unused",
				Caps: registry.Caps{
					ContextWindow:   new(1_000_000),
					MaxInputTokens:  new(1_000_000),
					MaxOutputTokens: new(4_096),
					StrictTools:     new(true),
				}},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	c := NewClient(WithRegistry(r))
	res, err := c.Resolve("prepared-budget/model")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, err := c.CountInputTokens(context.Background(), req)
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	advertised := EstimateInputTokensForResolved(res, req).Tokens
	emitted := EstimateInputTokensForResolved(res, priceEmittedToolSchemas(req, res)).Tokens
	if emitted <= advertised {
		t.Fatalf("fixture does not distinguish the two readings: advertised=%d emitted=%d",
			advertised, emitted)
	}
	if got.Tokens != emitted {
		t.Fatalf("CountInputTokens = %d, want %d: a caller sizing a request from this count would fit "+
			"it to %d and then meet the shortfall as a budget rejection from Complete",
			got.Tokens, emitted, advertised)
	}
}

// pricedChatDefs returns the definitions the deriver prices for req's route.
func pricedChatDefs(t *testing.T, req Request, res registry.Resolved) []ToolDefinition {
	t.Helper()
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol registered for %q", res.Protocol)
	}
	deriver, ok := p.(ToolSchemaDeriver)
	if !ok {
		t.Fatalf("protocol %q does not derive tool schemas", res.Protocol)
	}
	emitted, _ := deriver.EmitToolSchemas(req.Tools, res, req.PreparedTools())
	if emitted == nil {
		return req.Tools
	}
	return emitted
}

// chatWireToolParams reads the parameter schema of each tool from the body the
// Chat builder actually produces, with nil for a tool whose parameters key the
// builder omitted.
func chatWireToolParams(t *testing.T, req Request, res registry.Resolved) []map[string]any {
	t.Helper()
	p, ok := ProtocolFor(res.Protocol)
	if !ok {
		t.Fatalf("no protocol registered for %q", res.Protocol)
	}
	body, err := p.BuildBody(ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw, ok := body["tools"].([]map[string]any)
	if !ok {
		t.Fatalf("body tools = %#v, want the wire tool list", body["tools"])
	}
	out := make([]map[string]any, len(raw))
	for i, entry := range raw {
		fn, _ := entry["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		out[i] = params
	}
	return out
}
