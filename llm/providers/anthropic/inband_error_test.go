package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// streamAdapterFor serves body as an SSE response and returns an adapter
// pointed at it.
func streamAdapterFor(t *testing.T, body string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
}

func collectStream(t *testing.T, a *Adapter) (text string, streamErr error) {
	t.Helper()
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
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

// Anthropic reports an overloaded upstream as an in-band error event on an
// HTTP 200 stream. It must surface as a typed provider error carrying the
// documented 529 status, not as the generic incomplete-stream error.
func TestInbandError_Overloaded_TypedError(t *testing.T) {
	body := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	a := streamAdapterFor(t, body)
	_, streamErr := collectStream(t, a)
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
	if le.StatusCode() != 529 {
		t.Fatalf("StatusCode = %d, want 529 (overloaded_error): %v", le.StatusCode(), streamErr)
	}
	if le.ErrorCode() != "overloaded_error" {
		t.Fatalf("ErrorCode = %q, want overloaded_error: %v", le.ErrorCode(), streamErr)
	}
	if !le.Retryable() {
		t.Fatalf("overloaded_error must stay retryable: %v", streamErr)
	}
	if !strings.Contains(streamErr.Error(), "Overloaded") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// A rate_limit_error event maps to the 429 class so retry logic sees a rate
// limit rather than an opaque stream failure.
func TestInbandError_RateLimit_TypedRateLimit(t *testing.T) {
	body := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of requests has exceeded your rate limit\"}}\n\n"
	a := streamAdapterFor(t, body)
	_, streamErr := collectStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 429 || llm.Kind(streamErr) != llm.KindRateLimit {
		t.Fatalf("got status=%d kind=%v, want 429/KindRateLimit: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "exceeded your rate limit") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// An authentication_error event maps to the permanent 401 class: retrying a
// rejected credential just burns the retry budget.
func TestInbandError_Authentication_TypedAuth(t *testing.T) {
	body := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"invalid x-api-key\"}}\n\n"
	a := streamAdapterFor(t, body)
	_, streamErr := collectStream(t, a)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 401 || llm.Kind(streamErr) != llm.KindAuthentication {
		t.Fatalf("got status=%d kind=%v, want 401/KindAuthentication: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
	if le.Retryable() {
		t.Fatalf("authentication_error must not be retryable: %v", streamErr)
	}
}

// An error event arriving after real content must still terminate with the
// typed error; the already-delivered deltas stay delivered.
func TestInbandError_AfterContent(t *testing.T) {
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial \"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Internal server error\"}}\n\n"
	a := streamAdapterFor(t, body)
	text, streamErr := collectStream(t, a)
	if text != "partial answer" {
		t.Fatalf("delivered text = %q, want %q", text, "partial answer")
	}
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	var le llm.Error
	if !errors.As(streamErr, &le) {
		t.Fatalf("terminal error is not a typed llm.Error: %T %v", streamErr, streamErr)
	}
	if le.StatusCode() != 500 || llm.Kind(streamErr) != llm.KindServer {
		t.Fatalf("got status=%d kind=%v, want 500/KindServer: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
}

// An error type outside the documented set still yields a typed error rather
// than the generic stream error: the unknown class is retryable and the
// provider's own error type rides along as the error code.
func TestInbandError_UnknownType_TypedUnknown(t *testing.T) {
	body := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"tenant_quarantined_error\",\"message\":\"tenant quarantined\"}}\n\n"
	a := streamAdapterFor(t, body)
	_, streamErr := collectStream(t, a)
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
	if le.ErrorCode() != "tenant_quarantined_error" {
		t.Fatalf("ErrorCode = %q, want tenant_quarantined_error: %v", le.ErrorCode(), streamErr)
	}
	if !le.Retryable() {
		t.Fatalf("unknown error class must default to retryable: %v", streamErr)
	}
}

// Unparseable line noise keeps today's behavior: forwarded raw, and a stream
// that then ends without message_stop still reports the generic error.
func TestInbandError_LineNoiseStillSkipped(t *testing.T) {
	body := "event: content_block_delta\n" +
		"data: this is not json\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n"
	a := streamAdapterFor(t, body)
	text, streamErr := collectStream(t, a)
	if text != "x" {
		t.Fatalf("delivered text = %q, want %q", text, "x")
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "ended without completion") {
		t.Fatalf("expected generic incomplete-stream error for noise+EOF, got: %v", streamErr)
	}
}
