package llm

import (
	"context"
	"errors"
	"fmt"
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
	closed := make([]ToolDefinition, len(defs))
	copy(closed, defs)
	for i := range closed {
		closed[i].Parameters = strictDefaultToolParameters()
	}
	closedBytes := ToolsJSONBytes(closed)
	if closedBytes <= openBytes {
		t.Fatalf("fixture does not distinguish the two prices: open=%d closed=%d", openBytes, closedBytes)
	}

	// An opted-out tool keeps the open schema, so it must not be priced closed.
	optedOut := make([]ToolDefinition, len(defs))
	copy(optedOut, defs)
	optedOut[0].Strict = new(false)
	perTool := ToolsJSONBytes([]ToolDefinition{{Name: "x", Parameters: strictDefaultToolParameters()}}) -
		ToolsJSONBytes([]ToolDefinition{{Name: "x", Parameters: defaultToolParameters()}})
	if got, want := strictParameterlessByteDelta(optedOut), (toolCount-1)*perTool; got != want {
		t.Fatalf("delta with one opt-out = %d, want %d: an opted-out tool is sent open and must not be "+
			"priced closed", got, want)
	}
	if got, want := strictParameterlessByteDelta(defs), closedBytes-openBytes; got != want {
		t.Fatalf("delta = %d, want %d", got, want)
	}
	if got := strictParameterlessByteDelta(nil); got != 0 {
		t.Fatalf("delta over an empty catalog = %d, want 0", got)
	}

	// The prepared-budget row builds on the openai base, which declares strict
	// tools: the route that sends the closed form.
	c, res := preparedBudgetClient(t, &preparedBudgetAdapter{}, 1_000_000)
	if !registry.BoolValue(res.Caps.StrictTools) {
		t.Fatal("fixture route does not declare strict tools; the priced case needs a strict-capable row")
	}
	req := preparedRequest(defs)
	req.ToolsJSONBytes = openBytes
	priced := priceStrictParameterlessSchemas(req, mustDispatchTarget(t, c, req))
	if want := openBytes + closedBytes - openBytes; priced.ToolsJSONBytes != want {
		t.Fatalf("strict-capable count = %d, want %d: admission must price the closed schema the route emits",
			priced.ToolsJSONBytes, want)
	}

	// The control: the same catalog on a route without the capability keeps the
	// open price, because that is the schema such a route sends.
	off := mustDispatchTarget(t, c, req)
	off.res.Caps.StrictTools = nil
	if got := priceStrictParameterlessSchemas(req, off); got.ToolsJSONBytes != openBytes {
		t.Fatalf("capability-off count = %d, want %d: a route without strict enforcement sends the open schema",
			got.ToolsJSONBytes, openBytes)
	}

}

// mustDispatchTarget resolves req's route through the client, so the test asks
// the same question the dispatch path asks rather than constructing a stand-in.
func mustDispatchTarget(t *testing.T, c *Client, req Request) dispatchTarget {
	t.Helper()
	target, err := c.dispatchTarget(req)
	if err != nil {
		t.Fatalf("dispatchTarget: %v", err)
	}
	return target
}
