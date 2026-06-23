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
	var sseBody io.Reader = r.Resp.Body
	var sseBuf *bytes.Buffer
	if llm.RawBodyEnabled() {
		sseBuf = &bytes.Buffer{}
		sseBody = io.TeeReader(r.Resp.Body, sseBuf)
	}

	parseErr := llm.ParseSSE(ctx, sseBody, func(ev llm.SSEEvent) error {
		return r.OnEvent(ev, sseBuf)
	}, r.SSEOpts...)

	if !*r.Finished {
		if err := ctx.Err(); err != nil {
			r.Stream.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError(r.Provider, err)})
		} else {
			rawReqBody := ""
			rawRespBody := ""
			if llm.RawBodyEnabled() {
				rawReqBody = r.RawRequestBody
				if sseBuf != nil {
					rawRespBody = sseBuf.String()
				}
			}
			r.Stream.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamErrorWithRawBodies(r.Provider, r.IncompleteMsg, parseErr, rawReqBody, rawRespBody)})
		}
	}
}
