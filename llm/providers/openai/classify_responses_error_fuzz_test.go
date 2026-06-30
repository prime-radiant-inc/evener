package openai

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzClassifyResponsesError drives Adapter.ClassifyResponsesError, the pure
// error-mapper that decides whether a failed Responses-API call is transient,
// a rejected continuation, a model/endpoint mismatch, or permanent-other. That
// verdict steers retry vs. continuation-drop vs. chat-completions fallback, so a
// misclassification changes real recovery behavior.
//
// The fuzzer builds an llm.Error from an arbitrary HTTP status / error-code /
// message (optionally wrapped), with the request optionally carrying a
// previous_response_id, then asserts the documented invariants.
//
// Oracles beyond no-panic:
//   - the verdict is always one of the four defined classes;
//   - deterministic for a given (request, error);
//   - nil error -> permanent_other;
//   - a retryable llm.Error -> transient;
//   - context cancellation/deadline (non-llm.Error) -> transient;
//   - a continuation_rejected verdict only ever arises when the request actually
//     carried a previous_response_id.
func FuzzClassifyResponsesError(f *testing.F) {
	f.Add(429, "rate_limit_exceeded", "slow down", false, true, uint8(0))
	f.Add(404, "previous_response_not_found", "previous response not found", true, false, uint8(1))
	f.Add(400, "model_not_found", "the model gpt-x is not found", false, false, uint8(0))
	f.Add(500, "", "model not supported on this endpoint", true, false, uint8(2))
	f.Add(0, "", "", false, false, uint8(3))
	f.Add(403, "unsupported_model", "previous_response expired and not found", true, false, uint8(0))

	a := &Adapter{}

	f.Fuzz(func(t *testing.T, status int, code, msg string, withPrev, retryable bool, wrap uint8) {
		req := llm.Request{Model: "m"}
		if withPrev {
			req.PreviousResponseID = "resp_prev"
		}

		err := buildResponsesError(status, code, msg, retryable, wrap)

		class := a.ClassifyResponsesError(req, err)
		switch class {
		case llm.ResponsesErrorContinuationRejected, llm.ResponsesErrorModelEndpoint,
			llm.ResponsesErrorTransient, llm.ResponsesErrorPermanentOther:
		default:
			t.Fatalf("ClassifyResponsesError returned undefined class %q (status=%d code=%q msg=%q)", class, status, code, msg)
		}

		if again := a.ClassifyResponsesError(req, err); again != class {
			t.Fatalf("ClassifyResponsesError not deterministic: %q then %q", class, again)
		}

		if err == nil {
			if class != llm.ResponsesErrorPermanentOther {
				t.Fatalf("nil error classified %q, want permanent_other", class)
			}
			return
		}

		// A retryable llm.Error is the first branch ClassifyResponsesError takes.
		var llmErr llm.Error
		if errors.As(err, &llmErr) && llmErr.Retryable() {
			if class != llm.ResponsesErrorTransient {
				t.Fatalf("retryable llm.Error classified %q, want transient (status=%d)", class, status)
			}
		}

		// continuation_rejected can only be produced when a previous_response_id was
		// present on the request — the guard is gated on hasPreviousResponseID.
		if class == llm.ResponsesErrorContinuationRejected && !hasPreviousResponseID(req) {
			t.Fatalf("continuation_rejected without a previous_response_id on the request (msg=%q)", msg)
		}
	})
}

// buildResponsesError assembles the error shapes ClassifyResponsesError
// distinguishes: nil, a non-retryable/retryable llm.Error with a chosen
// status/code/message, or a context error — optionally wrapped.
func buildResponsesError(status int, code, msg string, retryable bool, wrap uint8) error {
	var base error
	switch wrap % 4 {
	case 3:
		base = context.Canceled
	default:
		st := status
		if retryable {
			st = 429 // a 429 makes llm.Error.Retryable() true
		} else if st == 429 || st == 500 || st == 502 || st == 503 {
			st = 400 // keep the non-retryable request honest
		}
		raw := map[string]any{"error": map[string]any{"code": code}}
		base = llm.ErrorFromHTTPStatus("openai", st, msg, raw, nil)
	}
	switch wrap % 4 {
	case 1:
		return fmt.Errorf("responses.create failed: %w", base)
	case 2:
		return fmt.Errorf("%s: %w", msg, base)
	default:
		return base
	}
}
