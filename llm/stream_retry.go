package llm

import (
	"context"
	"math/rand/v2"
)

// StreamAttempt opens one stream and drains it. It reports whether any partial
// output was already delivered to the caller — which gates retry-after-partial
// — and the attempt's error (nil on success).
type StreamAttempt func(ctx context.Context) (partialOutput bool, err error)

// RetryStreamOptions configures RetryStream.
type RetryStreamOptions struct {
	// Policy is the retry budget and backoff schedule.
	Policy RetryPolicy
	// Sleep waits between attempts; DefaultSleep is used when nil.
	Sleep SleepFunc
	// RetryAfterPartial controls whether a retryable failure is retried even
	// after partial output was delivered to the caller. When false, partial
	// output ends the retry chain so already-shown content is never re-streamed.
	RetryAfterPartial bool
	// OnReset, when set, is invoked before re-running an attempt that previously
	// delivered partial output (only relevant when RetryAfterPartial is true).
	// Callers use it to discard the partial so the next attempt replaces rather
	// than appends to it.
	OnReset func()
}

// RetryStream runs attempt, retrying the whole open+consume cycle on retryable
// errors with the policy's exponential backoff. It is the single home for the
// stream-retry control flow shared by the high-level StreamGenerate loop and the
// agent's per-round model call, so a mid-stream truncation is retried the same
// way on both paths instead of one path silently lacking it.
func RetryStream(ctx context.Context, opts RetryStreamOptions, attempt StreamAttempt) error {
	sleep := opts.Sleep
	if sleep == nil {
		sleep = DefaultSleep
	}
	maxRetries := opts.Policy.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	for n := 0; n <= maxRetries; n++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		partial, err := attempt(ctx)
		if err == nil {
			return nil
		}
		// A cancelled/expired context means the budget is gone — surface the
		// cancellation, not the attempt's transient error.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if partial && !opts.RetryAfterPartial {
			return err
		}
		if !retryableError(err) || n == maxRetries {
			return err
		}
		delay, ok := retryDelay(opts.Policy, rand.Float64, err, n)
		if !ok {
			return err
		}
		if opts.Policy.OnRetry != nil {
			opts.Policy.OnRetry(err, n+1, delay)
		}
		if sleepErr := sleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
		// Discard partial output before re-running so the next attempt replaces
		// what the failed one already streamed.
		if partial && opts.OnReset != nil {
			opts.OnReset()
		}
	}
	return nil
}
