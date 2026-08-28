package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// budgetClock is the deterministic clock the wall-budget tests measure with.
// Now advances only when sleep is called, so a retry group's elapsed time is
// exactly the sum of the backoffs the policy asked for and no test ever waits
// on real time.
type budgetClock struct {
	now    time.Time
	sleeps []time.Duration
}

func newBudgetClock() *budgetClock {
	return &budgetClock{now: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
}

func (c *budgetClock) Now() time.Time { return c.now }

func (c *budgetClock) sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *budgetClock) elapsed() time.Duration {
	return c.now.Sub(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC))
}

// budgetPolicy is DefaultRetryPolicy's shape with jitter off and the clock
// injected, so backoff is the exact 1s/2s/4s/.../60s ladder.
func budgetPolicy(clk *budgetClock, budget time.Duration) RetryPolicy {
	p := DefaultRetryPolicy()
	p.Jitter = false
	p.Now = clk.Now
	p.RateLimitWallBudget = budget
	return p
}

func rateLimit429() error {
	return ErrorFromHTTPStatus("bedrock", 429, "Too many tokens, please wait before trying again.", nil, nil)
}

func TestDefaultRetryPolicy_CarriesRateLimitWallBudget(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.RateLimitWallBudget != 30*time.Minute {
		t.Fatalf("RateLimitWallBudget = %s, want 30m", p.RateLimitWallBudget)
	}
}

// A 429 storm longer than the attempt budget must not end the group while the
// wall budget still has room: issue #342's fatal case is eleven consecutive
// Bedrock 429s settling provider_rejection with 25 minutes of run budget left.
func TestRetry_RateLimitOutlivesAttemptCapWhileWallBudgetRemains(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts <= 15 {
			return Response{}, rateLimit429()
		}
		return Response{Provider: "bedrock", Model: "m", Message: Assistant("ok")}, nil
	}

	resp, err := Retry(context.Background(), policy, clk.sleep, nil, fn)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp: %q", resp.Text())
	}
	if attempts != 16 {
		t.Fatalf("attempts = %d, want 16 (15 rate-limited + 1 success, past the %d-retry cap)", attempts, policy.MaxRetries)
	}
	if clk.elapsed() >= policy.RateLimitWallBudget {
		t.Fatalf("elapsed %s consumed the whole %s budget; the test no longer exercises the budget-remaining path", clk.elapsed(), policy.RateLimitWallBudget)
	}
}

// When the wall budget does elapse the rate-limit error is returned unchanged,
// so the caller's existing fatal escalation stands.
func TestRetry_RateLimitSettlesWhenWallBudgetElapses(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 10*time.Minute)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, rateLimit429()
	}

	_, err := Retry(context.Background(), policy, clk.sleep, nil, fn)
	if err == nil {
		t.Fatal("expected the rate-limit error once the wall budget elapsed")
	}
	if Kind(err) != KindRateLimit {
		t.Fatalf("Kind(err) = %s, want rate_limit (the original error must survive to the fatal path)", Kind(err))
	}
	if attempts <= policy.MaxRetries+1 {
		t.Fatalf("attempts = %d, want more than the %d-attempt cap before the budget bound it", attempts, policy.MaxRetries+1)
	}
	if clk.elapsed() < policy.RateLimitWallBudget {
		t.Fatalf("elapsed = %s, want >= the %s budget", clk.elapsed(), policy.RateLimitWallBudget)
	}
}

func TestRetry_WallBudgetPreservesTerminalErrorIdentity(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, time.Second)
	terminal := rateLimit429()

	_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
		return Response{}, terminal
	})
	if !errors.Is(err, terminal) {
		t.Fatalf("terminal error identity changed: got %T %v, want original %T", err, err, terminal)
	}
}

// Non-rate-limit retryables keep attempt-counted semantics exactly, even with a
// wall budget configured.
func TestRetry_NonRateLimitRetryableStillStopsAtAttemptCap(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 3

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, ErrorFromHTTPStatus("bedrock", 503, "unavailable", nil, nil)
	}

	if _, err := Retry(context.Background(), policy, clk.sleep, nil, fn); err == nil {
		t.Fatal("expected error after exhausting the attempt budget")
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4 (1 initial + 3 retries)", attempts)
	}
}

func TestRetry_NonRateLimitAfterRateLimitStormStillStops(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 1

	serverErr := ErrorFromHTTPStatus("bedrock", 503, "unavailable", nil, nil)
	attempts := 0
	_, err := Retry(context.Background(), policy, clk.sleep, nil, func() (Response, error) {
		attempts++
		if attempts <= 2 {
			return Response{}, rateLimit429()
		}
		return Response{}, serverErr
	})
	if !errors.Is(err, serverErr) {
		t.Fatalf("terminal error = %v, want the original 503", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3: the post-storm 503 must honor MaxRetries with >=", attempts)
	}
}

// A wall-budgeted rate-limit retry still waits exactly as long as the provider
// asked, rather than falling back to the calculated backoff ladder.
func TestRetry_RateLimitHonorsRetryAfterUnderWallBudget(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)

	ra := 45 * time.Second
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts <= 12 {
			return Response{}, ErrorFromHTTPStatus("bedrock", 429, "Too many tokens", nil, &ra)
		}
		return Response{Provider: "bedrock", Model: "m", Message: Assistant("ok")}, nil
	}

	if _, err := Retry(context.Background(), policy, clk.sleep, nil, fn); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if len(clk.sleeps) != 12 {
		t.Fatalf("sleeps = %d, want 12", len(clk.sleeps))
	}
	for i, d := range clk.sleeps {
		if d != ra {
			t.Fatalf("sleeps[%d] = %s, want the server's Retry-After %s", i, d, ra)
		}
	}
}

// Retry-After is a provider instruction, not a second client-side backoff
// guess. A wall-budgeted rate limit honors it even when it exceeds MaxDelay;
// otherwise this client would immediately re-enter the throttle it was told to
// avoid. Attempt-counted policies retain the legacy MaxDelay refusal.
func TestRetry_RateLimitHonorsRetryAfterAboveMaxDelay(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 5*time.Minute)
	policy.MaxRetries = 0
	policy.MaxDelay = time.Minute

	ra := 2 * time.Minute
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts == 1 {
			return Response{}, ErrorFromHTTPStatus("bedrock", 429, "slow down", nil, &ra)
		}
		return Response{Provider: "bedrock", Model: "m", Message: Assistant("ok")}, nil
	}

	if _, err := Retry(context.Background(), policy, clk.sleep, nil, fn); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := clk.elapsed(); got != ra {
		t.Fatalf("elapsed = %s, want Retry-After %s despite MaxDelay %s", got, ra, policy.MaxDelay)
	}
}

// A zero budget is the off switch: rate limits stay attempt-counted, which is
// what every policy literal that predates the budget gets.
func TestRetry_ZeroWallBudgetKeepsAttemptCountedRateLimits(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 0)
	policy.MaxRetries = 2

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, rateLimit429()
	}

	if _, err := Retry(context.Background(), policy, clk.sleep, nil, fn); err == nil {
		t.Fatal("expected error after exhausting the attempt budget")
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (1 initial + 2 retries)", attempts)
	}
}

// The streaming path is the one that died in the field (a 429 rejection at
// stream open), so it carries the same four properties.
func TestRetryStream_RateLimitOutlivesAttemptCapWhileWallBudgetRemains(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)

	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        policy,
		Sleep:         clk.sleep,
		FailFastAfter: 4,
	}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls <= 15 {
			return AttemptReport{Phase: PhaseOpen}, rateLimit429()
		}
		return AttemptReport{}, nil
	})
	if err != nil {
		t.Fatalf("RetryStream: %v", err)
	}
	if calls != 16 {
		t.Fatalf("calls = %d, want 16 (15 rate-limited opens + 1 success, past the %d-retry cap)", calls, policy.MaxRetries)
	}
}

func TestRetryStream_RateLimitSettlesWhenWallBudgetElapses(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 10*time.Minute)

	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{
		Policy:        policy,
		Sleep:         clk.sleep,
		FailFastAfter: 4,
	}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{Phase: PhaseOpen}, rateLimit429()
	})
	if err == nil {
		t.Fatal("expected the rate-limit error once the wall budget elapsed")
	}
	if Kind(err) != KindRateLimit {
		t.Fatalf("Kind(err) = %s, want rate_limit", Kind(err))
	}
	if calls <= policy.MaxRetries+1 {
		t.Fatalf("calls = %d, want more than the %d-attempt cap", calls, policy.MaxRetries+1)
	}
	if clk.elapsed() < policy.RateLimitWallBudget {
		t.Fatalf("elapsed = %s, want >= the %s budget", clk.elapsed(), policy.RateLimitWallBudget)
	}
}

func TestRetryStream_WallBudgetPreservesTerminalErrorIdentity(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, time.Second)
	terminal := rateLimit429()

	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		return AttemptReport{Phase: PhaseOpen}, terminal
	})
	if !errors.Is(err, terminal) {
		t.Fatalf("terminal error identity changed: got %T %v, want original %T", err, err, terminal)
	}
}

func TestRetryStream_NonRateLimitRetryableStillStopsAtAttemptCap(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 3

	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		return AttemptReport{Phase: PhaseOpen}, NewStreamError("bedrock", "truncated", nil)
	})
	if err == nil {
		t.Fatal("expected error after exhausting the attempt budget")
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4 (1 initial + 3 retries)", calls)
	}
}

func TestRetryStream_NonRateLimitAfterRateLimitStormStillStops(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)
	policy.MaxRetries = 1
	serverErr := NewStreamError("bedrock", "truncated", nil)
	calls := 0

	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls <= 2 {
			return AttemptReport{Phase: PhaseOpen}, rateLimit429()
		}
		return AttemptReport{Phase: PhaseOpen}, serverErr
	})
	if !errors.Is(err, serverErr) {
		t.Fatalf("terminal error = %v, want the original stream error", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3: the post-storm error must honor MaxRetries with >=", calls)
	}
}

func TestRetryStream_RateLimitHonorsRetryAfterUnderWallBudget(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)

	ra := 45 * time.Second
	calls := 0
	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls <= 12 {
			return AttemptReport{Phase: PhaseOpen}, ErrorFromHTTPStatus("bedrock", 429, "Too many tokens", nil, &ra)
		}
		return AttemptReport{}, nil
	})
	if err != nil {
		t.Fatalf("RetryStream: %v", err)
	}
	if len(clk.sleeps) != 12 {
		t.Fatalf("sleeps = %d, want 12", len(clk.sleeps))
	}
	for i, d := range clk.sleeps {
		if d != ra {
			t.Fatalf("sleeps[%d] = %s, want the server's Retry-After %s", i, d, ra)
		}
	}
}

func TestRetryStream_RateLimitHonorsRetryAfterAboveMaxDelay(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 5*time.Minute)
	policy.MaxRetries = 0
	policy.MaxDelay = time.Minute
	ra := 2 * time.Minute
	calls := 0

	err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: clk.sleep}, func(context.Context) (AttemptReport, error) {
		calls++
		if calls == 1 {
			return AttemptReport{Phase: PhaseOpen}, ErrorFromHTTPStatus("bedrock", 429, "slow down", nil, &ra)
		}
		return AttemptReport{}, nil
	})
	if err != nil {
		t.Fatalf("RetryStream: %v", err)
	}
	if calls != 2 || clk.elapsed() != ra {
		t.Fatalf("calls=%d elapsed=%s, want calls=2 elapsed=%s", calls, clk.elapsed(), ra)
	}
}

// An exhausted allowance is a 429 too, but it is KindQuotaExceeded and
// Permanent: the wall budget must not resurrect it into a 30-minute wait.
func TestRetry_QuotaExceeded429IsNotWallBudgeted(t *testing.T) {
	clk := newBudgetClock()
	policy := budgetPolicy(clk, 30*time.Minute)

	raw := map[string]any{"error": map[string]any{
		"code":    "insufficient_quota",
		"message": "You exceeded your current quota",
	}}
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, ErrorFromHTTPStatus("bedrock", 429, "quota exceeded", raw, nil)
	}

	_, err := Retry(context.Background(), policy, clk.sleep, nil, fn)
	if err == nil {
		t.Fatal("expected the quota error")
	}
	if Kind(err) != KindQuotaExceeded {
		t.Fatalf("Kind(err) = %s, want quota_exceeded", Kind(err))
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (quota exhaustion is permanent, not a rate-limit storm)", attempts)
	}
}

// WallBudgetedRateLimit is the one home for "is this failure wall-budgeted",
// shared with the agent's retry-chip denominator so the two cannot drift.
func TestWallBudgetedRateLimit(t *testing.T) {
	budgeted := DefaultRetryPolicy()
	unbudgeted := budgeted
	unbudgeted.RateLimitWallBudget = 0

	if !budgeted.WallBudgetedRateLimit(rateLimit429()) {
		t.Error("a 429 under a budgeted policy should be wall-budgeted")
	}
	if unbudgeted.WallBudgetedRateLimit(rateLimit429()) {
		t.Error("a 429 under a zero-budget policy should not be wall-budgeted")
	}
	if budgeted.WallBudgetedRateLimit(ErrorFromHTTPStatus("bedrock", 503, "unavailable", nil, nil)) {
		t.Error("a 503 should not be wall-budgeted")
	}
	if budgeted.WallBudgetedRateLimit(nil) {
		t.Error("nil should not be wall-budgeted")
	}
	if budgeted.WallBudgetedRateLimit(errors.New("boom")) {
		t.Error("an untyped error should not be wall-budgeted")
	}
}
