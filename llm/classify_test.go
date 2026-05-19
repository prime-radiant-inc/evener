package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyTimeoutIsRetryable(t *testing.T) {
	if got := Classify(context.DeadlineExceeded); got != ErrorClassRetryable {
		t.Fatalf("Classify(DeadlineExceeded) = %v, want Retryable", got)
	}
	// Also a wrapped form via NewRequestTimeoutError (non-HTTP timeout).
	err := NewRequestTimeoutError("openai", "deadline exceeded")
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(RequestTimeoutError) = %v, want Retryable", got)
	}
}

func TestClassify429IsRetryable(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, nil)
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(429) = %v, want Retryable", got)
	}
}

func TestClassify503IsRetryable(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 503, "service unavailable", nil, nil)
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(503) = %v, want Retryable", got)
	}
}

func TestClassify403IsPermanent(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(403) = %v, want Permanent", got)
	}
}

func TestClassify403CyberPolicyIsRetryable(t *testing.T) {
	// A 403 that the provider marks as retryable (cyber_policy_violation)
	// must classify as Retryable so we don't lose the existing exemption.
	raw := map[string]any{"error": map[string]any{"code": "cyber_policy_violation"}}
	err := ErrorFromHTTPStatus("openai", 403, "flagged", raw, nil)
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(403 cyber_policy_violation) = %v, want Retryable", got)
	}
}

func TestClassify404IsPermanent(t *testing.T) {
	// 404 model-not-found / endpoint-not-found is permanent for the retry
	// budget. The Responses->ChatCompletions fallback path classifies via
	// ErrorClassFallback (covered separately).
	err := ErrorFromHTTPStatus("openai", 404, "model not found", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(404) = %v, want Permanent", got)
	}
}

func TestClassifyOpenAIResponses404IsFallback(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 404, "responses.create(stream) failed: model not found", nil, nil)
	if got := Classify(err); got != ErrorClassFallback {
		t.Fatalf("Classify(openai responses 404) = %v, want Fallback", got)
	}
}

func TestClassifyOpenAIResponsesEmptyStreamIsFallback(t *testing.T) {
	err := NewStreamError("openai", "/v1/responses: empty stream (model not supported)")
	if got := Classify(err); got != ErrorClassFallback {
		t.Fatalf("Classify(openai responses empty stream) = %v, want Fallback", got)
	}
}

func TestClassify400IsPermanent(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 400, "bad request", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(400) = %v, want Permanent", got)
	}
}

func TestClassify401IsPermanent(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(401) = %v, want Permanent", got)
	}
}

func TestClassifyStreamEndedIsRetryable(t *testing.T) {
	err := NewStreamError("openai", "stream ended without finish event")
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(StreamError) = %v, want Retryable", got)
	}
}

func TestClassifyDefaultRetryable(t *testing.T) {
	// Unknown / unwrapped errors fall through to Retryable (conservative).
	err := errors.New("some random transport hiccup")
	if got := Classify(err); got != ErrorClassRetryable {
		t.Fatalf("Classify(unknown) = %v, want Retryable", got)
	}
}

func TestClassifyNilIsRetryable(t *testing.T) {
	// nil sentinel: Classify on nil shouldn't panic. Default to Retryable
	// (callers should check err != nil before calling Classify anyway).
	if got := Classify(nil); got != ErrorClassRetryable {
		t.Fatalf("Classify(nil) = %v, want Retryable", got)
	}
}

func TestClassifyContextCanceledIsPermanent(t *testing.T) {
	// User-initiated cancellation: permanent (do not retry). The existing
	// retryableError helper also bails on context.Canceled.
	if got := Classify(context.Canceled); got != ErrorClassPermanent {
		t.Fatalf("Classify(Canceled) = %v, want Permanent", got)
	}
}

func TestClassifyAbortErrorIsPermanent(t *testing.T) {
	err := NewAbortError("user canceled")
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(AbortError) = %v, want Permanent", got)
	}
}

func TestClassifyStringerHasName(t *testing.T) {
	// ErrorClass should stringify legibly for logs.
	got := fmt.Sprintf("%s/%s/%s", ErrorClassRetryable, ErrorClassPermanent, ErrorClassFallback)
	want := "retryable/permanent/fallback"
	if got != want {
		t.Fatalf("ErrorClass strings = %q, want %q", got, want)
	}
}
