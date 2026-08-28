package llm

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// SleepFunc pauses for the duration d, returning early with an error if ctx is
// cancelled before d elapses.
type SleepFunc func(ctx context.Context, d time.Duration) error

// DefaultSleep waits for d, returning nil once it elapses or ctx.Err() if ctx
// is cancelled first. A non-positive d returns immediately.
func DefaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryableError(err error) bool {
	if err == nil {
		return false
	}
	// A bare context cancellation / deadline at this level means the overall
	// budget is exhausted — don't retry even though Classify would label
	// DeadlineExceeded as Retryable (that's a signal classification, not a
	// budget decision). A typed llm.Error that merely *wraps* a context
	// sentinel (e.g. a RequestTimeoutError built from an adapter-level
	// deadline) is a transient failure, so the bare-sentinel guard skips it
	// and defers to the classifier below.
	var le Error
	if !errors.As(err, &le) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return false
	}
	// Delegate to the classifier so retry decisions are made in exactly one
	// place. Anything other than ErrorClassRetryable short-circuits the
	// retry budget (kata xgzz).
	return Classify(err) == ErrorClassRetryable
}

// Retry runs fn and retries retryable errors with exponential backoff and jitter.
//
// Semantics:
//   - policy.MaxRetries is the number of retries (not counting the initial attempt).
//   - Jitter is +/- 50% (factor in [0.5, 1.5]) per unified-llm-spec.md.
//   - A rate-limited error keeps retrying past MaxRetries while the wall budget
//     has room; all other retryable errors remain attempt-counted.
//   - If err provides RetryAfter, it overrides calculated backoff. For an
//     attempt-counted policy, a value over MaxDelay still declines the retry per
//     the existing spec. A wall-budgeted rate limit honors the provider's longer
//     wait: ignoring Retry-After would be the surprising choice and immediately
//     re-enter the provider's throttle. The wait is clipped at the wall-budget
//     boundary, where the original error is returned.
func Retry[T any](ctx context.Context, policy RetryPolicy, sleep SleepFunc, randFloat func() float64, fn func() (T, error)) (T, error) {
	var zero T
	if sleep == nil {
		sleep = DefaultSleep
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	maxRetries := max(policy.MaxRetries, 0)
	start := policy.now()
	ownerBudget := ownedRunBudget(ctx)

	for attempt := 0; ; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if !retryableError(err) {
			return zero, err
		}
		if policy.WallBudgetedRateLimit(err) {
			if !rateLimitBudgetRemains(ctx, policy, err, start, ownerBudget) {
				return zero, rateLimitBudgetExhausted(ctx, err)
			}
		} else if attempt >= maxRetries {
			return zero, err
		}

		delay, ok := retryDelay(policy, randFloat, err, attempt)
		if !ok {
			return zero, err
		}
		if policy.WallBudgetedRateLimit(err) {
			remaining := rateLimitRemaining(ctx, policy, start, ownerBudget)
			if remaining <= 0 {
				return zero, rateLimitBudgetExhausted(ctx, err)
			}
			if delay > remaining {
				// Do not start a wait that would carry the group beyond its wall
				// budget. Sleeping the remaining slice makes the elapsed budget
				// deterministic, then the original provider error is returned.
				delay = remaining
			}
		}
		if policy.OnRetry != nil {
			policy.OnRetry(err, attempt+1, delay)
		}
		if err := sleep(ctx, delay); err != nil {
			return zero, err
		}
		if policy.WallBudgetedRateLimit(err) && !rateLimitBudgetRemains(ctx, policy, err, start, ownerBudget) {
			return zero, rateLimitBudgetExhausted(ctx, err)
		}
	}
}

func rateLimitBudgetExhausted(ctx context.Context, original error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return original
	}
	if hasRunBudget(ctx) {
		return runBudgetError{}
	}
	return context.DeadlineExceeded
}

func rateLimitShutdownReserve(policy RetryPolicy) time.Duration {
	if policy.RateLimitShutdownReserve > 0 {
		return policy.RateLimitShutdownReserve
	}
	return defaultRateLimitShutdownReserve
}

func ownedRunBudget(ctx context.Context) time.Duration {
	if !hasRunBudget(ctx) {
		return 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return 0
}

func rateLimitRemaining(ctx context.Context, policy RetryPolicy, start time.Time, ownerBudget time.Duration) time.Duration {
	if hasRunBudget(ctx) {
		if ownerBudget > 0 {
			// Snapshot native duration at group start, then consume it using
			// the policy clock; fake clocks may use another epoch.
			return ownerBudget - policy.now().Sub(start) - rateLimitShutdownReserve(policy)
		}
	}
	return policy.RateLimitWallBudget - policy.now().Sub(start)
}

func rateLimitBudgetRemains(ctx context.Context, policy RetryPolicy, err error, start time.Time, ownerBudget time.Duration) bool {
	return policy.WallBudgetedRateLimit(err) && rateLimitRemaining(ctx, policy, start, ownerBudget) > 0
}

func retryDelay(policy RetryPolicy, randFloat func() float64, err error, n int) (time.Duration, bool) {
	// Prefer a positive server-provided Retry-After when present. Non-positive
	// values mean retry immediately according to the header, but an immediate
	// retry is unsafe for wall-budgeted rate limits; use calculated backoff.
	var e Error
	if errors.As(err, &e) && e.RetryAfter() != nil && *e.RetryAfter() > 0 {
		d := *e.RetryAfter()
		if policy.MaxDelay > 0 && d > policy.MaxDelay && !policy.WallBudgetedRateLimit(err) {
			// Attempt-counted policies retain the existing spec behavior. A
			// wall-budgeted rate limit deliberately honors the provider's
			// directive even when it exceeds MaxDelay.
			return 0, false
		}
		return d, true
	}

	base := max(policy.BaseDelay, 0)
	mult := policy.BackoffMultiplier
	if mult <= 1 {
		mult = 2
	}
	// Convert base*mult^n with saturation: a bare time.Duration(f) wraps an
	// overflowing or NaN f to a NEGATIVE value, which then slips past the
	// MaxDelay cap below (negative is not > MaxDelay) and collapses backoff into
	// a zero-wait busy-retry loop — the opposite of the cap's intent. Saturate to
	// the max Duration instead so the cap always applies.
	d := saturatingDelay(base, float64(base)*math.Pow(mult, float64(n)))
	if policy.MaxDelay > 0 && d > policy.MaxDelay {
		d = policy.MaxDelay
	}
	if policy.Jitter && d > 0 {
		// Spec: +/- 50% jitter.
		j := 0.5 + randFloat() // [0.5, 1.5] assuming randFloat in [0,1]
		d = saturatingDelay(d, float64(d)*j)
	}
	return d, true
}

// saturatingDelay converts f nanoseconds to a Duration without the int64 wrap a
// bare time.Duration(f) produces on overflow or NaN. A zero base means no delay
// regardless of the exponential term (including the 0*Inf=NaN case that a
// negative or zero BaseDelay produces); an overflowing positive delay saturates
// to the max Duration so the MaxDelay cap can clamp it rather than being bypassed
// by a wrapped-negative value.
func saturatingDelay(base time.Duration, f float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if math.IsNaN(f) || f >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if f < 0 {
		return 0
	}
	return time.Duration(f)
}
