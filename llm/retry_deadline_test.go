package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_RateLimitDeadlineUsesRunBudget(t *testing.T) {
	clk := newBudgetClock()
	clk.now = time.Now()
	policy := budgetPolicy(clk, 30*time.Minute)
	ctx, cancel := context.WithDeadline(WithRunBudget(context.Background()), clk.now.Add(2*time.Minute))
	defer cancel()
	calls := 0
	_, err := Retry(ctx, policy, clk.sleep, func() float64 { return .5 }, func() (Response, error) {
		calls++
		return Response{}, rateLimit429()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want run deadline exhaustion", err)
	}
	if calls != 7 {
		t.Fatalf("calls = %d, want 7 (no request after reserve boundary)", calls)
	}
}

func TestRetryStream_RateLimitDeadlineUsesRunBudget(t *testing.T) {
	clk := newBudgetClock()
	clk.now = time.Now()
	policy := budgetPolicy(clk, 30*time.Minute)
	ctx, cancel := context.WithDeadline(WithRunBudget(context.Background()), clk.now.Add(2*time.Minute))
	defer cancel()
	calls := 0
	err := RetryStream(ctx, RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{}, rateLimit429()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want run deadline exhaustion", err)
	}
	if calls != 7 {
		t.Fatalf("calls = %d, want 7 (no request after reserve boundary)", calls)
	}
}

func TestRetry_RateLimitLongRunDeadlineOutlivesThirtyMinuteFallback(t *testing.T) {
	clk := newBudgetClock()
	clk.now = time.Now()
	policy := budgetPolicy(clk, 30*time.Minute)
	policy.BaseDelay = time.Minute
	policy.MaxDelay = time.Minute
	ctx, cancel := context.WithDeadline(WithRunBudget(context.Background()), clk.now.Add(32*time.Minute))
	defer cancel()
	calls := 0
	_, err := Retry(ctx, policy, clk.sleep, func() float64 { return .5 }, func() (Response, error) {
		calls++
		if calls > 31 {
			return Response{Provider: "p", Model: "m", Message: Assistant("ok")}, nil
		}
		return Response{}, rateLimit429()
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if calls != 32 {
		t.Fatalf("calls = %d, want 32 (deadline budget must exceed old 30m fallback)", calls)
	}
}
