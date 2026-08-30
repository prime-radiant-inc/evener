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
	err := NewRequestTimeoutError("openai", "deadline exceeded", nil)
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
	// budget.
	err := ErrorFromHTTPStatus("openai", 404, "model not found", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(404) = %v, want Permanent", got)
	}
}

// TestClassifyOpenAIResponses404IsPermanent pins the flag day: a 404 naming the
// Responses endpoint is an ordinary permanent error now that the
// Responses->ChatCompletions handoff is gone, so it short-circuits the retry
// chain and feeds the model-fallback chain like any other permanent failure.
func TestClassifyOpenAIResponses404IsPermanent(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 404, "responses.create(stream) failed: model not found", nil, nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(openai responses 404) = %v, want Permanent", got)
	}
}

// TestClassifyOpenAIResponsesEmptyStreamIsPermanent pins the other half: a
// Responses stream that closes 200 OK without a single recognized event says
// the model does not speak this protocol. Retrying cannot help, so the retry
// chain must short-circuit and the caller's model-fallback chain must run —
// which needs both a permanent class and a kind the fallback routes act on.
func TestClassifyOpenAIResponsesEmptyStreamIsPermanent(t *testing.T) {
	err := NewUnsupportedEndpointError("openai", "responses stream closed with no events", nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(openai responses empty stream) = %v, want Permanent", got)
	}
	if got := Kind(err); got != KindNotFound {
		t.Fatalf("Kind(openai responses empty stream) = %v, want KindNotFound", got)
	}
	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("empty-stream sentinel %T does not implement llm.Error", err)
	}
	if le.Retryable() {
		t.Fatal("empty-stream sentinel reports itself retryable")
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
	err := NewStreamError("openai", "stream ended without finish event", nil)
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
	err := NewAbortError("user canceled", nil)
	if got := Classify(err); got != ErrorClassPermanent {
		t.Fatalf("Classify(AbortError) = %v, want Permanent", got)
	}
}

// TestClassify_BehaviorTagNeverChangesTheClass pins that the classifier no
// longer keys on provider identity at all: with the endpoint handoff deleted,
// a Responses 404 is Permanent whether it arrives from "openai", from a
// renamed instance tagged "openai", or from one tagged "anthropic".
func TestClassify_BehaviorTagNeverChangesTheClass(t *testing.T) {
	for _, tag := range []string{"", "openai", "anthropic"} {
		err := ErrorFromHTTPStatus("work", 404, "responses.create(stream) failed: model not found", nil, nil)
		if tag != "" {
			var bs behaviorTagSetter
			if !errors.As(err, &bs) {
				t.Fatalf("expected behaviorTagSetter, got %T", err)
			}
			bs.setBehaviorTag(tag)
		}
		if got := Classify(err); got != ErrorClassPermanent {
			t.Fatalf("Classify(work/tag=%q responses 404) = %v, want Permanent", tag, got)
		}
	}
}

func TestClassifyStringerHasName(t *testing.T) {
	// ErrorClass should stringify legibly for logs.
	got := fmt.Sprintf("%s/%s", ErrorClassRetryable, ErrorClassPermanent)
	want := "retryable/permanent"
	if got != want {
		t.Fatalf("ErrorClass strings = %q, want %q", got, want)
	}
}
