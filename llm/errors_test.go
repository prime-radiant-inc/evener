package llm

import (
	"errors"
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
		{status: 400, wantType: &invalidRequestError{}, retryable: false},
		{status: 401, wantType: &authenticationError{}, retryable: false},
		{status: 403, wantType: &accessDeniedError{}, retryable: false},
		{status: 404, wantType: &notFoundError{}, retryable: false},
		{status: 408, wantType: &requestTimeoutError{}, retryable: true},
		{status: 413, wantType: &contextLengthError{}, retryable: false},
		{status: 422, wantType: &invalidRequestError{}, retryable: false},
		{status: 429, wantType: &rateLimitError{}, retryable: true},
		{status: 500, wantType: &serverError{}, retryable: true},
		{status: 503, wantType: &serverError{}, retryable: true},
		{status: 599, wantType: &unknownHTTPError{}, retryable: true},
	}
	for _, tc := range cases {
		err := ErrorFromHTTPStatus("p", tc.status, "msg", nil, nil)
		switch tc.wantType.(type) {
		case *invalidRequestError:
			var target *invalidRequestError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *authenticationError:
			var target *authenticationError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *accessDeniedError:
			var target *accessDeniedError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *notFoundError:
			var target *notFoundError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *requestTimeoutError:
			var target *requestTimeoutError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *contextLengthError:
			var target *contextLengthError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *rateLimitError:
			var target *rateLimitError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *serverError:
			var target *serverError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *unknownHTTPError:
			var target *unknownHTTPError
			if !errors.As(err, &target) {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		}
		var e Error
		ok := errors.As(err, &e)
		if !ok {
			t.Fatalf("status %d: not an llm.Error (%T)", tc.status, err)
		}
		if e.Retryable() != tc.retryable {
			t.Fatalf("status %d: retryable=%t want %t", tc.status, e.Retryable(), tc.retryable)
		}
	}
}

func TestContentFilterError_ImplementsErrorInterface(t *testing.T) {
	err := &contentFilterError{httpBaseError{provider: "test", statusCode: 400, message: "blocked", retryable: false}}
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
	err := &quotaExceededError{httpBaseError{provider: "test", statusCode: 429, message: "quota exceeded", retryable: false}}
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
	cause := errors.New("underlying network problem")
	err := &serverError{httpBaseError{
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
	cause := errors.New("context canceled")
	err := &AbortError{nonHTTPBaseError{
		message: "aborted",
		cause:   cause,
	}}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find cause through Unwrap on non-HTTP errors")
	}
}

func TestNonHTTPError_ErrorCode_Raw(t *testing.T) {
	err := &AbortError{nonHTTPBaseError{message: "aborted"}}
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
		want      ErrorKind
	}{
		{
			"invalid_prompt code",
			400,
			"some generic message",
			"invalid_prompt",
			KindContentFilter,
		},
		{
			"content_policy_violation code",
			400,
			"some generic message",
			"content_policy_violation",
			KindContentFilter,
		},
		{
			"unrecognized code falls through",
			400,
			"bad request",
			"some_other_code",
			KindInvalidRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"error": map[string]any{"code": tc.errorCode}}
			err := ErrorFromHTTPStatus("openai", tc.status, tc.message, raw, nil)
			if got := Kind(err); got != tc.want {
				t.Fatalf("ErrorFromHTTPStatus(%d, code=%q) kind = %v, want %v", tc.status, tc.errorCode, got, tc.want)
			}
		})
	}
}

func TestCyberPolicyViolation_403_IsRetryable(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"code": "cyber_policy_violation"}}
	err := ErrorFromHTTPStatus("openai", 403, "flagged by cyber policy", raw, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if !e.Retryable() {
		t.Fatal("cyber_policy_violation should be retryable (temporary account ban)")
	}
	if e.ErrorCode() != "cyber_policy_violation" {
		t.Fatalf("ErrorCode() = %q, want %q", e.ErrorCode(), "cyber_policy_violation")
	}
}

func TestCyberPolicyViolation_403_DefaultsRetryAfterTo60s(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"code": "cyber_policy_violation"}}
	err := ErrorFromHTTPStatus("openai", 403, "flagged", raw, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	ra := e.RetryAfter()
	if ra == nil {
		t.Fatal("RetryAfter() should default to 60s for cyber_policy_violation")
	}
	if *ra != 60*time.Second {
		t.Fatalf("RetryAfter() = %v, want 60s", *ra)
	}
}

func TestCyberPolicyViolation_403_RespectsServerRetryAfter(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"code": "cyber_policy_violation"}}
	d := 90 * time.Second
	err := ErrorFromHTTPStatus("openai", 403, "flagged", raw, &d)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	ra := e.RetryAfter()
	if ra == nil || *ra != 90*time.Second {
		t.Fatalf("RetryAfter() = %v, want 90s (server-provided)", ra)
	}
}

func TestRegular403_StillNonRetryable(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil)
	var e Error
	if !errors.As(err, &e) {
		t.Fatal("expected Error interface")
	}
	if e.Retryable() {
		t.Fatal("regular 403 should NOT be retryable")
	}
}

func TestConfigurationError_Unwrap_ExposesCause(t *testing.T) {
	cause := errors.New("missing API key")
	err := &ConfigurationError{Message: "bad config", Cause: cause}

	// Unwrap returns cause.
	if got := errors.Unwrap(err); !errors.Is(got, cause) {
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
	err := NewRequestTimeoutError("openai", `Post "https://api.openai.com/v1/responses": context deadline exceeded`, nil)
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

// --- BehaviorTag tests (PRI-1880) ---

func TestBehaviorTag_SetAndGet(t *testing.T) {
	err := ErrorFromHTTPStatus("openaicompat", 429, "rate limited", nil, nil)
	var bs behaviorTagSetter
	if !errors.As(err, &bs) {
		t.Fatalf("expected behaviorTagSetter, got %T", err)
	}
	bs.setBehaviorTag("openai")

	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("expected llm.Error, got %T", err)
	}
	if le.BehaviorTag() != "openai" {
		t.Fatalf("BehaviorTag() = %q, want %q", le.BehaviorTag(), "openai")
	}
}

func TestBehaviorTag_DefaultEmpty(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 500, "server error", nil, nil)
	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("expected llm.Error")
	}
	if le.BehaviorTag() != "" {
		t.Fatalf("BehaviorTag() = %q, want empty by default", le.BehaviorTag())
	}
}

func TestBehaviorTag_NonHTTPError(t *testing.T) {
	err := NewStreamError("work", "stream closed", nil)
	var bs behaviorTagSetter
	if !errors.As(err, &bs) {
		t.Fatalf("expected behaviorTagSetter on StreamError, got %T", err)
	}
	bs.setBehaviorTag("openai")

	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("expected llm.Error")
	}
	if le.BehaviorTag() != "openai" {
		t.Fatalf("BehaviorTag() = %q, want %q", le.BehaviorTag(), "openai")
	}
}

func TestBehaviorTag_EmptyNoOp(t *testing.T) {
	// Setting empty behavior tag is allowed (matches empty-value no-op spirit).
	err := ErrorFromHTTPStatus("openai", 500, "server error", nil, nil)
	var bs behaviorTagSetter
	if !errors.As(err, &bs) {
		t.Fatalf("expected behaviorTagSetter")
	}
	bs.setBehaviorTag("")
	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("expected llm.Error")
	}
	if le.BehaviorTag() != "" {
		t.Fatalf("BehaviorTag() = %q, want empty", le.BehaviorTag())
	}
}

func TestErrorFromHTTPStatus_MessageBasedClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		want    ErrorKind
	}{
		// Ambiguous 400 classified by message.
		{"400 content filter", 400, "content filter policy violated", KindContentFilter},
		{"400 safety", 400, "blocked by safety settings", KindContentFilter},
		{"400 context length", 400, "context length exceeded", KindContextLength},
		{"400 too many tokens", 400, "too many tokens in request", KindContextLength},
		{"400 quota", 400, "quota exceeded for billing account", KindQuotaExceeded},
		{"400 billing", 400, "billing issue on account", KindQuotaExceeded},
		{"400 not found", 400, "model does not exist", KindNotFound},
		{"400 plain", 400, "bad request", KindInvalidRequest},

		// Unambiguous status codes should NOT be overridden by message.
		{"401 always auth", 401, "content filter something", KindAuthentication},
		{"429 always rate", 429, "quota exceeded", KindRateLimit},
		{"404 always notfound", 404, "quota exceeded", KindNotFound},

		// 422 is ambiguous like 400.
		{"422 content filter", 422, "this violates safety policy", KindContentFilter},
		{"422 plain", 422, "invalid field", KindInvalidRequest},

		// OpenAI usage policy violation (invalid_prompt).
		{"400 usage policy", 400, "Your prompt was flagged as potentially violating our usage policy", KindContentFilter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrorFromHTTPStatus("p", tc.status, tc.message, nil, nil)
			if got := Kind(err); got != tc.want {
				t.Fatalf("ErrorFromHTTPStatus(%d, %q) kind = %v, want %v", tc.status, tc.message, got, tc.want)
			}
		})
	}
}
