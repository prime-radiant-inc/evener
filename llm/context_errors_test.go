package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// WrapContextError must produce errors that honestly unwrap to the context
// sentinel they were built from, so errors.Is works for downstream callers
// (the agent's cancellation/drain logic, external consumers).
func TestWrapContextError_AbortUnwrapsToCanceled(t *testing.T) {
	err := WrapContextError("openai", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("errors.Is(WrapContextError(Canceled), context.Canceled) = false, want true")
	}
	var abort *AbortError
	if !errors.As(err, &abort) {
		t.Fatal("WrapContextError(Canceled) is not *AbortError")
	}
}

func TestWrapContextError_TimeoutUnwrapsToDeadline(t *testing.T) {
	err := WrapContextError("openai", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("errors.Is(WrapContextError(DeadlineExceeded), context.DeadlineExceeded) = false, want true")
	}
	var rt *requestTimeoutError
	if !errors.As(err, &rt) {
		t.Fatal("WrapContextError(DeadlineExceeded) is not *RequestTimeoutError")
	}
}

func TestWrapContextError_WrappedCanceledUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("dialing upstream: %w", context.Canceled)
	err := WrapContextError("openai", wrapped)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("errors.Is(WrapContextError(wrapped Canceled), context.Canceled) = false, want true")
	}
}

func TestWrapContextError_PassThrough(t *testing.T) {
	sentinel := errors.New("boom")
	if got := WrapContextError("openai", sentinel); !errors.Is(got, sentinel) {
		t.Fatalf("WrapContextError(non-context) = %v, want the error returned unchanged", got)
	}
	if WrapContextError("openai", nil) != nil {
		t.Fatal("WrapContextError(nil) != nil")
	}
}

// Classify's output must be identical after honest Unwrap: its context-sentinel
// branches and its typed-error branches already agreed for these inputs.
func TestClassify_UnchangedByHonestUnwrap(t *testing.T) {
	if got := Classify(WrapContextError("openai", context.Canceled)); got != ErrorClassPermanent {
		t.Fatalf("Classify(abort) = %v, want Permanent", got)
	}
	if got := Classify(WrapContextError("openai", context.DeadlineExceeded)); got != ErrorClassRetryable {
		t.Fatalf("Classify(timeout) = %v, want Retryable", got)
	}
}

// Regression (the panel's key find): a RequestTimeoutError that wraps
// context.DeadlineExceeded must stay retryable. retryableError short-circuits
// only on BARE context sentinels; a typed llm timeout is a transient HTTP-level
// timeout, not budget exhaustion.
func TestRetryableError_RequestTimeoutWrappingDeadline_StaysRetryable(t *testing.T) {
	err := WrapContextError("openai", context.DeadlineExceeded)
	if !retryableError(err) {
		t.Fatal("retryableError(RequestTimeoutError wrapping DeadlineExceeded) = false, want true")
	}
}

func TestRetryableError_BareContext_NotRetryable(t *testing.T) {
	if retryableError(context.DeadlineExceeded) {
		t.Fatal("retryableError(bare DeadlineExceeded) = true, want false (budget exhausted)")
	}
	if retryableError(context.Canceled) {
		t.Fatal("retryableError(bare Canceled) = true, want false")
	}
}

// An AbortError wrapping context.Canceled must not retry (user cancellation is
// permanent), whether caught by the bare-sentinel guard or the classifier.
func TestRetryableError_AbortWrappingCanceled_NotRetryable(t *testing.T) {
	if retryableError(WrapContextError("openai", context.Canceled)) {
		t.Fatal("retryableError(AbortError wrapping Canceled) = true, want false")
	}
}

// NewStreamError carries an optional underlying cause so adapters can surface
// the real mid-stream read failure (e.g. an SSE parse error) via errors.Is.
func TestNewStreamError_UnwrapsCause(t *testing.T) {
	cause := errors.New("sse parse failed")
	err := NewStreamError("anthropic", "stream read failed", cause)
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is(NewStreamError(..., cause), cause) = false, want true")
	}
}

func TestNewStreamError_NilCauseDoesNotUnwrap(t *testing.T) {
	if errors.Unwrap(NewStreamError("openai", "stream ended", nil)) != nil {
		t.Fatal("NewStreamError(..., nil) should not unwrap to anything")
	}
}
