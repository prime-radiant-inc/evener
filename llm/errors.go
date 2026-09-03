package llm

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/llm/registry"
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

// ConfigurationError reports a configuration problem (e.g. invalid or missing
// setup) and carries an optional underlying Cause. It satisfies the Error
// interface with empty provider, status code, and error code, and is never
// retryable.
type ConfigurationError struct {
	Message string
	Cause   error
}

// ContextBudgetError reports a request that Evener's local token admission
// calculation proved cannot fit a known model limit. It is local and never
// retryable; no provider request was made.
type ContextBudgetError struct {
	Provider     string
	Model        string
	Limit        string
	InputTokens  int
	OutputTokens int
	Maximum      int
}

func newContextBudgetError(req Request, res registry.Resolved, limit string, input, output, maximum int) *ContextBudgetError {
	provider, model := ResolveContextBudgetIdentity(req, res)
	return &ContextBudgetError{
		Provider: provider, Model: model, Limit: limit,
		InputTokens: input, OutputTokens: output, Maximum: maximum,
	}
}

// ResolveContextBudgetIdentity returns the provider instance and requested
// model used to attribute a local context-budget failure.
func ResolveContextBudgetIdentity(req Request, res registry.Resolved) (provider, model string) {
	provider = strings.TrimSpace(res.Instance)
	if provider == "" {
		provider = strings.TrimSpace(req.Provider)
	}
	model = strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(res.ModelID)
	}
	return provider, model
}

// Error states that the request was blocked locally before provider dispatch.
func (e *ContextBudgetError) Error() string {
	return fmt.Sprintf("Evener blocked %s/%s before provider dispatch: token budget exceeds %s (input=%d output=%d maximum=%d)",
		e.Provider, e.Model, e.Limit, e.InputTokens, e.OutputTokens, e.Maximum)
}

// Retryable returns false because retrying an unchanged request cannot fit it.
func (*ContextBudgetError) Retryable() bool { return false }

func (*ContextBudgetError) declaredKind() ErrorKind { return KindContextLength }

// Error returns the configuration error message, prefixed with
// "configuration error: " and with surrounding whitespace trimmed.
func (e *ConfigurationError) Error() string {
	return "configuration error: " + strings.TrimSpace(e.Message)
}

// Provider returns the empty string; configuration errors are not attributed
// to a provider.
func (e *ConfigurationError) Provider() string { return "" }

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
	provider string
	protocol string
	hint     string
	// rejectedParam is the request parameter a provider rejection named, from
	// the structured error.param or a spec §12 message pattern: "temperature"
	// for a rejected temperature, "" when the rejection named nothing.
	rejectedParam string
	statusCode    int
	message       string
	errorCode     string
	retryable     bool
	retryAfter    *time.Duration
	rawResponse   any
	cause         error
}

// Error returns the error message in the form "<provider> error (status=<code>): <message>",
// falling back to "request failed" when the message is empty, followed by
// " (hint: <hint>)" when the classifier attached one (spec §12).
func (e *httpBaseError) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	s := fmt.Sprintf("%s error (status=%d): %s", e.provider, e.statusCode, msg)
	if e.hint != "" {
		s += " (hint: " + e.hint + ")"
	}
	return s
}

// Provider returns the provider that produced the error.
func (e *httpBaseError) Provider() string { return e.provider }

// withProvider returns a copy of the base attributed to name, unwrapping to
// the original error it was copied from. The value receiver is the copy;
// original keeps errors.Is(copy, original) true, so a caller holding the
// error a scripted adapter or a failed turn produced still recognizes the
// re-attributed one, and the original's own cause stays reachable through it.
func (e httpBaseError) withProvider(name string, original error) httpBaseError {
	e.provider = strings.TrimSpace(name)
	e.cause = original
	return e
}

// Protocol returns the protocol id stamped by ClassifyHTTPError, or "".
func (e *httpBaseError) Protocol() string { return e.protocol }

// Hint returns the configuration hint attached by ClassifyHTTPError, or "".
func (e *httpBaseError) Hint() string { return e.hint }

// StatusCode returns the HTTP status code that produced the error.
func (e *httpBaseError) StatusCode() int { return e.statusCode }

// ErrorCode returns the provider-specific error code extracted from the response
// body, or the empty string if none was found.
func (e *httpBaseError) ErrorCode() string { return e.errorCode }

// RejectedParameter returns the request parameter a rejected-request error
// named, in the provider's own spelling ("temperature",
// "max_completion_tokens", …), or "" when the error named no parameter or is
// not a typed provider rejection. It reads the structured error.param and the
// message shapes ClassifyHTTPError recognizes, so callers never re-match
// provider prose.
func (e *httpBaseError) RejectedParameter() string { return e.rejectedParam }

// RejectedParameter returns the request parameter a rejected-request error
// named, in the provider's own spelling, or "" when the error named no
// parameter or is not a typed provider rejection. It reads the structured
// error.param and the message shapes ClassifyHTTPError and
// ErrorFromHTTPStatus recognize, so callers never re-match provider prose
// (the namer's temperature retry is the motivating caller).
func RejectedParameter(err error) string {
	var p interface{ RejectedParameter() string }
	if errors.As(err, &p) {
		return p.RejectedParameter()
	}
	return ""
}

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

// responseHeaderTimeoutError reports that a fully written request timed out
// before response headers arrived. It is retryable so a bounded stuck attempt
// advances through the configured retry policy. Provider work may still
// complete, so the duplicate-generation risk is accepted by the approved
// design. Its category is [KindTimeout].
type responseHeaderTimeoutError struct{ httpBaseError }

// contextLengthError reports that the request exceeded the model's context
// window, typically from an HTTP 413 status or a "context length" message. It
// is not retryable. Its category is [KindContextLength].
type contextLengthError struct{ httpBaseError }

// contentFilterError reports that the request or response was blocked by a
// content filter or safety/usage policy. It is not retryable. Its category is [KindContentFilter].
type contentFilterError struct{ httpBaseError }

// quotaExceededError reports that a quota or billing limit was exceeded,
// detected from a "quota" or "billing" message, or from a usage-limit error
// code on an HTTP 429. It is not retryable. Its category is [KindQuotaExceeded].
//
// usageLimitResetsAt is when the exhausted allowance returns, for the 429
// usage-limit form that names one; it is the zero time otherwise. Read it
// through [UsageLimitResetAt].
type quotaExceededError struct {
	httpBaseError
	usageLimitResetsAt time.Time
}

// rateLimitError reports that a rate limit was hit, typically from an HTTP 429
// status. It is retryable. Its category is [KindRateLimit].
type rateLimitError struct{ httpBaseError }

// serverError reports a server-side failure, typically from an HTTP 500, 502,
// 503, or 504 status. It is retryable. Its category is [KindServer].
type serverError struct{ httpBaseError }

// unknownHTTPError reports an HTTP failure with a status code that does not map
// to a more specific error type. It defaults to retryable. Its category is [KindUnknown].
type unknownHTTPError struct{ httpBaseError }

// copyWithProvider implementations. Each returns a new error of its own
// concrete type so errors.As finds the copy, never the original with a stale
// provider. One line apiece because the base carries every field;
// TestEveryProviderAttributedErrorCopies fails if a type is added without one.

func (e *invalidRequestError) copyWithProvider(name string) error {
	return &invalidRequestError{e.withProvider(name, e)}
}

func (e *authenticationError) copyWithProvider(name string) error {
	return &authenticationError{e.withProvider(name, e)}
}

func (e *accessDeniedError) copyWithProvider(name string) error {
	return &accessDeniedError{e.withProvider(name, e)}
}

func (e *notFoundError) copyWithProvider(name string) error {
	return &notFoundError{e.withProvider(name, e)}
}

func (e *requestTimeoutError) copyWithProvider(name string) error {
	return &requestTimeoutError{e.withProvider(name, e)}
}

func (e *responseHeaderTimeoutError) copyWithProvider(name string) error {
	return &responseHeaderTimeoutError{e.withProvider(name, e)}
}

func (e *contextLengthError) copyWithProvider(name string) error {
	return &contextLengthError{e.withProvider(name, e)}
}

func (e *contentFilterError) copyWithProvider(name string) error {
	return &contentFilterError{e.withProvider(name, e)}
}

func (e *rateLimitError) copyWithProvider(name string) error {
	return &rateLimitError{e.withProvider(name, e)}
}

func (e *serverError) copyWithProvider(name string) error {
	return &serverError{e.withProvider(name, e)}
}

func (e *unknownHTTPError) copyWithProvider(name string) error {
	return &unknownHTTPError{e.withProvider(name, e)}
}

func (e *quotaExceededError) copyWithProvider(name string) error {
	return &quotaExceededError{httpBaseError: e.withProvider(name, e), usageLimitResetsAt: e.usageLimitResetsAt}
}

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

// providerCopier is implemented by errors that can return a COPY of
// themselves attributed to another provider. The protocol packages use it so
// an error carries the INSTANCE that produced it rather than the wire
// vocabulary its classifier happened to stamp — an in-band Responses failure
// classified as "openai" becomes the caller's own instance name (spec §7.5).
//
// It copies rather than restamping in place because an error is a shared
// value: it is wrapped, joined, stored on a turn record, and re-served by
// scripted adapters. Two dispatches on one client — a session's own turn and
// its namer goroutine — held the same instance and raced, one reading the
// provider while the other wrote it.
type providerCopier interface {
	error
	Provider() string
	copyWithProvider(string) error
}

// RewriteErrorProvider returns err attributed to provider: a copy of the same
// concrete type, leaving the error it was given untouched. Returns err itself
// when it carries no provider attribution to rewrite. Safe to call on nil.
// Thin wrappers should call this on every error they forward so failures
// aren't misattributed to the inner adapter.
//
// Errors whose Provider() is empty are left alone. This protects errors that
// intentionally have no provider attribution — most importantly AbortError
// (user-driven cancellation) and NoObjectGeneratedError (response-shape
// failure that isn't provider-specific). Restamping these would change
// "context canceled" into "ollama error: context canceled", which is wrong.
//
// Only the error itself is rewritten, not one buried in a foreign wrapper:
// a wrapper this package does not own cannot be rebuilt around the copy, and
// reaching in to restamp the shared inner value is the mutation this exists
// to avoid. Nothing wraps before a rewrite point today — the one wrapper in
// the stream path (transport.FatalStreamError) is unwrapped by the runner
// before the terminal error is emitted.
func RewriteErrorProvider(err error, provider string) error {
	if err == nil {
		return nil
	}
	//nolint:errorlint // deliberate: only the error itself is rewritten, never one inside a wrapper this package cannot rebuild (see above)
	pc, ok := err.(providerCopier)
	if !ok || pc.Provider() == "" {
		return err
	}
	return pc.copyWithProvider(provider)
}

// ErrorFromHTTPStatus maps an HTTP status code to the corresponding Error
// implementation, populating it with the provider, message, raw response, and
// retry-after delay, and an error code extracted from raw. The returned error's
// retryable flag and concrete type are determined by the status code; 400/422
// responses are further refined by message via classifyByMessage. Unrecognized
// status codes yield a retryable UnknownHTTPError.
func ErrorFromHTTPStatus(provider string, statusCode int, message string, raw any, retryAfter *time.Duration) error {
	code := extractErrorCode(raw)
	base := httpBaseError{
		provider:      strings.TrimSpace(provider),
		statusCode:    statusCode,
		message:       message,
		rejectedParam: rejectedParameter(raw, message, code),
		errorCode:     code,
		retryAfter:    retryAfter,
		rawResponse:   raw,
	}
	return errorFromHTTPStatus(base)
}

func errorFromHTTPStatus(base httpBaseError) error {
	switch base.statusCode {
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
			return &accessDeniedError{base}
		}
		// Some providers (e.g. Kimi's Anthropic-compatible API) report a
		// billing-cycle allowance exhaustion as a 403 rather than a 429, with
		// no distinguishing error code — only the body's message names it.
		// Without this check the failure surfaces as generic access-denied,
		// and an orchestrator keeps re-dispatching delegate waves into a spent
		// quota instead of stopping.
		now := time.Now()
		if limit, ok := parseUsageLimit(base.rawResponse, now); ok {
			base.message = usageLimitMessage(limit, now)
			return &quotaExceededError{httpBaseError: base, usageLimitResetsAt: limit.resetsAt}
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
		// A 429 covers two unrelated conditions. "Slow down" is transient and
		// worth the full retry budget; "your allowance is spent" can be hours
		// or days from clearing, so retrying it just burns the budget and
		// delays the error the user needs to see.
		now := time.Now()
		if limit, ok := parseUsageLimit(base.rawResponse, now); ok {
			base.retryable = false
			base.message = usageLimitMessage(limit, now)
			return &quotaExceededError{httpBaseError: base, usageLimitResetsAt: limit.resetsAt}
		}
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
	case strings.Contains(lower, "context length") || strings.Contains(lower, "too many tokens") ||
		strings.Contains(lower, "maximum context") || strings.Contains(lower, "reduce the length") ||
		strings.Contains(lower, "prompt is too long"):
		return &contextLengthError{base}
	case strings.Contains(lower, "quota") || strings.Contains(lower, "billing"):
		return &quotaExceededError{httpBaseError: base}
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

func newResponseHeaderTimeoutError(provider string, message string, cause error) error {
	base := httpBaseError{
		provider:   strings.TrimSpace(provider),
		statusCode: 0,
		message:    message,
		retryable:  true,
		cause:      cause,
	}
	return &responseHeaderTimeoutError{base}
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
		// A server (or a hostile proxy) can send an absurd delay-seconds value
		// that overflows int64 nanoseconds and wraps to a negative Duration.
		// Saturate instead so the result is always a non-negative wait; callers
		// clamp it to their own MaxDelay.
		const maxSecs = int64(math.MaxInt64) / int64(time.Second)
		if int64(secs) > maxSecs {
			d := time.Duration(math.MaxInt64)
			return &d
		}
		d := time.Duration(secs) * time.Second
		return &d
	}
	if t, err := http.ParseTime(v); err == nil {
		d := max(t.Sub(now), 0)
		return &d
	}
	return nil
}
