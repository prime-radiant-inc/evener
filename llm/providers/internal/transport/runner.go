// Package transport holds the streaming skeleton shared by the SSE-decoding
// provider adapters (anthropic, openaicompat, google, openai chat-completions).
// StreamRunner owns the RawBody tee prologue, the ParseSSE call, and the
// uniform "stream ended without completion" epilogue so the adapters cannot
// drift on these mechanics. Each adapter still owns its per-event decode logic
// (and any cancel()/finished bookkeeping it performs) via OnEvent.
package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"primeradiant.com/serf/llm"
)

// StreamRunner decodes a provider SSE response into stream events. It tees the
// response body into a buffer only when RawBody capture is enabled, invokes
// OnEvent for each SSE event, and emits the terminal error event when the
// stream ends without the adapter marking Finished.
type StreamRunner struct {
	// Provider names the adapter for error wrapping.
	Provider string
	// Resp is the live HTTP response whose Body carries the SSE stream.
	Resp *http.Response
	// RawRequestBody is the serialized request body to attach to raw stream
	// errors when RawBody capture is enabled.
	RawRequestBody string
	// CaptureRawBody overrides the process-wide raw-body setting when non-nil.
	// It supports deterministic callers that need an explicit capture policy.
	CaptureRawBody *bool
	// Attempt is the explicitly attached canonical attempt, when any. It makes
	// exact response-byte capture active independently of the legacy raw-body
	// option.
	Attempt *APIAttemptCapture
	// StatusCode is the HTTP response status for Attempt.
	StatusCode int
	// FinalEvent returns the terminal success event prepared by OnEvent. The
	// runner appends Attempt before publishing this event.
	FinalEvent func() *llm.StreamEvent
	// Stream receives decoded events, including the terminal error event.
	Stream *llm.ChanStream
	// SSEOpts are passed through to ParseSSE (e.g. the stream-read timeout).
	SSEOpts []llm.SSEOption
	// OnEvent decodes one SSE event. sseBuf is the RawBody capture buffer when
	// RawBody is enabled, otherwise nil; the adapter reads it to populate
	// RawResponseBody on its finish event.
	OnEvent func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error
	// Finished points at the adapter's completion flag. When it remains false
	// after ParseSSE returns, the runner emits the terminal error event.
	Finished *bool
	// IncompleteMsg is the message for the terminal error when the stream ends
	// without completion and the context carries no error.
	IncompleteMsg string
}

// Run drives the SSE decode loop. It never calls cancel(): any cancellation is
// the adapter's responsibility inside OnEvent.
func (r *StreamRunner) Run(ctx context.Context) {
	captureRawBody := llm.RawBodyEnabled()
	if r.CaptureRawBody != nil {
		captureRawBody = *r.CaptureRawBody
	}
	var sseBody io.Reader = r.Resp.Body
	var sseBuf *bytes.Buffer
	if captureRawBody || r.Attempt.Active() {
		sseBuf = &bytes.Buffer{}
		sseBody = io.TeeReader(r.Resp.Body, sseBuf)
	}

	parseErr := llm.ParseSSE(ctx, sseBody, func(ev llm.SSEEvent) error {
		return r.OnEvent(ev, sseBuf)
	}, r.SSEOpts...)

	var terminalEvent *llm.StreamEvent
	if r.FinalEvent != nil {
		terminalEvent = r.FinalEvent()
	}
	var terminalErr error
	if !*r.Finished {
		if err := ctx.Err(); err != nil {
			terminalErr = llm.WrapContextError(r.Provider, err)
		} else {
			rawReqBody := ""
			rawRespBody := ""
			if captureRawBody {
				rawReqBody = r.RawRequestBody
				if sseBuf != nil {
					rawRespBody = sseBuf.String()
				}
			}
			terminalErr = llm.NewStreamErrorWithRawBodies(r.Provider, r.IncompleteMsg, parseErr, rawReqBody, rawRespBody)
		}
	}
	var response *llm.Response
	if terminalEvent != nil {
		response = terminalEvent.Response
	}
	var responseBody []byte
	if sseBuf != nil {
		responseBody = append([]byte(nil), sseBuf.Bytes()...)
	}
	attemptDecodeErr := parseErr
	if attemptDecodeErr == nil {
		attemptDecodeErr = terminalErr
	}
	if *r.Finished {
		attemptDecodeErr = nil
	}
	timeoutSource := llm.APITimeoutNone
	if errors.Is(parseErr, llm.ErrSSEReadTimeout) {
		timeoutSource = llm.APITimeoutSSERead
	}
	r.Attempt.Complete(llm.APIAttemptResult{
		StatusCode:   r.StatusCode,
		ResponseBody: responseBody,
		Response:     response,
		Err:          terminalErr,
	}, timeoutSource, attemptDecodeErr, nil)
	if terminalEvent != nil {
		r.Stream.Send(*terminalEvent)
	} else if terminalErr != nil {
		r.Stream.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: terminalErr})
	}
}
