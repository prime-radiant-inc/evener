package llm

import (
	"fmt"
	"strings"
	"time"
)

type nonHTTPBaseError struct {
	provider    string
	behaviorTag string
	message     string
	retryable   bool
	retryAfter  *time.Duration
	cause       error

	rawRequestBody  string
	rawResponseBody string
}

// Error returns the error message. It is prefixed with "<provider> error: "
// when a provider is set, and falls back to "request failed" when the message
// is empty.
func (e *nonHTTPBaseError) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	if strings.TrimSpace(e.provider) == "" {
		return msg
	}
	return fmt.Sprintf("%s error: %s", e.provider, msg)
}

// Provider returns the provider this error is attributed to, or the empty
// string when it has no provider attribution (e.g. user cancellation).
func (e *nonHTTPBaseError) Provider() string        { return e.provider }
func (e *nonHTTPBaseError) setProvider(name string) { e.provider = strings.TrimSpace(name) }

// BehaviorTag returns the provider behavior tag (provider type) stamped onto
// the error, or the empty string if none was set.
func (e *nonHTTPBaseError) BehaviorTag() string       { return e.behaviorTag }
func (e *nonHTTPBaseError) setBehaviorTag(tag string) { e.behaviorTag = strings.TrimSpace(tag) }

// StatusCode returns 0; these errors are not HTTP failures.
func (e *nonHTTPBaseError) StatusCode() int { return 0 }

// ErrorCode returns the empty string; non-HTTP errors carry no provider error code.
func (e *nonHTTPBaseError) ErrorCode() string { return "" }

// Retryable reports whether retrying the request might succeed.
func (e *nonHTTPBaseError) Retryable() bool { return e.retryable }

// RetryAfter returns the suggested delay before retrying, or nil if none applies.
func (e *nonHTTPBaseError) RetryAfter() *time.Duration { return e.retryAfter }

// Raw returns nil; non-HTTP errors have no raw provider response.
func (e *nonHTTPBaseError) Raw() any { return nil }

// RawHTTPBodies returns raw request/response bodies captured around non-HTTP
// provider failures, when available.
func (e *nonHTTPBaseError) RawHTTPBodies() (requestBody, responseBody string) {
	return e.rawRequestBody, e.rawResponseBody
}

// Unwrap returns the underlying cause, exposing it to errors.Is/errors.As.
func (e *nonHTTPBaseError) Unwrap() error { return e.cause }

// AbortError is a non-HTTP error reporting a user-initiated cancellation.
type AbortError struct{ nonHTTPBaseError }

// StreamError is a non-HTTP error reporting a streaming failure.
type StreamError struct{ nonHTTPBaseError }

// InvalidToolCallError is a non-HTTP error reporting an invalid tool call.
type InvalidToolCallError struct{ nonHTTPBaseError }

// NoObjectGeneratedError is a non-HTTP error reporting that no valid object
// could be produced from the model output. RawText holds the model output text
// that could not be parsed or validated.
type NoObjectGeneratedError struct {
	nonHTTPBaseError
	RawText string
}

// UnsupportedToolChoiceError is a non-HTTP error reporting that the requested
// tool_choice mode is not supported.
type UnsupportedToolChoiceError struct{ nonHTTPBaseError }

// NewAbortError reports a user-initiated cancellation. cause is the underlying
// error (typically context.Canceled); it is exposed via Unwrap so errors.Is(err,
// context.Canceled) holds. Pass nil when there is no underlying cause.
func NewAbortError(message string, cause error) error {
	return &AbortError{nonHTTPBaseError{message: message, retryable: false, cause: cause}}
}

// NewStreamError reports a streaming failure. cause is the underlying read/parse
// error (e.g. an SSE decode failure or a wrapped context error) and is exposed
// via Unwrap; pass nil for a content-level stream sentinel with no cause.
func NewStreamError(provider, message string, cause error) error {
	return NewStreamErrorWithRawBodies(provider, message, cause, "", "")
}

// NewStreamErrorWithRawBodies reports a streaming failure and carries raw HTTP
// bodies captured before the stream failed, when raw logging is enabled.
func NewStreamErrorWithRawBodies(provider, message string, cause error, requestBody, responseBody string) error {
	return &StreamError{nonHTTPBaseError{
		provider:        provider,
		message:         message,
		retryable:       true,
		cause:           cause,
		rawRequestBody:  requestBody,
		rawResponseBody: responseBody,
	}}
}

// NewNoObjectGeneratedError reports that no valid object could be produced from
// the model output. cause is the underlying parse/validation error, exposed via
// Unwrap; pass nil when none applies.
func NewNoObjectGeneratedError(message string, rawText string, cause error) error {
	return &NoObjectGeneratedError{nonHTTPBaseError: nonHTTPBaseError{message: message, retryable: false, cause: cause}, RawText: rawText}
}

// NewUnsupportedToolChoiceError reports that the given tool_choice mode is not
// supported by provider. The error is not retryable.
func NewUnsupportedToolChoiceError(provider, mode string) error {
	msg := strings.TrimSpace(mode)
	if msg == "" {
		msg = "(empty)"
	}
	return &UnsupportedToolChoiceError{nonHTTPBaseError{provider: provider, message: "unsupported tool_choice mode: " + msg, retryable: false}}
}
