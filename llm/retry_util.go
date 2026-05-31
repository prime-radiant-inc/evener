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
// - policy.MaxRetries is the number of retries (not counting the initial attempt).
// - Jitter is +/- 50% (factor in [0.5, 1.5]) per unified-llm-spec.md.
// - If err provides RetryAfter:
//   - if RetryAfter <= policy.MaxDelay (or MaxDelay <= 0), it overrides calculated backoff.
//   - if RetryAfter > policy.MaxDelay, Retry aborts immediately (no retry) per spec.
func Retry[T any](ctx context.Context, policy RetryPolicy, sleep SleepFunc, randFloat func() float64, fn func() (T, error)) (T, error) {
	var zero T
	if sleep == nil {
		sleep = DefaultSleep
	}
	if randFloat == nil {
		randFloat = rand.Float64
	}
	maxRetries := policy.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if !retryableError(err) || attempt == maxRetries {
			return zero, err
		}

		delay, ok := retryDelay(policy, randFloat, err, attempt)
		if !ok {
			return zero, err
		}
		if policy.OnRetry != nil {
			policy.OnRetry(err, attempt+1, delay)
		}
		if err := sleep(ctx, delay); err != nil {
			return zero, err
		}
	}
	return zero, context.Canceled
}

func retryDelay(policy RetryPolicy, randFloat func() float64, err error, n int) (time.Duration, bool) {
	// Prefer server-provided Retry-After when present.
	var e Error
	if errors.As(err, &e) && e.RetryAfter() != nil {
		d := *e.RetryAfter()
		if d < 0 {
			d = 0
		}
		if policy.MaxDelay > 0 && d > policy.MaxDelay {
			// Spec: do not retry if server asks us to wait longer than max_delay.
			return 0, false
		}
		return d, true
	}

	base := policy.BaseDelay
	if base < 0 {
		base = 0
	}
	mult := policy.BackoffMultiplier
	if mult <= 1 {
		mult = 2
	}
	f := float64(base) * math.Pow(mult, float64(n))
	d := time.Duration(f)
	if policy.MaxDelay > 0 && d > policy.MaxDelay {
		d = policy.MaxDelay
	}
	if policy.Jitter && d > 0 {
		// Spec: +/- 50% jitter.
		j := 0.5 + randFloat() // [0.5, 1.5] assuming randFloat in [0,1]
		d = time.Duration(float64(d) * j)
	}
	return d, true
}
