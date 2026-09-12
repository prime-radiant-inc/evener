// Package transport holds the streaming skeleton shared by the SSE-decoding
// protocol packages: anthropic, chatcompletions, google and responses.
// StreamRunner owns the ParseSSE call and the "stream ended without
// completion" epilogue — the terminal-error selection, the attempt
// bookkeeping and the final send — so the protocols cannot drift on these
// mechanics. Each protocol still owns its per-event decode logic (and any
// cancel()/finished bookkeeping it performs) via OnEvent, and a protocol with
// more than one way to end without completing classifies that ending in
// TerminalError.
package transport

import (
	"context"
	"errors"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// FatalStreamError marks an OnEvent error as the stream's terminal error in
// its own right: the runner publishes the wrapped error verbatim instead of
// folding it into the generic "stream ended without completion" epilogue.
// Adapters use it when the provider reported a structured failure in-band on
// an HTTP 200 stream (e.g. an OpenAI-compatible {"error": ...} chunk), where
// the decoded, typed error is strictly more informative than the wrap.
type FatalStreamError struct{ Err error }

func (e *FatalStreamError) Error() string { return e.Err.Error() }
func (e *FatalStreamError) Unwrap() error { return e.Err }

// StreamRunner decodes a provider SSE response into stream events. It invokes
// OnEvent for each SSE event and emits the terminal error event when the stream
// ends without the adapter marking Finished.
type StreamRunner struct {
	// Provider names the adapter for error wrapping.
	Provider string
	// Resp is the live HTTP response whose Body carries the SSE stream.
	Resp *http.Response
	// Attempt is the explicitly attached canonical attempt, when any. It owns
	// exact response-byte capture for the private per-session API log.
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
	// OnEvent decodes one SSE event.
	OnEvent func(ev llm.SSEEvent) error
	// Finished points at the adapter's completion flag. When it remains false
	// after ParseSSE returns, the runner emits the terminal error event.
	Finished *bool
	// IncompleteMsg is the message for the terminal error when the stream ends
	// without completion and the context carries no error.
	IncompleteMsg string
	// TerminalError, when set, replaces the IncompleteMsg wrap: the runner
	// publishes what it returns as the stream's terminal error. It is called
	// only for the endings the runner does not already classify — never for a
	// caller cancellation or a FatalStreamError, both of which stay the
	// runner's. An adapter needs it when "the stream ended without
	// completing" is more than one condition: the Responses transport has to
	// tell a stall from a broken read from a stream that produced nothing its
	// API can produce, and only the last of those says anything about whether
	// the endpoint implements the API at all.
	TerminalError func(parseErr error) error
}

// incomplete builds the terminal error for a stream that ended without
// completing and without a cancellation or an in-band failure.
func (r *StreamRunner) incomplete(parseErr error) error {
	if r.TerminalError != nil {
		return r.TerminalError(parseErr)
	}
	return llm.NewStreamError(r.Provider, r.IncompleteMsg, parseErr)
}

// Run drives the SSE decode loop. It never calls cancel(): any cancellation is
// the adapter's responsibility inside OnEvent.
func (r *StreamRunner) Run(ctx context.Context) {
	parseErr := llm.ParseSSE(ctx, r.Resp.Body, func(ev llm.SSEEvent) error {
		return r.OnEvent(ev)
	}, r.SSEOpts...)

	var terminalEvent *llm.StreamEvent
	if r.FinalEvent != nil {
		terminalEvent = r.FinalEvent()
	}
	var terminalErr error
	if !*r.Finished {
		if err := ctx.Err(); err != nil {
			terminalErr = llm.WrapContextError(r.Provider, err)
		} else if fatal, ok := errors.AsType[*FatalStreamError](parseErr); ok {
			terminalErr = fatal.Err
		} else {
			terminalErr = r.incomplete(parseErr)
		}
	}
	var response *llm.Response
	if terminalEvent != nil {
		response = terminalEvent.Response
	}
	attemptDecodeErr := parseErr
	if attemptDecodeErr == nil {
		attemptDecodeErr = terminalErr
	}
	if *r.Finished {
		attemptDecodeErr = nil
	}
	timeoutSource := llm.APITimeoutSourceForSSE(parseErr)
	if timeoutSource == llm.APITimeoutNone {
		timeoutSource = r.Attempt.TimeoutSource()
	}
	outcome := apilog.AttemptOutcomeClass("")
	if !*r.Finished && ctx.Err() == context.Canceled {
		outcome = apilog.AttemptCallerCancel
	}
	r.Attempt.Complete(llm.APIAttemptResult{
		StatusCode: r.StatusCode,
		Response:   response,
		Outcome:    outcome,
		Err:        terminalErr,
	}, timeoutSource, attemptDecodeErr, nil)
	if terminalEvent != nil {
		r.Stream.Send(*terminalEvent)
	} else if terminalErr != nil {
		r.Stream.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: terminalErr})
	}
}
