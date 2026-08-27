package llm

import (
	"context"
	"errors"
	"time"
)

// ErrRunBudgetExhausted identifies retry exhaustion at an owned run's
// shutdown reserve. It unwraps to context.DeadlineExceeded for callers.
var ErrRunBudgetExhausted = errors.New("run budget exhausted")

type runBudgetError struct{}

func (runBudgetError) Error() string        { return ErrRunBudgetExhausted.Error() }
func (runBudgetError) Unwrap() error        { return context.DeadlineExceeded }
func (runBudgetError) Is(target error) bool { return target == ErrRunBudgetExhausted }

type runBudgetContextKey struct{}

// WithRunBudget marks ctx as the explicit one-shot run owner.
func WithRunBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, runBudgetContextKey{}, true)
}

func hasRunBudget(ctx context.Context) bool {
	v, _ := ctx.Value(runBudgetContextKey{}).(bool)
	return v
}

// RetryPolicy configures how retry attempts are spaced, including the maximum
// number of retries, the exponential backoff delays, optional jitter, an
// optional rate-limit wall budget, and a callback invoked before each retry.
type RetryPolicy struct {
	// MaxRetries is the number of retry attempts (not counting the initial attempt).
	MaxRetries int

	// BaseDelay is the delay before the first retry attempt.
	BaseDelay time.Duration

	// MaxDelay caps the delay between retries.
	MaxDelay time.Duration

	// BackoffMultiplier controls exponential backoff growth (2.0 = double each retry).
	BackoffMultiplier float64

	// Jitter adds randomness to delays to reduce thundering-herd retries.
	Jitter bool

	// RateLimitWallBudget is how long an attempt group may keep retrying a
	// rate-limited call ([KindRateLimit]). While it has room, MaxRetries does
	// not end the group. Zero preserves the attempt-counted behavior.
	RateLimitWallBudget time.Duration

	// Now is the clock used to measure RateLimitWallBudget. Nil uses time.Now.
	Now func() time.Time

	// RateLimitShutdownReserve is held back from a caller deadline so the run
	// can unwind cleanly instead of beginning another provider wait. Zero uses
	// the package default reserve.
	RateLimitShutdownReserve time.Duration

	// OnRetry is invoked before sleeping for a retry attempt.
	OnRetry func(err error, attempt int, delay time.Duration)
}

// DefaultRetryPolicy returns a RetryPolicy with 10 retries, a 1 second base
// delay, a 60 second maximum delay, a backoff multiplier of 2.0, jitter
// enabled, and a 30 minute rate-limit wall budget. Transient provider failures
// (rate limits, 5xx, and mid-stream truncations) are common enough that a
// single turn should ride out a long burst of them rather than fail; the 60s
// delay cap bounds the worst-case wait per attempt.
//
// The wall budget is a backstop, not a target: the caller's context deadline
// still wins, and a shorter Retry-After/backoff schedule may settle earlier.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:               10,
		BaseDelay:                1 * time.Second,
		MaxDelay:                 60 * time.Second,
		BackoffMultiplier:        2.0,
		Jitter:                   true,
		RateLimitWallBudget:      30 * time.Minute,
		RateLimitShutdownReserve: 30 * time.Second,
	}
}

const defaultRateLimitShutdownReserve = 30 * time.Second

// WallBudgetedRateLimit reports whether this policy gives a rate-limit error a
// wall-clock budget instead of applying MaxRetries.
func (p RetryPolicy) WallBudgetedRateLimit(err error) bool {
	return p.RateLimitWallBudget > 0 && Kind(err) == KindRateLimit
}

func (p RetryPolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
