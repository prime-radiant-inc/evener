package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

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
