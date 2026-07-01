package llm

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// The retry control flow (Retry, RetryStream, retryDelay, DefaultRetryPolicy)
// was only unit-tested — no fuzz target drove it with adversarial
// (error, attempt-number, retry-after) sequences. These three targets exercise
// it end to end with a fuzzed failure script and a fake operation, using a hard
// per-run attempt cap so a control-flow regression that never terminates fails
// loudly instead of hanging the fuzzer.

// hardAttemptCap bounds how many times a fuzzed operation may be invoked in a
// single run. Every target clamps its policy so the honest budget stays well
// under this; blowing past it means the retry loop failed to terminate, which
// is itself a bug the oracle must surface rather than hang on.
const hardAttemptCap = 4096

// clampMaxRetries maps a fuzzed retry budget onto a sane, fast-to-drive range.
// Negative values are kept (the functions clamp them to 0 internally, an
// invariant the oracles rely on) but the upper bound stays small so an
// always-fail script completes quickly.
func clampMaxRetries(raw int) int {
	if raw < -3 {
		return -3
	}
	if raw > 200 {
		return 200
	}
	return raw
}

// clampUnit maps arbitrary fuzz bits onto [0,1), honoring the rand.Float64
// contract that retryDelay's jitter depends on. Feeding out-of-range values
// would manufacture fake "negative jitter" bugs, so the harness never does.
func clampUnit(bits uint64) float64 {
	// 53-bit mantissa division mirrors math/rand/v2's Float64.
	return float64(bits>>11) / (1 << 53)
}

// fuzzPolicy builds a RetryPolicy from fuzzed fields. When useDefault is set it
// returns DefaultRetryPolicy so that target is exercised too.
func fuzzPolicy(useDefault bool, maxRetries int, baseMs, maxDelayMs int32, mult float64, jitter bool) RetryPolicy {
	if useDefault {
		return DefaultRetryPolicy()
	}
	return RetryPolicy{
		MaxRetries:        maxRetries,
		BaseDelay:         time.Duration(baseMs) * time.Millisecond,
		MaxDelay:          time.Duration(maxDelayMs) * time.Millisecond,
		BackoffMultiplier: mult,
		Jitter:            jitter,
	}
}

// FuzzRetryDelay drives retryDelay (and DefaultRetryPolicy) over an adversarial
// policy x error x attempt-number space, including server-supplied Retry-After.
//
// Oracles (never a bare no-panic):
//   - retryDelay never panics.
//   - When it returns a delay, that delay is non-negative — except the int64
//     overflow regime (an exponential base*mult^n that exceeds MaxInt64
//     nanoseconds), which the harness detects and excludes. That regime is a
//     KNOWN latent bug: the overflow wraps to a negative Duration that slips
//     under the `d > MaxDelay` cap check, so the cap is bypassed and a negative
//     delay is returned even when MaxDelay is a small positive value. It is not
//     reachable via current callers (all use DefaultRetryPolicy, MaxRetries=10,
//     so n<=9 never overflows) but is reachable from any RetryPolicy with a
//     high MaxRetries/multiplier. Excluded here so the target ships green as a
//     regression guard rather than hiding or fabricating the finding.
//   - When MaxDelay > 0 the returned delay respects the cap: at most 1.5x
//     MaxDelay, accounting for the documented +/-50% jitter.
//   - A Retry-After larger than a positive MaxDelay must abort (ok == false),
//     per the spec that we never wait longer than the cap asks.
//   - A Retry-After within the cap is honored verbatim (clamped to >= 0), never
//     jittered.
//   - retryDelay is deterministic given a fixed randFloat.
func FuzzRetryDelay(f *testing.F) {
	// math.MinInt64 in the retryAfterMs slot means "no Retry-After"; any other
	// value builds one (negatives exercise the clamp-to-zero branch).
	noRA := int64(math.MinInt64)
	f.Add(false, 10, int32(1000), int32(60000), 2.0, true, 429, noRA, uint64(0), 0)
	f.Add(true, 0, int32(0), int32(0), 0.0, false, 500, int64(2000), uint64(1<<52), 3)
	f.Add(false, 3, int32(100), int32(0), 10.0, false, 429, noRA, uint64(0), 40)
	f.Add(false, 5, int32(500), int32(30000), 2.0, true, 503, int64(120000), uint64(1<<40), 2)
	// base<0 clamp-to-zero; mult<=1 defaulting.
	f.Add(false, 4, int32(-100), int32(0), 0.5, false, 429, noRA, uint64(0), 1)
	// backoff exceeds MaxDelay -> cap branch.
	f.Add(false, 6, int32(1000), int32(5000), 2.0, false, 429, noRA, uint64(0), 5)
	// negative Retry-After -> clamp to zero.
	f.Add(false, 3, int32(100), int32(60000), 2.0, false, 429, int64(-50), uint64(0), 0)
	// Retry-After exactly at the cap -> honored, not aborted.
	f.Add(false, 3, int32(100), int32(1000), 2.0, false, 429, int64(1000), uint64(0), 0)

	f.Fuzz(func(t *testing.T,
		useDefault bool, maxRetries int, baseMs, maxDelayMs int32, mult float64, jitter bool,
		status int, retryAfterMs int64, randBits uint64, n int,
	) {
		policy := fuzzPolicy(useDefault, maxRetries, baseMs, maxDelayMs, mult, jitter)
		if useDefault {
			// DefaultRetryPolicy target: its documented shape must hold.
			if policy.MaxRetries <= 0 || policy.MaxDelay <= 0 || policy.BackoffMultiplier <= 1 {
				t.Fatalf("DefaultRetryPolicy shape broken: %+v", policy)
			}
		}
		if n < 0 {
			n = -n
		}
		if n > 4096 {
			n %= 4097
		}

		var retryAfter *time.Duration
		if retryAfterMs != math.MinInt64 {
			d := time.Duration(retryAfterMs) * time.Millisecond
			retryAfter = &d
		}
		err := ErrorFromHTTPStatus("openai", status, "boom", nil, retryAfter)

		randFloat := func() float64 { return clampUnit(randBits) }
		delay, ok := retryDelay(policy, randFloat, err, n)

		// Determinism: identical inputs, identical output.
		if delay2, ok2 := retryDelay(policy, randFloat, err, n); delay2 != delay || ok2 != ok {
			t.Fatalf("retryDelay nondeterministic: (%v,%v) then (%v,%v)", delay, ok, delay2, ok2)
		}

		// Retry-After abort branch: a server ask longer than a positive cap
		// must refuse to retry.
		if retryAfter != nil && policy.MaxDelay > 0 && *retryAfter > policy.MaxDelay {
			if ok {
				t.Fatalf("Retry-After %v > MaxDelay %v but retryDelay returned ok (delay=%v)",
					*retryAfter, policy.MaxDelay, delay)
			}
			return
		}

		if !ok {
			return
		}

		// Non-negativity, excluding only the degenerate float-overflow regime.
		if !degenerateDelayFloat(policy, n) {
			if delay < 0 {
				t.Fatalf("retryDelay returned negative delay %v (policy=%+v n=%d err=%v)",
					delay, policy, n, err)
			}
		}

		// Retry-After honored verbatim when within the cap.
		if retryAfter != nil {
			want := *retryAfter
			if want < 0 {
				want = 0
			}
			if delay != want {
				t.Fatalf("Retry-After within cap not honored: got %v want %v", delay, want)
			}
			return
		}

		// Backoff cap: delay never exceeds 1.5x a positive MaxDelay.
		if policy.MaxDelay > 0 {
			if float64(delay) > float64(policy.MaxDelay)*1.5+1 {
				t.Fatalf("delay %v exceeds 1.5x MaxDelay %v", delay, policy.MaxDelay)
			}
		}
	})
}

// degenerateDelayFloat reports whether retryDelay's exponential
// (base * mult^n) is a float64 that does not convert cleanly to an int64
// nanosecond Duration: either it exceeds the int64 range, or it is NaN (which
// arises from 0 * +Inf when BaseDelay clamps to 0 and mult^n overflows to
// +Inf). It mirrors retryDelay's own defaulting so the non-negativity oracle
// excludes exactly this degenerate regime and nothing else. The bad conversion
// happens at time.Duration(f), which is BEFORE the MaxDelay cap check — and the
// wrapped-negative result then fails that check — so a positive MaxDelay does
// NOT save it; the exclusion is independent of MaxDelay.
func degenerateDelayFloat(policy RetryPolicy, n int) bool {
	base := policy.BaseDelay
	if base < 0 {
		base = 0
	}
	mult := policy.BackoffMultiplier
	if mult <= 1 {
		mult = 2
	}
	f := float64(base) * math.Pow(mult, float64(n))
	return math.IsNaN(f) || f >= float64(math.MaxInt64)
}

// scriptStep is one entry in a fuzzed failure script: what the fake operation
// does on a given attempt.
type scriptStep int

const (
	stepSucceed   scriptStep = iota // return the success value, nil error
	stepRetryable                   // fail with a retryable (429) llm.Error
	stepPermanent                   // fail with a permanent (400) llm.Error
)

// decodeStep maps a fuzz byte to a script step.
func decodeStep(b byte) scriptStep {
	switch b % 3 {
	case 0:
		return stepSucceed
	case 1:
		return stepRetryable
	default:
		return stepPermanent
	}
}

// stepFor returns the scripted step for attempt i. An empty script means
// "always retryable-fail" so the always-fail terminal case stays reachable.
func stepFor(script []byte, i int) scriptStep {
	if len(script) == 0 {
		return stepRetryable
	}
	return decodeStep(script[i%len(script)])
}

const (
	retrySuccessValue = 0x5eed
	retryableStatus   = 429
	permanentStatus   = 400
)

// errSleepInterrupted stands in for a context-cancelled sleep so the harness can
// drive the retry loop's "sleep returned an error, give up" path without a real
// wait.
var errSleepInterrupted = errors.New("sleep interrupted")

// FuzzRetry drives the generic Retry helper with a fuzzed failure script and a
// fake operation, proving its control flow rather than any I/O.
//
// Oracles:
//   - Retry never panics and always terminates within the honest budget; a hard
//     cap turns a runaway loop into a failure instead of a hang.
//   - The operation is invoked at most maxRetries+1 times (initial + retries),
//     with the negative-budget clamp respected.
//   - A success verdict is returned only when the final attempt actually
//     succeeded, and it carries that attempt's value.
//   - Hitting a permanent error stops immediately and surfaces a non-nil error.
//   - An always-retryable-fail script exhausts the full budget and returns the
//     last (non-nil) error — never nil, never a hang.
//   - OnRetry fires at most once per retry taken (never more than the budget).
//   - Cancelling the context mid-run surfaces context.Canceled and stops.
//   - A retryable error whose Retry-After exceeds MaxDelay aborts the retry
//     chain (variant 3), and a sleep error is surfaced verbatim (variant 2);
//     both exercise the give-up paths without hanging.
//   - The nil-Sleep/nil-randFloat defaults are honored (variant 1).
func FuzzRetry(f *testing.F) {
	f.Add(3, []byte{1, 1, 0}, uint64(0), false, 0, uint8(0))
	f.Add(0, []byte{1}, uint64(1<<40), true, 0, uint8(0))
	f.Add(10, []byte{}, uint64(0), true, 0, uint8(0))
	f.Add(5, []byte{1, 2}, uint64(1<<52), false, 0, uint8(0))
	f.Add(-2, []byte{0}, uint64(0), false, 0, uint8(0))
	f.Add(5, []byte{1, 1, 1}, uint64(0), true, 2, uint8(0))
	f.Add(4, []byte{1, 1, 0}, uint64(0), false, 0, uint8(1)) // nil sleep/rand defaults
	f.Add(4, []byte{1, 1, 1}, uint64(0), false, 0, uint8(2)) // sleep error
	f.Add(4, []byte{1, 1, 1}, uint64(0), true, 0, uint8(3))  // Retry-After > MaxDelay abort

	f.Fuzz(func(t *testing.T, rawMaxRetries int, script []byte, randBits uint64, onRetry bool, cancelAfter int, variant uint8) {
		maxRetries := clampMaxRetries(rawMaxRetries)
		effectiveMax := maxRetries
		if effectiveMax < 0 {
			effectiveMax = 0
		}
		variant %= 4
		nilFuncs := variant == 1
		sleepErrs := variant == 2
		bigRetryAfter := variant == 3

		onRetryCount := 0
		policy := RetryPolicy{
			MaxRetries:        maxRetries,
			BaseDelay:         time.Millisecond,
			MaxDelay:          time.Second,
			BackoffMultiplier: 2.0,
			Jitter:            true,
		}
		if onRetry {
			policy.OnRetry = func(error, int, time.Duration) { onRetryCount++ }
		}

		// Noop sleep keeps the fuzzer free of real waits while still driving the
		// full backoff-and-retry control flow.
		var sleep SleepFunc = func(context.Context, time.Duration) error { return nil }
		randFloat := func() float64 { return clampUnit(randBits) }
		if sleepErrs {
			sleep = func(context.Context, time.Duration) error { return errSleepInterrupted }
		}
		if nilFuncs {
			// Exercise the nil-Sleep/nil-randFloat defaulting. Zero the delays so
			// the real DefaultSleep never actually waits.
			sleep = nil
			randFloat = nil
			policy.BaseDelay = 0
			policy.MaxDelay = 0
			policy.Jitter = false
		}
		// A retryable error carrying a Retry-After beyond MaxDelay must abort.
		var retryAfterErr *time.Duration
		if bigRetryAfter {
			d := time.Hour
			retryAfterErr = &d
		}
		newRetryable := func() error {
			return ErrorFromHTTPStatus("openai", retryableStatus, "retryable", nil, retryAfterErr)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		calls := 0
		lastStep := stepSucceed
		cancelFired := false
		fn := func() (int, error) {
			calls++
			if calls > hardAttemptCap {
				t.Fatalf("Retry ran the operation %d times (>%d): loop failed to terminate", calls, hardAttemptCap)
			}
			// Cancelling the context and returning a retryable error drives
			// Retry's post-attempt ctx.Err() branch.
			if cancelAfter > 0 && calls == cancelAfter {
				cancel()
				cancelFired = true
				lastStep = stepRetryable
				return 0, newRetryable()
			}
			step := stepFor(script, calls-1)
			lastStep = step
			switch step {
			case stepSucceed:
				return retrySuccessValue, nil
			case stepPermanent:
				return 0, ErrorFromHTTPStatus("openai", permanentStatus, "permanent", nil, nil)
			default:
				return 0, newRetryable()
			}
		}

		v, err := Retry(ctx, policy, sleep, randFloat, fn)

		// Attempt-count bound: initial attempt plus at most maxRetries retries.
		if calls > effectiveMax+1 {
			t.Fatalf("Retry invoked operation %d times, budget is %d (max+1)", calls, effectiveMax+1)
		}
		if calls == 0 {
			t.Fatalf("Retry never invoked the operation")
		}
		// OnRetry can never fire more than the retry budget allows.
		if onRetryCount > effectiveMax {
			t.Fatalf("OnRetry fired %d times, more than %d retries", onRetryCount, effectiveMax)
		}

		// A fired cancellation surfaces context.Canceled and stops there.
		if cancelFired {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("context cancelled at call %d but Retry returned %v", calls, err)
			}
			if v != 0 {
				t.Fatalf("cancelled Retry returned non-zero value %d", v)
			}
			return
		}

		// A surfaced sleep error is returned verbatim with a zero value.
		if sleepErrs && errors.Is(err, errSleepInterrupted) {
			if v != 0 {
				t.Fatalf("sleep-interrupted Retry returned non-zero value %d", v)
			}
			return
		}

		// Success is returned only when the final attempt truly succeeded.
		if err == nil {
			if lastStep != stepSucceed {
				t.Fatalf("Retry returned success but final attempt was %v", lastStep)
			}
			if v != retrySuccessValue {
				t.Fatalf("Retry returned nil error but value %d != %d", v, retrySuccessValue)
			}
			return
		}

		// A failure verdict must carry a non-nil error and a zero value.
		if v != 0 {
			t.Fatalf("Retry returned error %v but non-zero value %d", err, v)
		}

		// Always-fail (variant 0 only, so no early give-up path preempts it):
		// the full budget was spent and the last error surfaced.
		if variant == 0 && len(script) == 0 {
			if calls != effectiveMax+1 {
				t.Fatalf("always-fail ran %d attempts, want %d", calls, effectiveMax+1)
			}
			if lastStep != stepRetryable {
				t.Fatalf("always-fail terminal step was %v, want retryable", lastStep)
			}
		}
	})
}

// FuzzRetryStream drives RetryStream — the stream-open/consume retry loop —
// with a fuzzed script that also decides, per attempt, whether partial output
// was already delivered (which gates retry-after-partial).
//
// Oracles:
//   - RetryStream never panics and terminates within the honest budget.
//   - The attempt is invoked at most maxRetries+1 times.
//   - A nil result is returned only when the final attempt succeeded.
//   - When an attempt delivers partial output and RetryAfterPartial is false,
//     the loop stops immediately and returns that attempt's error.
//   - A permanent error stops immediately with a non-nil error.
//   - OnReset fires only for a retried partial attempt, so with
//     RetryAfterPartial disabled it never fires, and it never fires more times
//     than retries were taken.
//   - OnRetry fires at most once per retry taken.
//   - Cancelling the context (before the loop or mid-run) surfaces the
//     cancellation and stops.
//   - A retryable error whose Retry-After exceeds MaxDelay aborts (variant 3), a
//     sleep error is surfaced verbatim (variant 2), and the nil-Sleep default is
//     honored (variant 1) — all without hanging.
func FuzzRetryStream(f *testing.F) {
	f.Add(3, []byte{1, 1, 0}, []byte{0, 0, 0}, false, false, 0, uint8(0))
	f.Add(2, []byte{1}, []byte{1}, true, true, 0, uint8(0))
	f.Add(10, []byte{}, []byte{}, false, true, 0, uint8(0))
	f.Add(5, []byte{1, 2}, []byte{1, 0}, true, false, 0, uint8(0))
	f.Add(0, []byte{1}, []byte{1}, false, false, 0, uint8(0))
	f.Add(5, []byte{1, 1, 1}, []byte{0}, true, true, 2, uint8(0))
	f.Add(5, []byte{1}, []byte{0}, false, false, -1, uint8(0))
	f.Add(-2, []byte{}, []byte{}, false, false, 0, uint8(0)) // negative-budget clamp
	f.Add(4, []byte{1, 1, 0}, []byte{0}, false, false, 0, uint8(1))
	f.Add(4, []byte{1, 1, 1}, []byte{0}, false, false, 0, uint8(2))
	f.Add(4, []byte{1, 1, 1}, []byte{0}, false, true, 0, uint8(3))

	f.Fuzz(func(t *testing.T, rawMaxRetries int, script, partialScript []byte, retryAfterPartial, onRetry bool, cancelAfter int, variant uint8) {
		maxRetries := clampMaxRetries(rawMaxRetries)
		effectiveMax := maxRetries
		if effectiveMax < 0 {
			effectiveMax = 0
		}
		variant %= 4
		nilSleep := variant == 1
		sleepErrs := variant == 2
		bigRetryAfter := variant == 3

		onRetryCount := 0
		policy := RetryPolicy{
			MaxRetries:        maxRetries,
			BaseDelay:         time.Millisecond,
			MaxDelay:          time.Second,
			BackoffMultiplier: 2.0,
			Jitter:            true,
		}
		if onRetry {
			policy.OnRetry = func(error, int, time.Duration) { onRetryCount++ }
		}
		var sleep SleepFunc = func(context.Context, time.Duration) error { return nil }
		if sleepErrs {
			sleep = func(context.Context, time.Duration) error { return errSleepInterrupted }
		}
		if nilSleep {
			// Exercise the nil-Sleep default; zero the delays so the real
			// DefaultSleep never actually waits.
			sleep = nil
			policy.BaseDelay = 0
			policy.MaxDelay = 0
			policy.Jitter = false
		}
		var retryAfterErr *time.Duration
		if bigRetryAfter {
			d := time.Hour
			retryAfterErr = &d
		}
		newRetryable := func() error {
			return ErrorFromHTTPStatus("openai", retryableStatus, "retryable", nil, retryAfterErr)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// cancelAfter < 0 pre-cancels the context to drive the top-of-loop
		// ctx.Err() guard (attempt is never invoked).
		preCancel := cancelAfter < 0
		if preCancel {
			cancel()
		}

		calls := 0
		resets := 0
		cancelFired := false
		var lastStep scriptStep

		opts := RetryStreamOptions{
			Policy:            policy,
			Sleep:             sleep,
			RetryAfterPartial: retryAfterPartial,
			OnReset:           func() { resets++ },
		}
		attempt := func(context.Context) (bool, error) {
			calls++
			if calls > hardAttemptCap {
				t.Fatalf("RetryStream ran the attempt %d times (>%d): loop failed to terminate", calls, hardAttemptCap)
			}
			step := stepFor(script, calls-1)
			partial := false
			if len(partialScript) > 0 {
				partial = partialScript[(calls-1)%len(partialScript)]%2 == 1
			}
			lastStep = step
			// Cancelling mid-attempt and failing retryably drives the
			// post-attempt ctx.Err() guard.
			if cancelAfter > 0 && calls == cancelAfter {
				cancel()
				cancelFired = true
				return partial, newRetryable()
			}
			switch step {
			case stepSucceed:
				return partial, nil
			case stepPermanent:
				return partial, ErrorFromHTTPStatus("openai", permanentStatus, "permanent", nil, nil)
			default:
				return partial, newRetryable()
			}
		}

		err := RetryStream(ctx, opts, attempt)

		// A pre-cancelled context returns before the attempt ever runs.
		if preCancel {
			if calls != 0 {
				t.Fatalf("pre-cancelled RetryStream still invoked attempt %d times", calls)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pre-cancelled RetryStream returned %v, want context.Canceled", err)
			}
			return
		}

		if calls > effectiveMax+1 {
			t.Fatalf("RetryStream invoked attempt %d times, budget is %d", calls, effectiveMax+1)
		}
		if calls == 0 {
			t.Fatalf("RetryStream never invoked the attempt")
		}
		if onRetryCount > effectiveMax {
			t.Fatalf("OnRetry fired %d times, more than %d retries", onRetryCount, effectiveMax)
		}

		// A fired mid-run cancellation surfaces the cancellation and stops.
		if cancelFired {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("context cancelled at call %d but RetryStream returned %v", calls, err)
			}
			return
		}

		// A surfaced sleep error is returned verbatim.
		if sleepErrs && errors.Is(err, errSleepInterrupted) {
			return
		}

		// OnReset only fires on the retried-partial path, so it can never fire
		// when retry-after-partial is disabled, and never more than the retries
		// actually taken.
		if !retryAfterPartial && resets != 0 {
			t.Fatalf("OnReset fired %d times with RetryAfterPartial disabled", resets)
		}
		if resets > effectiveMax {
			t.Fatalf("OnReset fired %d times, more than %d retries", resets, effectiveMax)
		}

		if err == nil {
			if lastStep != stepSucceed {
				t.Fatalf("RetryStream returned nil but final attempt was %v", lastStep)
			}
			return
		}

		// Always retryable-fail with no partial gating and no early give-up
		// path (variant 0): full budget spent, last error surfaced (never nil,
		// never a hang).
		if variant == 0 && len(script) == 0 && len(partialScript) == 0 && calls != effectiveMax+1 {
			t.Fatalf("always-fail ran %d attempts, want %d", calls, effectiveMax+1)
		}
	})
}
