package llm

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Error is the unified error interface returned by provider adapters and the client.
type Error interface {
	error
	Provider() string
	StatusCode() int
	ErrorCode() string
	Retryable() bool
	RetryAfter() *time.Duration
	Raw() any
}

type ConfigurationError struct {
	Message string
	Cause   error
}

func (e *ConfigurationError) Error() string {
	return "configuration error: " + strings.TrimSpace(e.Message)
}
func (e *ConfigurationError) Provider() string           { return "" }
func (e *ConfigurationError) StatusCode() int            { return 0 }
func (e *ConfigurationError) ErrorCode() string          { return "" }
func (e *ConfigurationError) Retryable() bool            { return false }
func (e *ConfigurationError) RetryAfter() *time.Duration { return nil }
func (e *ConfigurationError) Raw() any                   { return nil }
func (e *ConfigurationError) Unwrap() error              { return e.Cause }

type httpErrorBase struct {
	provider    string
	statusCode  int
	message     string
	errorCode   string
	retryable   bool
	retryAfter  *time.Duration
	rawResponse any
	cause       error
}

func (e *httpErrorBase) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	return fmt.Sprintf("%s error (status=%d): %s", e.provider, e.statusCode, msg)
}
func (e *httpErrorBase) Provider() string           { return e.provider }
func (e *httpErrorBase) StatusCode() int            { return e.statusCode }
func (e *httpErrorBase) ErrorCode() string          { return e.errorCode }
func (e *httpErrorBase) Retryable() bool            { return e.retryable }
func (e *httpErrorBase) RetryAfter() *time.Duration { return e.retryAfter }
func (e *httpErrorBase) Raw() any                   { return e.rawResponse }
func (e *httpErrorBase) Unwrap() error              { return e.cause }

type InvalidRequestError struct{ httpErrorBase }
type AuthenticationError struct{ httpErrorBase }
type AccessDeniedError struct{ httpErrorBase }
type NotFoundError struct{ httpErrorBase }
type RequestTimeoutError struct{ httpErrorBase }
type ContextLengthError struct{ httpErrorBase }
type ContentFilterError struct{ httpErrorBase }
type QuotaExceededError struct{ httpErrorBase }
type RateLimitError struct{ httpErrorBase }
type ServerError struct{ httpErrorBase }
type UnknownHTTPError struct{ httpErrorBase }

// extractErrorCode attempts to find an error code from a raw API response body.
// Supports OpenAI ({"error":{"code":"..."}}) and Anthropic ({"error":{"type":"..."}}) formats.
func extractErrorCode(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if errObj, ok := m["error"].(map[string]any); ok {
		if code, _ := errObj["code"].(string); code != "" {
			return code
		}
		if typ, _ := errObj["type"].(string); typ != "" {
			return typ
		}
	}
	return ""
}

func ErrorFromHTTPStatus(provider string, statusCode int, message string, raw any, retryAfter *time.Duration) error {
	base := httpErrorBase{
		provider:    strings.TrimSpace(provider),
		statusCode:  statusCode,
		message:     message,
		errorCode:   extractErrorCode(raw),
		retryAfter:  retryAfter,
		rawResponse: raw,
	}
	switch statusCode {
	case 400, 422:
		// Ambiguous status codes: check message for more specific classification.
		base.retryable = false
		if err := classifyByMessage(base); err != nil {
			return err
		}
		return &InvalidRequestError{base}
	case 401:
		base.retryable = false
		return &AuthenticationError{base}
	case 403:
		base.retryable = false
		return &AccessDeniedError{base}
	case 404:
		base.retryable = false
		return &NotFoundError{base}
	case 408:
		base.retryable = true
		return &RequestTimeoutError{base}
	case 413:
		base.retryable = false
		return &ContextLengthError{base}
	case 429:
		base.retryable = true
		return &RateLimitError{base}
	case 500, 502, 503, 504:
		base.retryable = true
		return &ServerError{base}
	default:
		// Spec: unknown errors default to retryable.
		base.retryable = true
		return &UnknownHTTPError{base}
	}
}

// classifyByMessage checks the error message for classification signals when
// the HTTP status code is ambiguous (e.g., 400/422). Returns nil if no match.
func classifyByMessage(base httpErrorBase) error {
	lower := strings.ToLower(base.message)
	switch {
	case strings.Contains(lower, "content filter") || strings.Contains(lower, "safety"):
		return &ContentFilterError{base}
	case strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens"):
		return &ContextLengthError{base}
	case strings.Contains(lower, "quota") || strings.Contains(lower, "billing"):
		return &QuotaExceededError{base}
	case strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"):
		return &NotFoundError{base}
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid key"):
		return &AuthenticationError{base}
	}
	return nil
}

// NewRequestTimeoutError constructs a non-HTTP timeout error (e.g., context deadline
// exceeded) that matches the unified error hierarchy. HTTP-level timeouts are retried
// because the server may not have received the request. User-initiated cancellation
// (context.Canceled) goes through NewAbortError instead and is not retried.
func NewRequestTimeoutError(provider string, message string) error {
	base := httpErrorBase{
		provider:   strings.TrimSpace(provider),
		statusCode: 0,
		message:    message,
		retryable:  true,
	}
	return &RequestTimeoutError{base}
}

// ParseRetryAfter parses the Retry-After header value.
// Supported forms:
// - integer seconds
// - HTTP-date (RFC 7231)
func ParseRetryAfter(v string, now time.Time) *time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		d := time.Duration(secs) * time.Second
		return &d
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}
