package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
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

// TestRun_TeeOnlyWhenRawBodyEnabled asserts the runner tees the response body
// into a buffer exactly when llm.RawBodyEnabled() is true, and hands that buffer
// (else nil) to OnEvent. Both branches are exercised unconditionally: the
// disabled path runs inline; the enabled path runs in a subprocess with
// SERF_LOG_RAW_HTTP=1 so the positive assertions always execute.
func TestRun_TeeOnlyWhenRawBodyEnabled(t *testing.T) {
	if os.Getenv("SERF_TRANSPORT_TEE_ENABLED_HELPER") == "1" {
		runTeeEnabledHelper(t)
		return
	}

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
	got := drain(context.Background(), t, r)

	if !sawEvent {
		t.Fatal("OnEvent was never invoked; runner did not read the body")
	}
	// finished was set on [DONE], so no terminal error event must be emitted.
	for _, ev := range got {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected error event after completion: %v", ev.Err)
		}
	}
	if !llm.RawBodyEnabled() {
		if lastBuf != nil {
			t.Fatalf("RawBody disabled: expected nil sseBuf, got %q", lastBuf.String())
		}
	}

	// Spawn a subprocess with SERF_LOG_RAW_HTTP=1 to unconditionally exercise
	// the tee path (sseBuf non-nil and populated with body content).
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestRun_TeeOnlyWhenRawBodyEnabled", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"SERF_TRANSPORT_TEE_ENABLED_HELPER=1",
		"SERF_LOG_RAW_HTTP=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tee-enabled helper failed: %v\n%s", err, out)
	}
}

func runTeeEnabledHelper(t *testing.T) {
	t.Helper()
	if !llm.RawBodyEnabled() {
		t.Fatal("RawBodyEnabled is false in helper subprocess")
	}

	const body = "event: ping\ndata: {\"a\":1}\n\ndata: [DONE]\n\n"

	var lastBuf *bytes.Buffer
	finished := false
	r := &StreamRunner{
		Provider: "test",
		Resp:     respFrom(body),
		Stream:   llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			lastBuf = sseBuf
			if string(ev.Data) == "[DONE]" {
				finished = true
			}
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "incomplete",
	}
	drain(context.Background(), t, r)

	if lastBuf == nil {
		t.Fatal("RawBodyEnabled: expected non-nil sseBuf in OnEvent")
	}
	// The tee mirrors everything the parser consumed from the body.
	if !strings.Contains(lastBuf.String(), "[DONE]") {
		t.Fatalf("RawBodyEnabled: tee buffer did not capture body, got %q", lastBuf.String())
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
	var streamErr *llm.StreamError
	if errors.As(errEv.Err, &streamErr) {
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
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
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

func TestRun_IncompleteErrorCarriesRawBodiesWhenEnabled(t *testing.T) {
	if os.Getenv("SERF_TRANSPORT_RAW_INCOMPLETE_HELPER") == "1" {
		runIncompleteRawBodyHelper(t)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestRun_IncompleteErrorCarriesRawBodiesWhenEnabled", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"SERF_TRANSPORT_RAW_INCOMPLETE_HELPER=1",
		"SERF_LOG_RAW_HTTP=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw incomplete stream helper failed: %v\n%s", err, out)
	}
}

func runIncompleteRawBodyHelper(t *testing.T) {
	t.Helper()
	if !llm.RawBodyEnabled() {
		t.Fatal("RawBodyEnabled is false in helper subprocess")
	}

	const requestBody = `{"model":"test"}`
	const body = "data: {\"partial\":1}\n\n"
	finished := false
	r := &StreamRunner{
		Provider:       "openai-compatible",
		Resp:           respFrom(body),
		RawRequestBody: requestBody,
		Stream:         llm.NewChanStream(nil),
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
			return nil
		},
		Finished:      &finished,
		IncompleteMsg: "openai-compatible stream ended without completion",
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
	var rawErr llm.RawHTTPBodyError
	if !errors.As(errEv.Err, &rawErr) {
		t.Fatalf("error %T (%v) does not expose raw HTTP bodies", errEv.Err, errEv.Err)
	}
	gotRequestBody, responseBody := rawErr.RawHTTPBodies()
	if gotRequestBody != requestBody {
		t.Fatalf("raw request body = %q, want %q", gotRequestBody, requestBody)
	}
	if responseBody != body {
		t.Fatalf("raw response body = %q, want %q", responseBody, body)
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
		OnEvent: func(ev llm.SSEEvent, sseBuf *bytes.Buffer) error {
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
