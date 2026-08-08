package llm

import (
	"context"
	"math/rand/v2"
	"time"
)

// AttemptPhase classifies how a stream attempt failed, positionally — the
// attempt closure already knows whether the failure happened before/at open
// or after content started flowing, so no error-class taxonomy is needed to
// derive it.
type AttemptPhase int

const (
	// PhaseOpen is a rejection at or before stream open (429, 5xx, auth).
	PhaseOpen AttemptPhase = iota
	// PhaseConsume is a stream that opened, delivered content events, then died.
	PhaseConsume
	// PhaseSilentStall is a stream that opened, delivered zero content events,
	// and ended in the stall timeout (the provider accepted the request and
	// then sent nothing).
	PhaseSilentStall
	// PhaseFastReject is a stream that opened, delivered zero content events,
	// and ended fast — a decoded in-band rejection with no streaming attempted.
	PhaseFastReject
)

// AttemptReport carries one stream attempt's outcome back to RetryStream: the
// existing partial-output flag plus enough phase/stats detail to drive the
// early-stop rules.
type AttemptReport struct {
	// PartialOutput reports whether partial output was already delivered to
	// the caller — existing semantics, gates retry-after-partial.
	PartialOutput bool
	// Phase classifies where in the open/consume lifecycle the attempt failed.
	Phase AttemptPhase
	// ContentWindow is the span from the first content event to the last
	// (text, tool-call-argument, or reasoning delta). Zero when no content
	// event was ever seen.
	ContentWindow time.Duration
	// SalvagedBytes is text+tool-arg bytes accumulated (0 for reasoning-only).
	SalvagedBytes int
}

// StreamAttempt opens one stream and drains it, reporting the attempt's
// outcome and error (nil on success).
type StreamAttempt func(ctx context.Context) (AttemptReport, error)

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
	// FailFastAfter is the number of consecutive consume-phase failures
	// (PhaseConsume or PhaseSilentStall) that trip ProviderUnhealthyError,
	// short-circuiting the retry budget against a provider that keeps failing
	// in a shape retries cannot fix. 0 disables both early-stop rules (the
	// streak rule and the cap-detection rule below), so callers that never set
	// it see no behavior change.
	FailFastAfter int
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
	maxRetries := max(opts.Policy.MaxRetries, 0)
	start := time.Now()
	// consumeStreak counts consecutive attempts whose Phase is PhaseConsume or
	// PhaseSilentStall (the streak rule); capStreak counts the consecutive
	// suffix of those that are also cap-shaped (the cap rule). PhaseOpen and
	// PhaseFastReject are transparent: they touch neither counter.
	consumeStreak := 0
	capStreak := 0
	for n := 0; ; n++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		rep, err := attempt(ctx)
		if err == nil {
			return nil
		}
		// A cancelled/expired context means the budget is gone — surface the
		// cancellation, not the attempt's transient error.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if opts.FailFastAfter > 0 && (rep.Phase == PhaseConsume || rep.Phase == PhaseSilentStall) {
			consumeStreak++
			capShaped := rep.Phase == PhaseConsume && rep.ContentWindow >= 60*time.Second
			if capShaped {
				capStreak++
			} else {
				// A counted attempt that isn't cap-shaped breaks the cap rule's
				// consecutive-attempt requirement.
				capStreak = 0
			}
			// Checked before the streak rule: if both trip on the same attempt
			// (FailFastAfter <= 2), the cap-shaped pair reports as "cap" rather
			// than being subsumed by a coincident streak trip.
			if capStreak >= 2 {
				return &ProviderUnhealthyError{Shape: "cap", Attempts: n + 1, Elapsed: time.Since(start), LastErr: err}
			}
			if consumeStreak >= opts.FailFastAfter {
				return &ProviderUnhealthyError{Shape: "stall", Attempts: n + 1, Elapsed: time.Since(start), LastErr: err}
			}
		}
		if rep.PartialOutput && !opts.RetryAfterPartial {
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
		if rep.PartialOutput && opts.OnReset != nil {
			opts.OnReset()
		}
	}
}
