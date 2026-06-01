package llm

import (
	"errors"
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
	BehaviorTag() string
	StatusCode() int
	ErrorCode() string
	Retryable() bool
	RetryAfter() *time.Duration
	Raw() any
}

// ConfigurationError reports a configuration problem (e.g. invalid or missing
// setup) and carries an optional underlying Cause. It satisfies the Error
// interface with empty provider, behavior tag, status code, and error code,
// and is never retryable.
type ConfigurationError struct {
	Message string
	Cause   error
}

// Error returns the configuration error message, prefixed with
// "configuration error: " and with surrounding whitespace trimmed.
func (e *ConfigurationError) Error() string {
	return "configuration error: " + strings.TrimSpace(e.Message)
}

// Provider returns the empty string; configuration errors are not attributed
// to a provider.
func (e *ConfigurationError) Provider() string { return "" }

// BehaviorTag returns the empty string; configuration errors carry no behavior tag.
func (e *ConfigurationError) BehaviorTag() string { return "" }

// StatusCode returns 0; configuration errors have no HTTP status.
func (e *ConfigurationError) StatusCode() int { return 0 }

// ErrorCode returns the empty string; configuration errors carry no error code.
func (e *ConfigurationError) ErrorCode() string { return "" }

// Retryable returns false; configuration errors are not retryable.
func (e *ConfigurationError) Retryable() bool { return false }

// RetryAfter returns nil; configuration errors have no retry delay.
func (e *ConfigurationError) RetryAfter() *time.Duration { return nil }

// Raw returns nil; configuration errors have no raw response.
func (e *ConfigurationError) Raw() any { return nil }

// Unwrap returns the underlying Cause, if any.
func (e *ConfigurationError) Unwrap() error { return e.Cause }

type httpBaseError struct {
	provider    string
	behaviorTag string
	statusCode  int
	message     string
	errorCode   string
	retryable   bool
	retryAfter  *time.Duration
	rawResponse any
	cause       error
}

// Error returns the error message in the form "<provider> error (status=<code>): <message>",
// falling back to "request failed" when the message is empty.
func (e *httpBaseError) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	return fmt.Sprintf("%s error (status=%d): %s", e.provider, e.statusCode, msg)
}

// Provider returns the provider that produced the error.
func (e *httpBaseError) Provider() string        { return e.provider }
func (e *httpBaseError) setProvider(name string) { e.provider = strings.TrimSpace(name) }

// BehaviorTag returns the provider behavior tag (provider type) stamped onto
// the error, or the empty string if none was set.
func (e *httpBaseError) BehaviorTag() string       { return e.behaviorTag }
func (e *httpBaseError) setBehaviorTag(tag string) { e.behaviorTag = strings.TrimSpace(tag) }

// StatusCode returns the HTTP status code that produced the error.
func (e *httpBaseError) StatusCode() int { return e.statusCode }

// ErrorCode returns the provider-specific error code extracted from the response
// body, or the empty string if none was found.
func (e *httpBaseError) ErrorCode() string { return e.errorCode }

// Retryable reports whether retrying the request might succeed.
func (e *httpBaseError) Retryable() bool { return e.retryable }

// RetryAfter returns the suggested delay before retrying, typically parsed from
// the provider's Retry-After header, or nil if none applies.
func (e *httpBaseError) RetryAfter() *time.Duration { return e.retryAfter }

// Raw returns the decoded raw provider response body, or nil if unavailable.
func (e *httpBaseError) Raw() any { return e.rawResponse }

// Unwrap returns the underlying cause, exposing it to errors.Is/errors.As.
func (e *httpBaseError) Unwrap() error { return e.cause }

// invalidRequestError reports a malformed or rejected request, typically from
// an HTTP 400 or 422 status. It is not retryable. Its category is [KindInvalidRequest].
type invalidRequestError struct{ httpBaseError }

// authenticationError reports failed authentication, typically from an HTTP 401
// status. It is not retryable. Its category is [KindAuthentication].
type authenticationError struct{ httpBaseError }

// accessDeniedError reports a forbidden request, typically from an HTTP 403
// status. It is not retryable, except for transient provider bans (e.g. OpenAI
// cyber_policy_violation) which are marked retryable. Its category is [KindAccessDenied].
type accessDeniedError struct{ httpBaseError }

// notFoundError reports a missing resource, typically from an HTTP 404 status
// or a "not found" message. It is not retryable. Its category is [KindNotFound].
type notFoundError struct{ httpBaseError }

// requestTimeoutError reports a request timeout, from an HTTP 408 status or a
// non-HTTP deadline. It is retryable. Its category is [KindTimeout].
type requestTimeoutError struct{ httpBaseError }

// contextLengthError reports that the request exceeded the model's context
// window, typically from an HTTP 413 status or a "context length" message. It
// is not retryable. Its category is [KindContextLength].
type contextLengthError struct{ httpBaseError }

// contentFilterError reports that the request or response was blocked by a
// content filter or safety/usage policy. It is not retryable. Its category is [KindContentFilter].
type contentFilterError struct{ httpBaseError }

// quotaExceededError reports that a quota or billing limit was exceeded,
// detected from a "quota" or "billing" message. It is not retryable. Its category is [KindQuotaExceeded].
type quotaExceededError struct{ httpBaseError }

// rateLimitError reports that a rate limit was hit, typically from an HTTP 429
// status. It is retryable. Its category is [KindRateLimit].
type rateLimitError struct{ httpBaseError }

// serverError reports a server-side failure, typically from an HTTP 500, 502,
// 503, or 504 status. It is retryable. Its category is [KindServer].
type serverError struct{ httpBaseError }

// unknownHTTPError reports an HTTP failure with a status code that does not map
// to a more specific error type. It defaults to retryable. Its category is [KindUnknown].
type unknownHTTPError struct{ httpBaseError }

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

// providerSetter is implemented by errors whose provider name can be
// rewritten in place. Used by thin provider wrappers (e.g. the ollama
// adapter, which delegates to openaicompat) so errors carry the wrapper's
// own provider stamp instead of the inner adapter's.
type providerSetter interface {
	setProvider(string)
}

// behaviorTagSetter is implemented by errors whose behavior tag can be
// stamped in place. Used by Client to record the behavior tag (e.g. "openai")
// associated with the provider instance that returned the error, so classifiers
// can key on behavior type rather than instance name.
type behaviorTagSetter interface {
	setBehaviorTag(string)
}

// RewriteErrorProvider rewrites err's provider name in place if the error
// (or any error in its Unwrap chain) supports it. Returns err unchanged
// otherwise. Safe to call on nil. Thin wrappers should call this on every
// error they forward so failures aren't misattributed to the inner adapter.
//
// Errors whose original Provider() is empty are left alone. This protects
// errors that intentionally have no provider attribution — most importantly
// AbortError (user-driven cancellation) and NoObjectGeneratedError
// (response-shape failure that isn't provider-specific). Restamping these
// would change "context canceled" into "ollama error: context canceled",
// which is wrong.
func RewriteErrorProvider(err error, provider string) error {
	if err == nil {
		return nil
	}
	var ps providerSetter
	if !errors.As(err, &ps) {
		return err
	}
	if getter, ok := ps.(interface{ Provider() string }); ok && getter.Provider() == "" {
		return err
	}
	ps.setProvider(provider)
	return err
}

// StampErrorBehaviorTag stamps the behavior tag onto err in place if the error
// (or any error in its Unwrap chain) supports it. Returns err unchanged
// otherwise. Safe to call on nil. Only stamps errors that already have a
// non-empty Provider() — same no-op guard as RewriteErrorProvider.
func StampErrorBehaviorTag(err error, tag string) error {
	if err == nil || strings.TrimSpace(tag) == "" {
		return err
	}
	var bs behaviorTagSetter
	if !errors.As(err, &bs) {
		return err
	}
	if getter, ok := bs.(interface{ Provider() string }); ok && getter.Provider() == "" {
		return err
	}
	bs.setBehaviorTag(tag)
	return err
}

// ErrorFromHTTPStatus maps an HTTP status code to the corresponding Error
// implementation, populating it with the provider, message, raw response, and
// retry-after delay, and an error code extracted from raw. The returned error's
// retryable flag and concrete type are determined by the status code; 400/422
// responses are further refined by message via classifyByMessage. Unrecognized
// status codes yield a retryable UnknownHTTPError.
func ErrorFromHTTPStatus(provider string, statusCode int, message string, raw any, retryAfter *time.Duration) error {
	base := httpBaseError{
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
		return &invalidRequestError{base}
	case 401:
		base.retryable = false
		return &authenticationError{base}
	case 403:
		base.retryable = false
		// OpenAI cyber_policy_violation is a temporary account-level ban that
		// clears after a few minutes. Retry with backoff instead of dying.
		if base.errorCode == "cyber_policy_violation" {
			base.retryable = true
			if base.retryAfter == nil {
				d := 60 * time.Second
				base.retryAfter = &d
			}
		}
		return &accessDeniedError{base}
	case 404:
		base.retryable = false
		return &notFoundError{base}
	case 408:
		base.retryable = true
		return &requestTimeoutError{base}
	case 413:
		base.retryable = false
		return &contextLengthError{base}
	case 429:
		base.retryable = true
		return &rateLimitError{base}
	case 500, 502, 503, 504:
		base.retryable = true
		return &serverError{base}
	default:
		// Spec: unknown errors default to retryable.
		base.retryable = true
		return &unknownHTTPError{base}
	}
}

// classifyByMessage checks the error message for classification signals when
// the HTTP status code is ambiguous (e.g., 400/422). Returns nil if no match.
func classifyByMessage(base httpBaseError) error {
	// Check error code first — more reliable than message parsing.
	switch base.errorCode {
	case "invalid_prompt", "content_policy_violation":
		return &contentFilterError{base}
	}

	lower := strings.ToLower(base.message)
	switch {
	case strings.Contains(lower, "content filter") || strings.Contains(lower, "safety") ||
		strings.Contains(lower, "usage policy"):
		return &contentFilterError{base}
	case strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens"):
		return &contextLengthError{base}
	case strings.Contains(lower, "quota") || strings.Contains(lower, "billing"):
		return &quotaExceededError{base}
	case strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"):
		return &notFoundError{base}
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid key"):
		return &authenticationError{base}
	}
	return nil
}

// NewRequestTimeoutError constructs a non-HTTP timeout error (e.g., context deadline
// exceeded) that matches the unified error hierarchy. HTTP-level timeouts are retried
// because the server may not have received the request. User-initiated cancellation
// (context.Canceled) goes through NewAbortError instead and is not retried.
//
// cause is the underlying error (typically context.DeadlineExceeded), exposed via
// Unwrap so errors.Is(err, context.DeadlineExceeded) holds. Pass nil when there is
// no underlying cause (e.g. an HTTP 408 synthesized from a status code).
func NewRequestTimeoutError(provider string, message string, cause error) error {
	base := httpBaseError{
		provider:   strings.TrimSpace(provider),
		statusCode: 0,
		message:    message,
		retryable:  true,
		cause:      cause,
	}
	return &requestTimeoutError{base}
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
