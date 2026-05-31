package llm

import "errors"

// ErrStreamUnsupported indicates that streaming is not supported.
var ErrStreamUnsupported = errors.New("stream unsupported")

// Stream is an asynchronous iterator of StreamEvent values. Implementations must
// be explicitly closed when the consumer is done to avoid leaking connections.
type Stream interface {
	Events() <-chan StreamEvent
	Close() error
}

// StreamEventType identifies the kind of a StreamEvent.
type StreamEventType string

const (
	// StreamEventStreamStart marks the start of the stream.
	StreamEventStreamStart StreamEventType = "STREAM_START"
	// StreamEventTextStart marks the start of a text segment.
	StreamEventTextStart StreamEventType = "TEXT_START"
	// StreamEventTextDelta carries an incremental chunk of text.
	StreamEventTextDelta StreamEventType = "TEXT_DELTA"
	// StreamEventTextEnd marks the end of a text segment.
	StreamEventTextEnd StreamEventType = "TEXT_END"
	// StreamEventReasoningStart marks the start of a reasoning segment.
	StreamEventReasoningStart StreamEventType = "REASONING_START"
	// StreamEventReasoningDelta carries an incremental chunk of reasoning.
	StreamEventReasoningDelta StreamEventType = "REASONING_DELTA"
	// StreamEventReasoningEnd marks the end of a reasoning segment.
	StreamEventReasoningEnd StreamEventType = "REASONING_END"
	// StreamEventToolCallStart marks the start of a tool call.
	StreamEventToolCallStart StreamEventType = "TOOL_CALL_START"
	// StreamEventToolCallDelta carries an incremental chunk of a tool call.
	StreamEventToolCallDelta StreamEventType = "TOOL_CALL_DELTA"
	// StreamEventToolCallEnd marks the end of a tool call.
	StreamEventToolCallEnd StreamEventType = "TOOL_CALL_END"
	// StreamEventStepFinish marks the completion of a step.
	StreamEventStepFinish StreamEventType = "STEP_FINISH"
	// StreamEventObjectDelta carries an incremental chunk of structured output.
	StreamEventObjectDelta StreamEventType = "OBJECT_DELTA"
	// StreamEventFinish marks the completion of the stream.
	StreamEventFinish StreamEventType = "FINISH"
	// StreamEventError reports an error that occurred during streaming.
	StreamEventError StreamEventType = "ERROR"
	// StreamEventProviderEvent carries a provider-specific passthrough event.
	StreamEventProviderEvent StreamEventType = "PROVIDER_EVENT"
)

// StreamEvent is a single event emitted on a Stream. Type indicates which kind
// of event it is and therefore which of the remaining fields are populated.
type StreamEvent struct {
	Type StreamEventType `json:"type"`

	// Text events
	Delta  string `json:"delta,omitempty"`
	TextID string `json:"text_id,omitempty"`

	// Reasoning events
	ReasoningDelta string `json:"reasoning_delta,omitempty"`

	// Object delta events (streaming structured output)
	ObjectDelta any `json:"object_delta,omitempty"`

	// Tool call events
	ToolCall *ToolCallData `json:"tool_call,omitempty"`

	// Finish event
	FinishReason *FinishReason `json:"finish_reason,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
	Response     *Response     `json:"response,omitempty"`

	// Error event
	Err error `json:"-"`

	// Passthrough
	Raw map[string]any `json:"raw,omitempty"`
}
