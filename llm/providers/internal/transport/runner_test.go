package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// respFrom wraps body in a minimal *http.Response for the runner.
func respFrom(body string) *http.Response {
	return &http.Response{Body: io.NopCloser(strings.NewReader(body))}
}

// drain runs the runner to completion on its stream and returns the emitted
// events. The runner is the single producer, so CloseSend happens after Run.
func drain(ctx context.Context, t *testing.T, r *StreamRunner) []llm.StreamEvent {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.Stream.CloseSend()
		r.Run(ctx)
	}()
	var got []llm.StreamEvent
	for ev := range r.Stream.Events() {
		got = append(got, ev)
	}
	<-done
	return got
}

func TestRun_PersistsResponseFromBodyObservation(t *testing.T) {
	const responseBody = "data: finish\n\n"
	sink := &responseAssociationSink{}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader(responseBody)),
			ContentLength: -1,
		}, nil
	})}
	request, err := http.NewRequestWithContext(
		attemptContext("ag_stream_runner_observation", sink),
		http.MethodPost,
		"https://provider.test/v1",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	finished := false
	runner := &StreamRunner{
		Provider:   "test",
		Resp:       response,
		Attempt:    attempt,
		StatusCode: response.StatusCode,
		Stream:     llm.NewChanStream(nil),
		OnEvent: func(event llm.SSEEvent) error {
			finished = string(event.Data) == "finish"
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "test stream ended without completion",
	}
	_ = drain(context.Background(), t, runner)

	record := onlyAttempt(t, sink)
	if record.Response == nil {
		t.Fatal("stream runner omitted its observed response")
	}
	if !record.Response.Body.Exact {
		t.Fatal("stream runner marked an EOF-observed response inexact")
	}
	got, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if string(got) != responseBody {
		t.Fatalf("recorded response = %q, want %q", got, responseBody)
	}
}

// TestRun_EpilogueWrapsContextErrorOnCancel asserts that when the stream ends
// without the adapter marking Finished and the context carries an error, the
// runner emits WrapContextError(provider, ctxErr) — not the IncompleteMsg path.
func TestRun_EpilogueWrapsContextErrorOnCancel(t *testing.T) {
	// Deadline already in the past so ctx.Err() == context.DeadlineExceeded.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	finished := false
	r := &StreamRunner{
		Provider: "anthropic",
		Resp:     respFrom("data: {\"a\":1}\n\n"), // no terminal event → not finished
		Stream:   llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "anthropic stream ended without completion",
	}
	got := drain(ctx, t, r)

	var errEv *llm.StreamEvent
	for i := range got {
		if got[i].Type == llm.StreamEventError {
			errEv = &got[i]
		}
	}
	if errEv == nil {
		t.Fatal("expected a terminal error event")
	}
	want := llm.WrapContextError("anthropic", context.DeadlineExceeded)
	if llm.Kind(errEv.Err) != llm.KindTimeout {
		t.Fatalf("ctx-cancel epilogue: got %T (%v), want %T (%v)", errEv.Err, errEv.Err, want, want)
	}
	// It must NOT be a StreamError carrying IncompleteMsg.
	if _, ok := errors.AsType[*llm.StreamError](errEv.Err); ok {
		t.Fatalf("ctx-cancel epilogue wrongly produced StreamError: %v", errEv.Err)
	}
	// The provider name must be threaded from r.Provider into WrapContextError.
	var llmErr llm.Error
	if !errors.As(errEv.Err, &llmErr) {
		t.Fatalf("ctx-cancel epilogue: error %T does not implement llm.Error", errEv.Err)
	}
	if got := llmErr.Provider(); got != "anthropic" {
		t.Fatalf("ctx-cancel epilogue: provider = %q, want %q", got, "anthropic")
	}
}

// TestRun_EpilogueStreamErrorOnIncomplete asserts that when the stream ends
// unfinished and the context has no error, the runner emits
// NewStreamError(provider, IncompleteMsg, parseErr).
func TestRun_EpilogueStreamErrorOnIncomplete(t *testing.T) {
	finished := false
	r := &StreamRunner{
		Provider: "google",
		Resp:     respFrom("data: {\"a\":1}\n\n"), // no terminal event → not finished
		Stream:   llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "google stream ended without completion",
	}
	got := drain(context.Background(), t, r)

	var errEv *llm.StreamEvent
	for i := range got {
		if got[i].Type == llm.StreamEventError {
			errEv = &got[i]
		}
	}
	if errEv == nil {
		t.Fatal("expected a terminal error event")
	}
	var streamErr *llm.StreamError
	if !errors.As(errEv.Err, &streamErr) {
		t.Fatalf("incomplete epilogue: got %T (%v), want *llm.StreamError", errEv.Err, errEv.Err)
	}
	if streamErr.Provider() != "google" {
		t.Fatalf("StreamError provider = %q, want %q", streamErr.Provider(), "google")
	}
	if !strings.Contains(streamErr.Error(), "google stream ended without completion") {
		t.Fatalf("StreamError message = %q, want IncompleteMsg embedded", streamErr.Error())
	}
}

// TestRun_NoErrorWhenFinished asserts the runner emits no terminal error event
// when the adapter marked Finished, even with a cancelled context.
func TestRun_NoErrorWhenFinished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx.Err() == context.Canceled

	finished := true // adapter already completed
	r := &StreamRunner{
		Provider: "openai",
		Resp:     respFrom(""),
		Stream:   llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "should not appear",
	}
	got := drain(ctx, t, r)

	for _, ev := range got {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("finished stream must emit no error event, got %v", ev.Err)
		}
	}
}

// TestRun_NeverCallsCancel verifies the runner does not invoke Stream.Close().
// Close is the consumer-side tear-down API; the runner is the producer and must
// only call Send/CloseSend. We observe this via a cancel hook counter, then
// confirm Stream.Close() completes without deadlocking after drain: CloseSend
// was called by drain so <-s.done returns immediately.
func TestRun_NeverCallsCancel(t *testing.T) {
	var closeCalls int
	stream := llm.NewChanStream(func() { closeCalls++ })

	finished := false
	r := &StreamRunner{
		Provider: "openai-compatible",
		Resp:     respFrom("data: {\"a\":1}\n\ndata: [DONE]\n\n"),
		Stream:   stream,
		OnEvent: func(ev llm.SSEEvent) error {
			if string(ev.Data) == "[DONE]" {
				finished = true
			}
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "openai-compatible stream ended without completion",
	}
	_ = drain(context.Background(), t, r)

	if closeCalls != 0 {
		t.Fatalf("runner called Stream.Close() %d time(s); it must never cancel the stream", closeCalls)
	}
	// After CloseSend (called by drain), Stream.Close() must not block:
	// <-s.done returns immediately because s.done was already closed.
	stream.Close()
}

// TestRun_TerminalErrorHookReplacesTheIncompleteWrap: an adapter whose stream
// has more than one way to end without completing supplies TerminalError
// instead of IncompleteMsg, and the runner publishes exactly what the hook
// returns. Without the hook the responses adapter had to reimplement the whole
// epilogue, and the copy drifted from this one.
func TestRun_TerminalErrorHookReplacesTheIncompleteWrap(t *testing.T) {
	sentinel := errors.New("adapter's own terminal classification")
	finished := false
	var sawParseErr error
	var hookCalls int
	r := &StreamRunner{
		Provider:      "responses",
		Resp:          respFrom("data: {\"a\":1}\n\n"),
		Stream:        llm.NewChanStream(nil),
		OnEvent:       func(llm.SSEEvent) error { return nil },
		Finished:      &finished,
		IncompleteMsg: "responses stream ended without completion",
		TerminalError: func(parseErr error) error {
			hookCalls++
			sawParseErr = parseErr
			return sentinel
		},
	}
	got := drain(context.Background(), t, r)
	if hookCalls != 1 {
		t.Fatalf("TerminalError called %d times, want 1", hookCalls)
	}
	if sawParseErr != nil {
		t.Fatalf("hook parseErr = %v, want nil for a stream that closed cleanly", sawParseErr)
	}
	if len(got) != 1 || got[0].Type != llm.StreamEventError || !errors.Is(got[0].Err, sentinel) {
		t.Fatalf("events = %+v, want the hook's error verbatim", got)
	}
	if strings.Contains(got[0].Err.Error(), "responses stream ended without completion") {
		t.Fatalf("the hook must replace the IncompleteMsg wrap, not add to it: %v", got[0].Err)
	}
}

// TestRun_TerminalErrorHookIsNotConsultedOnCancelOrFatal: the two endings the
// runner already classifies stay the runner's, so an adapter's hook cannot
// turn a caller cancellation or an in-band provider failure into something
// else.
func TestRun_TerminalErrorHookIsNotConsultedOnCancelOrFatal(t *testing.T) {
	inband := errors.New("provider reported a rate limit in-band")
	for _, tc := range []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		onEvent func(llm.SSEEvent) error
		want    func(t *testing.T, err error)
	}{
		{
			name: "caller cancelled",
			ctx: func(t *testing.T) context.Context {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			onEvent: func(llm.SSEEvent) error { return nil },
			want: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("terminal error = %v, want the wrapped context error", err)
				}
			},
		},
		{
			name:    "in-band fatal",
			ctx:     func(*testing.T) context.Context { return context.Background() },
			onEvent: func(llm.SSEEvent) error { return &FatalStreamError{Err: inband} },
			want: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, inband) {
					t.Fatalf("terminal error = %v, want the in-band error verbatim", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finished := false
			hookCalls := 0
			r := &StreamRunner{
				Provider:      "responses",
				Resp:          respFrom("data: {\"a\":1}\n\n"),
				Stream:        llm.NewChanStream(nil),
				OnEvent:       tc.onEvent,
				Finished:      &finished,
				IncompleteMsg: "responses stream ended without completion",
				TerminalError: func(error) error {
					hookCalls++
					return errors.New("the hook must not run here")
				},
			}
			got := drain(tc.ctx(t), t, r)
			if hookCalls != 0 {
				t.Fatalf("TerminalError ran %d times for %s", hookCalls, tc.name)
			}
			if len(got) != 1 || got[0].Type != llm.StreamEventError {
				t.Fatalf("events = %+v, want one terminal error", got)
			}
			tc.want(t, got[0].Err)
		})
	}
}
