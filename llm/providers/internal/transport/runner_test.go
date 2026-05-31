package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// respFrom wraps body in a minimal *http.Response for the runner.
func respFrom(body string) *http.Response {
	return &http.Response{Body: io.NopCloser(strings.NewReader(body))}
}

// drain runs the runner to completion on its stream and returns the emitted
// events. The runner is the single producer, so CloseSend happens after Run.
func drain(t *testing.T, r *StreamRunner, ctx context.Context) []llm.StreamEvent {
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

// TestRun_TeeOnlyWhenRawBodyEnabled asserts the runner tees the response body
// into a buffer exactly when llm.RawBodyEnabled() is true, and hands that buffer
// (else nil) to OnEvent. RawBodyEnabled is frozen at llm-package init from
// SERF_LOG_RAW_HTTP, so this validates whichever mode the binary runs in: with
// the env set, the tee path (buffer non-nil and filled); without it, the nil path.
func TestRun_TeeOnlyWhenRawBodyEnabled(t *testing.T) {
	const body = "event: ping\ndata: {\"a\":1}\n\ndata: [DONE]\n\n"

	var lastBuf *bytes.Buffer
	var sawEvent bool
	finished := false
	r := &StreamRunner{
		Provider: "test",
		Resp:     respFrom(body),
		Stream:   llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			sawEvent = true
			lastBuf = sseBuf
			if string(ev.Data) == "[DONE]" {
				finished = true
			}
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "incomplete",
	}
	got := drain(t, r, context.Background())

	if !sawEvent {
		t.Fatal("OnEvent was never invoked; runner did not read the body")
	}
	// finished was set on [DONE], so no terminal error event must be emitted.
	for _, ev := range got {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected error event after completion: %v", ev.Err)
		}
	}

	if llm.RawBodyEnabled() {
		if lastBuf == nil {
			t.Fatal("RawBodyEnabled: expected non-nil sseBuf in OnEvent")
		}
		// The tee mirrors everything the parser consumed from the body.
		if !strings.Contains(lastBuf.String(), "[DONE]") {
			t.Fatalf("RawBodyEnabled: tee buffer did not capture body, got %q", lastBuf.String())
		}
	} else {
		if lastBuf != nil {
			t.Fatalf("RawBody disabled: expected nil sseBuf, got %q", lastBuf.String())
		}
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
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "anthropic stream ended without completion",
	}
	got := drain(t, r, ctx)

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
	var timeoutErr *llm.RequestTimeoutError
	if !errors.As(errEv.Err, &timeoutErr) {
		t.Fatalf("ctx-cancel epilogue: got %T (%v), want %T (%v)", errEv.Err, errEv.Err, want, want)
	}
	// It must NOT be a StreamError carrying IncompleteMsg.
	var streamErr *llm.StreamError
	if errors.As(errEv.Err, &streamErr) {
		t.Fatalf("ctx-cancel epilogue wrongly produced StreamError: %v", errEv.Err)
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
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "google stream ended without completion",
	}
	got := drain(t, r, context.Background())

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
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "should not appear",
	}
	got := drain(t, r, ctx)

	for _, ev := range got {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("finished stream must emit no error event, got %v", ev.Err)
		}
	}
}

// TestRun_NeverCallsCancel documents the no-cancel invariant: the runner holds
// no CancelFunc and never cancels. We assert it by passing a stream whose
// cancel hook would flip a flag if invoked, then confirming it stays false
// across a normal run. (The runner never receives this cancel; ChanStream.Close
// owns it, and the runner never calls Close.)
func TestRun_NeverCallsCancel(t *testing.T) {
	cancelled := false
	stream := llm.NewChanStream(func() { cancelled = true })

	finished := false
	r := &StreamRunner{
		Provider: "openai-compatible",
		Resp:     respFrom("data: {\"a\":1}\n\ndata: [DONE]\n\n"),
		Stream:   stream,
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			if string(ev.Data) == "[DONE]" {
				finished = true
			}
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "openai-compatible stream ended without completion",
	}
	_ = drain(t, r, context.Background())

	if cancelled {
		t.Fatal("runner invoked the stream cancel hook; it must never cancel")
	}
}
