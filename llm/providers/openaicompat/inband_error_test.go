package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"errors"
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
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return NewForInstance(OpenAICompatInstanceParams{Name: "openrouter", BaseURL: srv.URL, APIKey: "k", Quirks: QuirksPreset("openrouter")})
}

func collectStream(t *testing.T, a *Adapter) (text string, streamErr error) {
	t.Helper()
	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "kimi-k3",
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

// An OpenRouter-style in-band error chunk (integer code) on an HTTP 200
// stream must surface as a typed provider error, not as the generic
// "stream ended without completion".
func TestInbandError_IntCode_TypedRateLimit(t *testing.T) {
	body := ": OPENROUTER PROCESSING\n\n" +
		"data: {\"error\":{\"message\":\"Provider returned error: rate limited upstream\",\"code\":429,\"metadata\":{\"provider_name\":\"moonshot\"}}}\n\n"
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
	if le.StatusCode() != 429 {
		t.Fatalf("StatusCode = %d, want 429 (from in-band integer code): %v", le.StatusCode(), streamErr)
	}
	if llm.Kind(streamErr) != llm.KindRateLimit {
		t.Fatalf("Kind = %v, want KindRateLimit: %v", llm.Kind(streamErr), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "rate limited upstream") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// An OpenAI-style in-band error (string code, no HTTP-like status) must
// still produce a typed error carrying the error code, degrading to the
// retryable unknown class rather than the generic stream error.
func TestInbandError_StringCode_TypedUnknown(t *testing.T) {
	body := "data: {\"error\":{\"message\":\"You exceeded your current quota\",\"type\":\"insufficient_quota\",\"code\":\"insufficient_quota\"}}\n\n"
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
	if le.ErrorCode() != "insufficient_quota" {
		t.Fatalf("ErrorCode = %q, want insufficient_quota: %v", le.ErrorCode(), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "exceeded your current quota") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// An in-band error arriving after real content must still terminate with
// the typed error; the already-delivered deltas stay delivered.
func TestInbandError_AfterContent(t *testing.T) {
	body := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial \"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"upstream disconnected\",\"code\":502}}\n\n"
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
	if le.StatusCode() != 502 || llm.Kind(streamErr) != llm.KindServer {
		t.Fatalf("got status=%d kind=%v, want 502/KindServer: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
}

// Unparseable line noise keeps today's behavior: skipped, and a stream that
// then ends without [DONE] still reports the generic incomplete error.
func TestInbandError_LineNoiseStillSkipped(t *testing.T) {
	body := "data: this is not json\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"
	a := streamAdapterFor(t, body)
	text, streamErr := collectStream(t, a)
	if text != "x" {
		t.Fatalf("delivered text = %q, want %q", text, "x")
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "ended without completion") {
		t.Fatalf("expected generic incomplete-stream error for noise+EOF, got: %v", streamErr)
	}
}
