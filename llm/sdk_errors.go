package llm

import (
	"fmt"
	"strings"
	"time"
)

type nonHTTPErrorBase struct {
	provider    string
	behaviorTag string
	message     string
	retryable   bool
	retryAfter  *time.Duration
	cause       error
}

func (e *nonHTTPErrorBase) Error() string {
	msg := strings.TrimSpace(e.message)
	if msg == "" {
		msg = "request failed"
	}
	if strings.TrimSpace(e.provider) == "" {
		return msg
	}
	return fmt.Sprintf("%s error: %s", e.provider, msg)
}
func (e *nonHTTPErrorBase) Provider() string           { return e.provider }
func (e *nonHTTPErrorBase) setProvider(name string)    { e.provider = strings.TrimSpace(name) }
func (e *nonHTTPErrorBase) BehaviorTag() string        { return e.behaviorTag }
func (e *nonHTTPErrorBase) setBehaviorTag(tag string)  { e.behaviorTag = strings.TrimSpace(tag) }
func (e *nonHTTPErrorBase) StatusCode() int            { return 0 }
func (e *nonHTTPErrorBase) ErrorCode() string          { return "" }
func (e *nonHTTPErrorBase) Retryable() bool            { return e.retryable }
func (e *nonHTTPErrorBase) RetryAfter() *time.Duration { return e.retryAfter }
func (e *nonHTTPErrorBase) Raw() any                   { return nil }
func (e *nonHTTPErrorBase) Unwrap() error              { return e.cause }

// AbortError is a non-HTTP error reporting a user-initiated cancellation.
type AbortError struct{ nonHTTPErrorBase }

// NetworkError is a non-HTTP error reporting a network-level failure.
type NetworkError struct{ nonHTTPErrorBase }

// StreamError is a non-HTTP error reporting a streaming failure.
type StreamError struct{ nonHTTPErrorBase }

// InvalidToolCallError is a non-HTTP error reporting an invalid tool call.
type InvalidToolCallError struct{ nonHTTPErrorBase }

// NoObjectGeneratedError is a non-HTTP error reporting that no valid object
// could be produced from the model output. RawText holds the model output text
// that could not be parsed or validated.
type NoObjectGeneratedError struct {
	nonHTTPErrorBase
	RawText string
}

// UnsupportedToolChoiceError is a non-HTTP error reporting that the requested
// tool_choice mode is not supported.
type UnsupportedToolChoiceError struct{ nonHTTPErrorBase }

// NewAbortError reports a user-initiated cancellation. cause is the underlying
// error (typically context.Canceled); it is exposed via Unwrap so errors.Is(err,
// context.Canceled) holds. Pass nil when there is no underlying cause.
func NewAbortError(message string, cause error) error {
	return &AbortError{nonHTTPErrorBase{message: message, retryable: false, cause: cause}}
}

// NewStreamError reports a streaming failure. cause is the underlying read/parse
// error (e.g. an SSE decode failure or a wrapped context error) and is exposed
// via Unwrap; pass nil for a content-level stream sentinel with no cause.
func NewStreamError(provider, message string, cause error) error {
	return &StreamError{nonHTTPErrorBase{provider: provider, message: message, retryable: true, cause: cause}}
}

// NewNoObjectGeneratedError reports that no valid object could be produced from
// the model output. cause is the underlying parse/validation error, exposed via
// Unwrap; pass nil when none applies.
func NewNoObjectGeneratedError(message string, rawText string, cause error) error {
	return &NoObjectGeneratedError{nonHTTPErrorBase: nonHTTPErrorBase{message: message, retryable: false, cause: cause}, RawText: rawText}
}

// NewUnsupportedToolChoiceError reports that the given tool_choice mode is not
// supported by provider. The error is not retryable.
func NewUnsupportedToolChoiceError(provider, mode string) error {
	msg := strings.TrimSpace(mode)
	if msg == "" {
		msg = "(empty)"
	}
	return &UnsupportedToolChoiceError{nonHTTPErrorBase{provider: provider, message: fmt.Sprintf("unsupported tool_choice mode: %s", msg), retryable: false}}
}
