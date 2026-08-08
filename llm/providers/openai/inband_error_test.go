package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/llm"
)

// sseAdapterFor serves body as an SSE response on every path and returns an
// adapter pointed at it. chatHits counts requests that reached the Chat
// Completions endpoint, so a test can prove the Responses path did not fall
// back.
func sseAdapterFor(t *testing.T, body string, chatHits *atomic.Int32) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chatHits != nil && strings.Contains(r.URL.Path, "/chat/completions") {
			chatHits.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
}

func collectStreamEvents(t *testing.T, stream llm.Stream) (text string, streamErr error) {
	t.Helper()
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventTextDelta:
			text += ev.Delta
		case llm.StreamEventError:
			streamErr = ev.Err
		}
	}
	return text, streamErr
}

func collectChatCompletionsStream(t *testing.T, a *Adapter) (string, error) {
	t.Helper()
	stream, err := a.streamViaChatCompletions(context.Background(), llm.Request{
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("streamViaChatCompletions: %v", err)
	}
	return collectStreamEvents(t, stream)
}

func collectResponsesStream(t *testing.T, a *Adapter) (string, error) {
	t.Helper()
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return collectStreamEvents(t, stream)
}

// An in-band error chunk with an HTTP-like integer code on a Chat Completions
// 200 stream must surface as a typed provider error, not as the generic
// stream-closed-without-[DONE] error.
func TestChatCompletionsInbandError_IntCode_TypedRateLimit(t *testing.T) {
	body := "data: {\"error\":{\"message\":\"Provider returned error: rate limited upstream\",\"code\":429}}\n\n"
	a := sseAdapterFor(t, body, nil)
	_, streamErr := collectChatCompletionsStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if strings.Contains(streamErr.Error(), "closed without [DONE]") {
		t.Fatalf("in-band error degraded to generic incomplete-stream error: %v", streamErr)
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 429 || llm.Kind(streamErr) != llm.KindRateLimit {
		t.Fatalf("got status=%d kind=%v, want 429/KindRateLimit: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "rate limited upstream") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// An OpenAI-style in-band error (string code, no HTTP-like status) still
// produces a typed error carrying the error code.
func TestChatCompletionsInbandError_StringCode_TypedUnknown(t *testing.T) {
	body := "data: {\"error\":{\"message\":\"You exceeded your current quota\",\"type\":\"insufficient_quota\",\"code\":\"insufficient_quota\"}}\n\n"
	a := sseAdapterFor(t, body, nil)
	_, streamErr := collectChatCompletionsStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.ErrorCode() != "insufficient_quota" {
		t.Fatalf("ErrorCode = %q, want insufficient_quota: %v", le.ErrorCode(), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "exceeded your current quota") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// An in-band error after real content still terminates with the typed error;
// the already-delivered deltas stay delivered.
func TestChatCompletionsInbandError_AfterContent(t *testing.T) {
	body := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial \"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"upstream disconnected\",\"code\":502}}\n\n"
	a := sseAdapterFor(t, body, nil)
	text, streamErr := collectChatCompletionsStream(t, a)
	if text != "partial answer" {
		t.Fatalf("delivered text = %q, want %q", text, "partial answer")
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 502 || llm.Kind(streamErr) != llm.KindServer {
		t.Fatalf("got status=%d kind=%v, want 502/KindServer: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
}

// Unparseable line noise keeps today's behavior: skipped, and a stream that
// then ends without [DONE] still reports the generic incomplete error.
func TestChatCompletionsInbandError_LineNoiseStillSkipped(t *testing.T) {
	body := "data: this is not json\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"
	a := sseAdapterFor(t, body, nil)
	text, streamErr := collectChatCompletionsStream(t, a)
	if text != "x" {
		t.Fatalf("delivered text = %q, want %q", text, "x")
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "closed without [DONE]") {
		t.Fatalf("expected generic incomplete-stream error for noise+EOF, got: %v", streamErr)
	}
}

// A Responses-API "error" event on a 200 stream must surface as a typed error.
// It must not be mistaken for an empty stream, which would silently retry the
// request against Chat Completions and hide the real failure.
func TestResponsesInbandError_ErrorEvent_TypedNoFallback(t *testing.T) {
	var chatHits atomic.Int32
	body := "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit reached for gpt-test\",\"param\":null}\n\n"
	a := sseAdapterFor(t, body, &chatHits)
	_, streamErr := collectResponsesStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if chatHits.Load() != 0 {
		t.Fatalf("structured failure fell back to Chat Completions (%d hits): %v", chatHits.Load(), streamErr)
	}
	if errors.Is(streamErr, errEmptyResponsesStream) {
		t.Fatalf("structured failure degraded to the empty-stream sentinel: %v", streamErr)
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.ErrorCode() != "rate_limit_exceeded" {
		t.Fatalf("ErrorCode = %q, want rate_limit_exceeded: %v", le.ErrorCode(), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "Rate limit reached") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// A "response.failed" event carries its error nested in the response object;
// it must decode to the same typed error rather than a raw passthrough.
func TestResponsesInbandError_ResponseFailed_Typed(t *testing.T) {
	var chatHits atomic.Int32
	body := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\"," +
		"\"error\":{\"code\":\"server_error\",\"message\":\"The model failed to generate a response\"}}}\n\n"
	a := sseAdapterFor(t, body, &chatHits)
	_, streamErr := collectResponsesStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if chatHits.Load() != 0 {
		t.Fatalf("structured failure fell back to Chat Completions (%d hits): %v", chatHits.Load(), streamErr)
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.ErrorCode() != "server_error" {
		t.Fatalf("ErrorCode = %q, want server_error: %v", le.ErrorCode(), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "failed to generate a response") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// A Responses failure after real content terminates with the typed error
// instead of the generic "ended without completion".
func TestResponsesInbandError_AfterContent(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial \"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n" +
		"data: {\"type\":\"error\",\"code\":502,\"message\":\"upstream disconnected\"}\n\n"
	a := sseAdapterFor(t, body, nil)
	text, streamErr := collectResponsesStream(t, a)
	if text != "partial answer" {
		t.Fatalf("delivered text = %q, want %q", text, "partial answer")
	}
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if strings.Contains(streamErr.Error(), "ended without completion") {
		t.Fatalf("in-band error degraded to generic incomplete-stream error: %v", streamErr)
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 502 || llm.Kind(streamErr) != llm.KindServer {
		t.Fatalf("got status=%d kind=%v, want 502/KindServer: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
}

// An unknown Responses event with no error payload keeps today's behavior:
// forwarded raw, and the empty stream still reports the fallback sentinel.
func TestResponsesInbandError_UnknownEventStillPassthrough(t *testing.T) {
	body := "data: {\"type\":\"response.some_future_event\",\"detail\":\"x\"}\n\n"
	a := sseAdapterFor(t, body, nil)
	stream, err := a.streamResponses(context.Background(), llm.Request{
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("streamResponses: %v", err)
	}
	_, streamErr := collectStreamEvents(t, stream)
	if !errors.Is(streamErr, errEmptyResponsesStream) {
		t.Fatalf("expected the empty-stream sentinel for an unknown event, got: %v", streamErr)
	}
}
