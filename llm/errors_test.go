package llm

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseRetryAfter_Seconds(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("12", now)
	if d == nil || *d != 12*time.Second {
		t.Fatalf("got %v want 12s", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("Sat, 07 Feb 2026 00:00:10 GMT", now)
	if d == nil || *d != 10*time.Second {
		t.Fatalf("got %v want 10s", d)
	}
}

func TestErrorFromHTTPStatus_MappingAndRetryable(t *testing.T) {
	cases := []struct {
		status    int
		wantType  any
		retryable bool
	}{
		{status: 400, wantType: &InvalidRequestError{}, retryable: false},
		{status: 401, wantType: &AuthenticationError{}, retryable: false},
		{status: 403, wantType: &AccessDeniedError{}, retryable: false},
		{status: 404, wantType: &NotFoundError{}, retryable: false},
		{status: 408, wantType: &RequestTimeoutError{}, retryable: true},
		{status: 413, wantType: &ContextLengthError{}, retryable: false},
		{status: 422, wantType: &InvalidRequestError{}, retryable: false},
		{status: 429, wantType: &RateLimitError{}, retryable: true},
		{status: 500, wantType: &ServerError{}, retryable: true},
		{status: 503, wantType: &ServerError{}, retryable: true},
		{status: 599, wantType: &UnknownHTTPError{}, retryable: true},
	}
	for _, tc := range cases {
		err := ErrorFromHTTPStatus("p", tc.status, "msg", nil, nil)
		switch tc.wantType.(type) {
		case *InvalidRequestError:
			if _, ok := err.(*InvalidRequestError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AuthenticationError:
			if _, ok := err.(*AuthenticationError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AccessDeniedError:
			if _, ok := err.(*AccessDeniedError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *NotFoundError:
			if _, ok := err.(*NotFoundError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RequestTimeoutError:
			if _, ok := err.(*RequestTimeoutError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ContextLengthError:
			if _, ok := err.(*ContextLengthError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RateLimitError:
			if _, ok := err.(*RateLimitError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ServerError:
			if _, ok := err.(*ServerError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *UnknownHTTPError:
			if _, ok := err.(*UnknownHTTPError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		}
		e, ok := err.(Error)
		if !ok {
			t.Fatalf("status %d: not an llm.Error (%T)", tc.status, err)
		}
		if e.Retryable() != tc.retryable {
			t.Fatalf("status %d: retryable=%t want %t", tc.status, e.Retryable(), tc.retryable)
		}
	}
}

func TestContentFilterError_ImplementsErrorInterface(t *testing.T) {
	err := &ContentFilterError{httpErrorBase{provider: "test", statusCode: 400, message: "blocked", retryable: false}}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("ContentFilterError does not implement Error interface")
	}
	if llmErr.Provider() != "test" {
		t.Fatalf("Provider: %q", llmErr.Provider())
	}
	if llmErr.Retryable() {
		t.Fatalf("expected non-retryable")
	}
}

func TestQuotaExceededError_ImplementsErrorInterface(t *testing.T) {
	err := &QuotaExceededError{httpErrorBase{provider: "test", statusCode: 429, message: "quota exceeded", retryable: false}}
	var llmErr Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("QuotaExceededError does not implement Error interface")
	}
	if llmErr.Provider() != "test" {
		t.Fatalf("Provider: %q", llmErr.Provider())
	}
	if llmErr.Retryable() {
		t.Fatalf("expected non-retryable")
	}
}

func TestErrorCode_OnHTTPErrors(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"code": "model_not_found"}}
	err := ErrorFromHTTPStatus("openai", 404, "not found", raw, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.ErrorCode() != "model_not_found" {
		t.Fatalf("ErrorCode() = %q, want %q", e.ErrorCode(), "model_not_found")
	}
}

func TestErrorCode_FallsBackToType(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"type": "invalid_request_error"}}
	err := ErrorFromHTTPStatus("anthropic", 400, "bad request", raw, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.ErrorCode() != "invalid_request_error" {
		t.Fatalf("ErrorCode() = %q, want %q", e.ErrorCode(), "invalid_request_error")
	}
}

func TestErrorCode_EmptyWhenNoRaw(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 500, "server error", nil, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.ErrorCode() != "" {
		t.Fatalf("ErrorCode() = %q, want empty", e.ErrorCode())
	}
}

func TestRaw_ExposesRawResponse(t *testing.T) {
	raw := map[string]any{"error": "details"}
	err := ErrorFromHTTPStatus("openai", 500, "server error", raw, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	got := e.Raw()
	if got == nil {
		t.Fatal("Raw() returned nil")
	}
	rawMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("Raw() type = %T, want map[string]any", got)
	}
	if rawMap["error"] != "details" {
		t.Fatalf("Raw() = %v", rawMap)
	}
}

func TestUnwrap_ErrorChain(t *testing.T) {
	cause := fmt.Errorf("underlying network problem")
	err := &ServerError{httpErrorBase{
		provider:   "openai",
		statusCode: 500,
		message:    "server error",
		retryable:  true,
		cause:      cause,
	}}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find cause through Unwrap")
	}
}

func TestNonHTTPError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("context canceled")
	err := &AbortError{nonHTTPErrorBase{
		message: "aborted",
		cause:   cause,
	}}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find cause through Unwrap on non-HTTP errors")
	}
}

func TestNonHTTPError_ErrorCode_Raw(t *testing.T) {
	err := &AbortError{nonHTTPErrorBase{message: "aborted"}}
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.ErrorCode() != "" {
		t.Fatalf("ErrorCode() = %q, want empty", e.ErrorCode())
	}
	if e.Raw() != nil {
		t.Fatalf("Raw() = %v, want nil", e.Raw())
	}
}

func TestErrorFromHTTPStatus_ErrorCodeClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		message   string
		errorCode string
		want      string
	}{
		{
			"invalid_prompt code",
			400,
			"some generic message",
			"invalid_prompt",
			"*llm.ContentFilterError",
		},
		{
			"content_policy_violation code",
			400,
			"some generic message",
			"content_policy_violation",
			"*llm.ContentFilterError",
		},
		{
			"unrecognized code falls through",
			400,
			"bad request",
			"some_other_code",
			"*llm.InvalidRequestError",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"error": map[string]any{"code": tc.errorCode}}
			err := ErrorFromHTTPStatus("openai", tc.status, tc.message, raw, nil)
			got := fmt.Sprintf("%T", err)
			if got != tc.want {
				t.Fatalf("ErrorFromHTTPStatus(%d, code=%q) = %s, want %s", tc.status, tc.errorCode, got, tc.want)
			}
		})
	}
}

func TestConfigurationError_Unwrap_ExposesCause(t *testing.T) {
	cause := fmt.Errorf("missing API key")
	err := &ConfigurationError{Message: "bad config", Cause: cause}

	// Unwrap returns cause.
	if got := errors.Unwrap(err); got != cause {
		t.Fatalf("Unwrap() = %v, want %v", got, cause)
	}
	// errors.Is traverses the chain.
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find cause through Unwrap")
	}
}

func TestConfigurationError_Unwrap_NilCause(t *testing.T) {
	err := &ConfigurationError{Message: "no cause"}
	if got := errors.Unwrap(err); got != nil {
		t.Fatalf("Unwrap() = %v, want nil", got)
	}
}

func TestConfigurationError_ErrorCode_Raw(t *testing.T) {
	err := &ConfigurationError{Message: "bad config"}
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.ErrorCode() != "" {
		t.Fatalf("ErrorCode() = %q, want empty", e.ErrorCode())
	}
	if e.Raw() != nil {
		t.Fatalf("Raw() = %v, want nil", e.Raw())
	}
}

func TestNewRequestTimeoutError_IsRetryable(t *testing.T) {
	err := NewRequestTimeoutError("openai", `Post "https://api.openai.com/v1/responses": context deadline exceeded`)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if !e.Retryable() {
		t.Fatal("NewRequestTimeoutError should be retryable (HTTP-level timeouts should be retried)")
	}
	if e.StatusCode() != 0 {
		t.Fatalf("StatusCode() = %d, want 0", e.StatusCode())
	}
	// Verify the retry util also considers it retryable.
	if !retryableError(err) {
		t.Fatal("retryableError() should return true for NewRequestTimeoutError")
	}
}

func TestErrorFromHTTPStatus_MessageBasedClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		want    string // expected error type name
	}{
		// Ambiguous 400 classified by message.
		{"400 content filter", 400, "content filter policy violated", "*llm.ContentFilterError"},
		{"400 safety", 400, "blocked by safety settings", "*llm.ContentFilterError"},
		{"400 context length", 400, "context length exceeded", "*llm.ContextLengthError"},
		{"400 too many tokens", 400, "too many tokens in request", "*llm.ContextLengthError"},
		{"400 quota", 400, "quota exceeded for billing account", "*llm.QuotaExceededError"},
		{"400 billing", 400, "billing issue on account", "*llm.QuotaExceededError"},
		{"400 not found", 400, "model does not exist", "*llm.NotFoundError"},
		{"400 plain", 400, "bad request", "*llm.InvalidRequestError"},

		// Unambiguous status codes should NOT be overridden by message.
		{"401 always auth", 401, "content filter something", "*llm.AuthenticationError"},
		{"429 always rate", 429, "quota exceeded", "*llm.RateLimitError"},
		{"404 always notfound", 404, "quota exceeded", "*llm.NotFoundError"},

		// 422 is ambiguous like 400.
		{"422 content filter", 422, "this violates safety policy", "*llm.ContentFilterError"},
		{"422 plain", 422, "invalid field", "*llm.InvalidRequestError"},

		// OpenAI usage policy violation (invalid_prompt).
		{"400 usage policy", 400, "Your prompt was flagged as potentially violating our usage policy", "*llm.ContentFilterError"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrorFromHTTPStatus("p", tc.status, tc.message, nil, nil)
			got := fmt.Sprintf("%T", err)
			if got != tc.want {
				t.Fatalf("ErrorFromHTTPStatus(%d, %q) = %s, want %s", tc.status, tc.message, got, tc.want)
			}
		})
	}
}
