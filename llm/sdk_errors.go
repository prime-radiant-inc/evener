package llm

import (
	"fmt"
	"strings"
	"time"
)

type nonHTTPBaseError struct {
	provider   string
	message    string
	retryable  bool
	retryAfter *time.Duration
	cause      error
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
func (e *nonHTTPBaseError) Provider() string { return e.provider }

// withProvider returns a copy of the base attributed to name, unwrapping to
// original; see httpBaseError.withProvider for why the rewrite copies and
// why the copy wraps.
func (e nonHTTPBaseError) withProvider(name string, original error) nonHTTPBaseError {
	e.provider = strings.TrimSpace(name)
	e.cause = original
	return e
}

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

// UnsupportedEndpointError is a non-HTTP error reporting that an endpoint
// accepted the request but served nothing the protocol recognizes: the model
// does not speak it. It is [KindNotFound] — the model is not there on this
// endpoint — and never retryable, so the retry chain short-circuits and the
// caller routes to its next model instead of re-POSTing a request that cannot
// succeed.
type UnsupportedEndpointError struct{ nonHTTPBaseError }

// copyWithProvider implementations; see the http ones in errors.go.

func (e *AbortError) copyWithProvider(name string) error {
	return &AbortError{e.withProvider(name, e)}
}

func (e *StreamError) copyWithProvider(name string) error {
	return &StreamError{e.withProvider(name, e)}
}

func (e *InvalidToolCallError) copyWithProvider(name string) error {
	return &InvalidToolCallError{e.withProvider(name, e)}
}

func (e *UnsupportedToolChoiceError) copyWithProvider(name string) error {
	return &UnsupportedToolChoiceError{e.withProvider(name, e)}
}

func (e *UnsupportedEndpointError) copyWithProvider(name string) error {
	return &UnsupportedEndpointError{e.withProvider(name, e)}
}

func (e *NoObjectGeneratedError) copyWithProvider(name string) error {
	return &NoObjectGeneratedError{nonHTTPBaseError: e.withProvider(name, e), RawText: e.RawText}
}

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
	return &StreamError{nonHTTPBaseError{
		provider:  provider,
		message:   message,
		retryable: true,
		cause:     cause,
	}}
}

// NewUnsupportedEndpointError reports that provider's endpoint cannot serve
// the request over this protocol. cause is the underlying stream or decode
// error, exposed via Unwrap; pass nil for a content-level sentinel with none.
func NewUnsupportedEndpointError(provider, message string, cause error) error {
	return &UnsupportedEndpointError{nonHTTPBaseError{
		provider:  provider,
		message:   message,
		retryable: false,
		cause:     cause,
	}}
}

// NewNoObjectGeneratedError reports that no valid object could be produced from
// the model output. cause is the underlying parse/validation error, exposed via
// Unwrap; pass nil when none applies.
func NewNoObjectGeneratedError(message string, rawText string, cause error) error {
	return &NoObjectGeneratedError{message: message, retryable: false, cause: cause, RawText: rawText}
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
