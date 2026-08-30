package llm

import (
	"context"
	"errors"
)

// ErrorClass describes how a provider error should affect the retry chain.
//
// The classifier is conservative: unknown / unclassifiable errors fall back to
// ErrorClassRetryable. Callers (the StreamGenerate retry loop, the Retry
// helper, etc.) branch on the class to decide whether to burn additional
// budget or give up.
type ErrorClass int

const (
	// ErrorClassRetryable covers transient failures that the retry chain
	// should treat with the full budget: 408/429/5xx, request timeouts,
	// stream-ended-without-finish sentinels, and unclassified errors.
	ErrorClassRetryable ErrorClass = iota

	// ErrorClassPermanent covers failures that will not improve by retrying
	// the same request: 400 bad-request, 401 unauthenticated, 403 access
	// denied, 404 not-found, 413 context-length, 422 invalid request, plus
	// user-initiated cancellation. These must short-circuit the retry loop.
	ErrorClassPermanent
)

// String implements fmt.Stringer for legible log/metric output.
func (c ErrorClass) String() string {
	switch c {
	case ErrorClassRetryable:
		return "retryable"
	case ErrorClassPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// Classify returns the retry-class for err. Safe to call on nil (returns
// Retryable — callers should check err != nil before classifying anyway).
//
// Order of checks:
//  1. User cancellation (context.Canceled, *AbortError) → Permanent.
//  2. llm.Error with an HTTP status code → classified by status.
//     A status carrying Retryable()==true (e.g. 403 cyber_policy_violation
//     which is a temporary account ban) stays Retryable even if the bare
//     status would normally be Permanent.
//  3. Any other llm.Error → use its Retryable() bit.
//  4. Bare context deadline (context.DeadlineExceeded) → Retryable.
//  5. Default → Retryable (conservative).
//
// This mirrors the retryableError helper but exposes the classification so
// the retry loop can branch explicitly instead of relying on a single
// boolean.
func Classify(err error) ErrorClass {
	if err == nil {
		return ErrorClassRetryable
	}

	// User-initiated cancellation is permanent (never retry).
	if errors.Is(err, context.Canceled) {
		return ErrorClassPermanent
	}
	if _, ok := errors.AsType[*AbortError](err); ok {
		return ErrorClassPermanent
	}

	if le, ok := errors.AsType[Error](err); ok {
		// A provider may flag an otherwise-permanent status as retryable
		// (e.g. OpenAI cyber_policy_violation on 403). Honor that bit first.
		if le.Retryable() {
			return ErrorClassRetryable
		}
		// An exhausted quota arrives as a 429, which the status switch below
		// maps to Retryable. Honor the category first, or the retry chain
		// spends its whole budget on an allowance that resets in days.
		if Kind(err) == KindQuotaExceeded {
			return ErrorClassPermanent
		}
		switch le.StatusCode() {
		case 400, 401, 403, 404, 413, 422:
			return ErrorClassPermanent
		case 408, 429, 500, 502, 503, 504:
			return ErrorClassRetryable
		}
		// Non-HTTP llm.Error with Retryable()==false (AbortError handled
		// above, NoObjectGeneratedError, UnsupportedToolChoiceError, etc.):
		// treat as Permanent.
		return ErrorClassPermanent
	}

	// An unclassified deadline is retryable because the server may not have
	// received the request. Phase-specific typed timeouts decide above.
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassRetryable
	}

	// Unknown / unwrapped error: default to Retryable (conservative — when
	// in doubt, retry — matches retryableError's existing behavior).
	return ErrorClassRetryable
}
