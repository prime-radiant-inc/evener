package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_RetriesRetryableErrors_WithExponentialBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        3,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          5 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts <= 2 {
			return Response{}, err429
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := Retry(ctx, policy, sleep, nil, fn)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp: %q", resp.Text())
	}
	if got, want := len(sleeps), 2; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != 10*time.Millisecond {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], 10*time.Millisecond)
	}
	if sleeps[1] != 20*time.Millisecond {
		t.Fatalf("sleep[1]: got %s want %s", sleeps[1], 20*time.Millisecond)
	}
}

func TestRetry_RetryAfterHeader_OverridesCalculatedBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        1,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          5 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	ra := 3 * time.Second
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, &ra)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		if attempts == 1 {
			return Response{}, err429
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Retry(ctx, policy, sleep, nil, fn); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got, want := len(sleeps), 1; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != ra {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], ra)
	}
}

func TestRetry_RetryAfterHeader_TooLarge_AbortsWithoutRetry(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        3,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	sleep := func(ctx context.Context, d time.Duration) error {
		t.Fatalf("unexpected sleep: %s", d)
		return nil
	}

	ra := 5 * time.Second
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, &ra)
	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err429
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, sleep, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_DoesNotRetry_NonRetryableErrors(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, Jitter: false, BackoffMultiplier: 2.0, MaxDelay: 1 * time.Second}
	sleep := func(ctx context.Context, d time.Duration) error {
		t.Fatalf("unexpected sleep: %s", d)
		return nil
	}
	err401 := ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err401
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, sleep, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_MaxRetriesZero_DisablesRetry(t *testing.T) {
	policy := RetryPolicy{MaxRetries: 0, BaseDelay: 1 * time.Millisecond, Jitter: false, BackoffMultiplier: 2.0, MaxDelay: 1 * time.Second}
	err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)

	attempts := 0
	fn := func() (Response, error) {
		attempts++
		return Response{}, err429
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Retry(ctx, policy, nil, nil, fn)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts: got %d want 1", attempts)
	}
}

func TestRetry_JitterAppliedWhenEnabled(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        1,
		BaseDelay:         10 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}
	err := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)

	if got, want := func() time.Duration {
		d, ok := retryDelay(policy, func() float64 { return 0.0 }, err, 0) // retry #1 uses n=0
		if !ok {
			t.Fatalf("expected ok=true")
		}
		return d
	}(), 5*time.Millisecond; got != want {
		t.Fatalf("jitter low: got %s want %s", got, want)
	}

	if got, want := func() time.Duration {
		d, ok := retryDelay(policy, func() float64 { return 1.0 }, err, 0)
		if !ok {
			t.Fatalf("expected ok=true")
		}
		return d
	}(), 15*time.Millisecond; got != want {
		t.Fatalf("jitter high: got %s want %s", got, want)
	}
}

func TestRetry_UnknownErrors_DefaultRetryable(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:        2,
		BaseDelay:         0,
		MaxDelay:          0,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}
	var attempts int
	fn := func() (Response, error) {
		attempts++
		if attempts < 3 {
			return Response{}, errors.New("network fell over")
		}
		return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := Retry(ctx, policy, nil, nil, fn)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("resp: %q", resp.Text())
	}
	if attempts != 3 {
		t.Fatalf("attempts: got %d want 3", attempts)
	}
}

func TestDefaultRetryPolicy_Values(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxRetries != 10 {
		t.Fatalf("MaxRetries = %d, want 10", p.MaxRetries)
	}
	if p.BaseDelay != 1*time.Second {
		t.Fatalf("BaseDelay = %v, want 1s", p.BaseDelay)
	}
	if p.MaxDelay != 60*time.Second {
		t.Fatalf("MaxDelay = %v, want 60s", p.MaxDelay)
	}
	if p.BackoffMultiplier != 2.0 {
		t.Fatalf("BackoffMultiplier = %v, want 2.0", p.BackoffMultiplier)
	}
	if !p.Jitter {
		t.Fatal("Jitter should be true")
	}
}

// TestRetryDelay_SaturatesInsteadOfWrappingNegative is the regression for a
// latent overflow the retry fuzzer found: base*mult^n exceeding MaxInt64 ns (or a
// 0*+Inf=NaN when BaseDelay clamps to 0) wrapped through time.Duration(f) to a
// NEGATIVE Duration, which slipped under the `d > MaxDelay` cap check — bypassing
// the cap and collapsing backoff into a zero-wait busy loop. retryDelay must now
// saturate and honor the cap. Not reachable via DefaultRetryPolicy (MaxRetries=10),
// but reachable from any policy with a high MaxRetries/multiplier.
func TestRetryDelay_SaturatesInsteadOfWrappingNegative(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 429, "boom", nil, nil)
	cases := []struct {
		name   string
		policy RetryPolicy
		n      int
	}{
		{"overflow_positive_base", RetryPolicy{MaxRetries: 97, BaseDelay: 58 * time.Millisecond, MaxDelay: 99 * time.Millisecond, BackoffMultiplier: 10}, 20},
		{"nan_from_clamped_base_inf_mult", RetryPolicy{MaxRetries: 500, BaseDelay: -1, MaxDelay: 50 * time.Millisecond, BackoffMultiplier: 10}, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := retryDelay(tc.policy, func() float64 { return 0 }, err, tc.n)
			if !ok {
				t.Fatal("expected a delay, got ok=false")
			}
			if d < 0 {
				t.Fatalf("negative delay %v: overflow wrapped past the cap", d)
			}
			if tc.policy.MaxDelay > 0 && d > tc.policy.MaxDelay {
				t.Fatalf("delay %v exceeds MaxDelay %v: cap bypassed", d, tc.policy.MaxDelay)
			}
		})
	}
}

// stepClock is a policy clock that walks a fixed list of offsets from a single
// epoch, repeating the last one if more calls arrive than steps were planned.
// The wall-budget checks consult the clock at known points, so exact steps pin
// which budget check observes the exhaustion.
type stepClock struct {
	steps []time.Duration
	calls int
}

func (c *stepClock) now() time.Time {
	at := c.calls
	if at >= len(c.steps) {
		at = len(c.steps) - 1
	}
	c.calls++
	return time.Unix(0, 0).UTC().Add(c.steps[at])
}

func wallBudgetPolicy(clock func() time.Time, budget, reserve time.Duration) RetryPolicy {
	return RetryPolicy{
		MaxRetries: 5, BaseDelay: time.Millisecond, MaxDelay: time.Second, BackoffMultiplier: 2,
		RateLimitWallBudget: budget, RateLimitShutdownReserve: reserve, Now: clock,
	}
}

func alwaysRateLimited() (Response, error) {
	return Response{}, ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
}

// A wall budget already spent when the first rate limit arrives ends the group
// there: with a deadline on the context, the budget error surfaces as
// context.DeadlineExceeded rather than the provider's own error.
func TestRetryWallBudgetExhaustedBeforeTheFirstRetrySleep(t *testing.T) {
	clock := &stepClock{steps: []time.Duration{0, 5 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	_, err := Retry(ctx, wallBudgetPolicy(clock.now, time.Second, 0), noSleepRetry, nil, func() (Response, error) {
		attempts++
		return alwaysRateLimited()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrRunBudgetExhausted) {
		t.Fatalf("a context with no run budget must not report the run-budget sentinel: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1: a spent budget must not earn another attempt", attempts)
	}
}

// A budget that runs out between the delay computation and the wait still ends
// the group without sleeping; without a context deadline the caller gets the
// provider's own error back.
func TestRetryWallBudgetExhaustedWhileComputingTheWait(t *testing.T) {
	clock := &stepClock{steps: []time.Duration{0, 0, 30 * time.Second}}
	slept := 0
	attempts := 0
	rateLimited := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	_, err := Retry(context.Background(), wallBudgetPolicy(clock.now, 10*time.Second, 0), func(context.Context, time.Duration) error {
		slept++
		return nil
	}, nil, func() (Response, error) {
		attempts++
		return Response{}, rateLimited
	})
	//nolint:errorlint // identity is the assertion: the spent budget hands back the provider's own error
	if err != rateLimited {
		t.Fatalf("err = %v, want the original rate-limit error handed back unchanged", err)
	}
	if attempts != 1 || slept != 0 {
		t.Fatalf("attempts = %d slept = %d, want 1 and no sleep: the group must end before waiting", attempts, slept)
	}
}

// Cancellation observed only after the wait — a sleep that swallows the
// cancellation — must surface the context error from the budget check, not
// start another attempt.
func TestRetryWallBudgetExhaustedAfterACancelledWait(t *testing.T) {
	clock := &stepClock{steps: []time.Duration{0, 0, 0, 30 * time.Second}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	_, err := Retry(ctx, wallBudgetPolicy(clock.now, 10*time.Second, 0), func(context.Context, time.Duration) error {
		cancel()
		return nil // a sleep that missed the cancellation window
	}, nil, func() (Response, error) {
		attempts++
		return alwaysRateLimited()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// A run-budget context measures the wall budget against the run's own
// deadline, net of the shutdown reserve. The exhausted run reports the
// run-budget sentinel, which unwraps to context.DeadlineExceeded and renders
// the sentinel's own text.
func TestRetryRunBudgetExhaustionReportsTheRunBudgetSentinel(t *testing.T) {
	for _, reserve := range []time.Duration{0, 200 * time.Millisecond} {
		clock := &stepClock{steps: []time.Duration{0, 2 * time.Second}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := Retry(WithRunBudget(ctx), wallBudgetPolicy(clock.now, time.Hour, reserve), noSleepRetry, nil, alwaysRateLimited)
		if !errors.Is(err, ErrRunBudgetExhausted) {
			t.Fatalf("reserve %v: err = %v, want the run-budget sentinel", reserve, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("reserve %v: the sentinel must unwrap to context.DeadlineExceeded: %v", reserve, err)
		}
		if err.Error() != ErrRunBudgetExhausted.Error() {
			t.Fatalf("reserve %v: rendered %q, want the sentinel's text %q", reserve, err.Error(), ErrRunBudgetExhausted.Error())
		}
	}
}
