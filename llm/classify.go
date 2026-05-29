package llm

import (
	"context"
	"errors"
	"strings"
)

// ErrorClass describes how a provider error should affect the retry chain.
//
// The classifier is conservative: unknown / unclassifiable errors fall back to
// ErrorClassRetryable. Callers (the StreamGenerate retry loop, the Retry
// helper, etc.) branch on the class to decide whether to burn additional
// budget, give up, or trigger an endpoint fallback.
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

	// ErrorClassFallback covers errors that signal "this endpoint won't
	// work for this model — try a different one." Today this is the
	// OpenAI Responses->ChatCompletions handoff (404/422 from /v1/responses
	// and the errEmptyResponsesStream sentinel). The retry chain itself
	// treats Fallback the same as Permanent (short-circuit) because the
	// fallback handoff happens inside the adapter, below the retry chain.
	ErrorClassFallback
)

// String implements fmt.Stringer for legible log/metric output.
func (c ErrorClass) String() string {
	switch c {
	case ErrorClassRetryable:
		return "retryable"
	case ErrorClassPermanent:
		return "permanent"
	case ErrorClassFallback:
		return "fallback"
	default:
		return "unknown"
	}
}

// Classify returns the retry-class for err. Safe to call on nil (returns
// Retryable — callers should check err != nil before classifying anyway).
//
// Order of checks:
//  1. User cancellation (context.Canceled, *AbortError) → Permanent.
//  2. Context deadline (context.DeadlineExceeded) → Retryable.
//  3. llm.Error with an HTTP status code → classified by status.
//     A status carrying Retryable()==true (e.g. 403 cyber_policy_violation
//     which is a temporary account ban) stays Retryable even if the bare
//     status would normally be Permanent.
//  4. Any other llm.Error → use its Retryable() bit.
//  5. Default → Retryable (conservative).
//
// This mirrors the retryableError helper but exposes the classification
// (including the Fallback signal) so the retry loop can branch explicitly
// instead of relying on a single boolean.
func Classify(err error) ErrorClass {
	if err == nil {
		return ErrorClassRetryable
	}

	// User-initiated cancellation is permanent (never retry).
	if errors.Is(err, context.Canceled) {
		return ErrorClassPermanent
	}
	var abort *AbortError
	if errors.As(err, &abort) {
		return ErrorClassPermanent
	}

	// Deadline exceeded: retryable (server may not have received the request).
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClassRetryable
	}

	var le Error
	if errors.As(err, &le) {
		if isEndpointFallbackSignal(err, le) {
			return ErrorClassFallback
		}
		// A provider may flag an otherwise-permanent status as retryable
		// (e.g. OpenAI cyber_policy_violation on 403). Honor that bit first.
		if le.Retryable() {
			return ErrorClassRetryable
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

	// Unknown / unwrapped error: default to Retryable (conservative — when
	// in doubt, retry — matches retryableError's existing behavior).
	return ErrorClassRetryable
}

func isEndpointFallbackSignal(err error, le Error) bool {
	// Key on BehaviorTag when set (instance named "work" with tag "openai"
	// must still trigger fallback); fall back to Provider() for the default
	// identity case (tag empty, name==type=="openai").
	tag := le.BehaviorTag()
	if tag == "" {
		tag = le.Provider()
	}
	if !strings.EqualFold(strings.TrimSpace(tag), "openai") {
		return false
	}
	message := strings.ToLower(err.Error())
	switch le.StatusCode() {
	case 404, 422:
		return strings.Contains(message, "responses.create") ||
			strings.Contains(message, "/v1/responses")
	}
	return strings.Contains(message, "responses stream closed with no events") ||
		(strings.Contains(message, "/v1/responses") && strings.Contains(message, "empty stream"))
}
