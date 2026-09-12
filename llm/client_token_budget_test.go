package llm_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

type tokenBudgetRecordingAdapter struct {
	mu    sync.Mutex
	calls int
}

func (*tokenBudgetRecordingAdapter) Name() string { return "budgeted" }

func (a *tokenBudgetRecordingAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return llm.Response{Message: llm.Assistant("unexpected dispatch")}, nil
}

func (a *tokenBudgetRecordingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return nil, llm.ErrStreamUnsupported
}

func (a *tokenBudgetRecordingAdapter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func tokenBudgetClient(t *testing.T) (*llm.Client, *tokenBudgetRecordingAdapter) {
	t.Helper()
	r := fixtureRegistry(t, "http://127.0.0.1:9", map[string]registry.Provider{
		"budgeted": {
			Base:   "openai",
			APIKey: "unused",
			Caps: registry.Caps{
				ContextWindow:   new(20_000),
				MaxInputTokens:  new(10_000),
				MaxOutputTokens: new(4_096),
			},
		},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	a := &tokenBudgetRecordingAdapter{}
	c.Register(a)
	return c, a
}

func assertLocalTokenBudgetFailure(t *testing.T, err error) {
	t.Helper()
	var budgetErr *llm.ContextBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v, want *ContextBudgetError", err)
	}
	if budgetErr.Provider != "budgeted" || budgetErr.Limit != "max_input" || llm.Kind(err) != llm.KindContextLength || budgetErr.Retryable() {
		t.Fatalf("local error = %+v kind=%v retryable=%v", budgetErr, llm.Kind(err), budgetErr.Retryable())
	}
}

func TestClientCompleteTokenBudgetPreventsAdapterDispatch(t *testing.T) {
	c, adapter := tokenBudgetClient(t)
	req := userRequest("budgeted", "model")
	req.InputTokensEstimate = 9_000

	_, err := c.Complete(context.Background(), req)
	assertLocalTokenBudgetFailure(t, err)
	if got := adapter.callCount(); got != 0 {
		t.Fatalf("adapter calls = %d, want 0", got)
	}
}

func TestClientStreamTokenBudgetPreventsAdapterDispatch(t *testing.T) {
	c, adapter := tokenBudgetClient(t)
	req := userRequest("budgeted", "model")
	req.InputTokensEstimate = 9_000

	_, err := c.Stream(context.Background(), req)
	assertLocalTokenBudgetFailure(t, err)
	if got := adapter.callCount(); got != 0 {
		t.Fatalf("adapter calls = %d, want 0", got)
	}
}

func TestClientMiddlewareTokenBudgetMutationCannotBypassGuard(t *testing.T) {
	c, adapter := tokenBudgetClient(t)
	c.Use(llm.MiddlewareFunc{Complete: func(ctx context.Context, req llm.Request, next llm.CompleteFunc) (llm.Response, error) {
		req.InputTokensEstimate = 9_000
		return next(ctx, req)
	}})

	_, err := c.Complete(context.Background(), userRequest("budgeted", "model"))
	assertLocalTokenBudgetFailure(t, err)
	if got := adapter.callCount(); got != 0 {
		t.Fatalf("adapter calls = %d, want 0", got)
	}
}
