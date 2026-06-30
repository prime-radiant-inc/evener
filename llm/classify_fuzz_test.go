package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// FuzzClassify drives the retry-class classifier over a diverse error space —
// the kinds Classify branches on (nil, cancellation, deadline, AbortError, an
// llm.Error with an arbitrary HTTP status / provider / message, a bare error)
// optionally wrapped. Classify is a pure function over an adversarial input that
// only unit tests touched; this puts arbitrary inputs through every branch
// (including isEndpointFallbackSignal and ErrorClass.String).
//
// Oracles (never bare no-panic):
//   - Classify never panics and returns one of the three defined classes.
//   - ErrorClass.String never panics and is non-empty for a valid class.
//   - Classification is deterministic for a given error.
//   - The documented invariants hold: nil -> Retryable; context.Canceled and
//     *AbortError -> Permanent (even when wrapped); a fallback verdict only ever
//     arises from an llm.Error.
func FuzzClassify(f *testing.F) {
	f.Add(uint8(0), 400, "openai", "bad request", uint8(0))
	f.Add(uint8(5), 404, "openai", "responses.create failed for /v1/responses", uint8(1))
	f.Add(uint8(5), 429, "anthropic", "rate limited", uint8(0))
	f.Add(uint8(2), 0, "", "", uint8(2))
	f.Add(uint8(4), 0, "", "aborted by user", uint8(0))

	f.Fuzz(func(t *testing.T, kind uint8, status int, provider, msg string, wrap uint8) {
		isCancel, isAbort, isLLMErr, base := buildClassifyError(kind, status, provider, msg)
		err := wrapErr(base, wrap, msg)

		class := Classify(err)
		if class < ErrorClassRetryable || class > ErrorClassFallback {
			t.Fatalf("Classify returned out-of-range class %d for %v", class, err)
		}
		if s := class.String(); s == "" || s == "unknown" {
			t.Fatalf("ErrorClass(%d).String() = %q for a valid class", class, s)
		}
		if again := Classify(err); again != class {
			t.Fatalf("Classify not deterministic: %d then %d for %v", class, again, err)
		}

		switch {
		case base == nil && wrap == 0:
			if class != ErrorClassRetryable {
				t.Fatalf("nil error classified %s, want retryable", class)
			}
		case isCancel || isAbort:
			if class != ErrorClassPermanent {
				t.Fatalf("cancellation/abort classified %s, want permanent (err=%v)", class, err)
			}
		}

		// A Fallback verdict can only come from an llm.Error (the only branch that
		// returns it). If we never produced one, fallback must not appear.
		if class == ErrorClassFallback && !isLLMErr {
			t.Fatalf("non-llm.Error classified as fallback: %v", err)
		}
	})
}

// buildClassifyError maps the fuzzer's selector to one of the error shapes
// Classify distinguishes, reporting which invariant-bearing shape it built.
func buildClassifyError(kind uint8, status int, provider, msg string) (isCancel, isAbort, isLLMErr bool, err error) {
	switch kind % 6 {
	case 0:
		return false, false, false, nil
	case 1:
		return false, false, false, errors.New(msg)
	case 2:
		return true, false, false, context.Canceled
	case 3:
		return false, false, false, context.DeadlineExceeded
	case 4:
		return false, true, false, NewAbortError(msg, nil)
	default:
		return false, false, true, ErrorFromHTTPStatus(provider, status, msg, nil, nil)
	}
}

// wrapErr optionally wraps err to exercise Classify's errors.As/errors.Is
// unwrapping. A nil base stays nil so the nil invariant is still reachable.
func wrapErr(err error, wrap uint8, msg string) error {
	if err == nil {
		return nil
	}
	switch wrap % 3 {
	case 1:
		return fmt.Errorf("wrapped: %w", err)
	case 2:
		return fmt.Errorf("%s: %w (more context)", msg, err)
	default:
		return err
	}
}
