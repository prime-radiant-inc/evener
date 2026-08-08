package kimi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// kimi speaks the OpenAI-compatible wire through a forwarder over the
// openaicompat adapter, so an in-band error chunk on an HTTP 200 stream must
// reach the caller as the same typed error the backing adapter decodes — not
// as the generic incomplete-stream error.
func TestInbandError_DelegatesToBackingAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"rate limited upstream\",\"code\":429}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(InstanceParams{BaseURL: srv.URL, APIKey: "k"})
	a.Client = srv.Client()

	stream, err := a.Stream(context.Background(), llm.Request{
		Model:    "kimi-k2.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var streamErr error
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			streamErr = ev.Err
		}
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
	if le.StatusCode() != 429 || llm.Kind(streamErr) != llm.KindRateLimit {
		t.Fatalf("got status=%d kind=%v, want 429/KindRateLimit: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
	if !strings.Contains(streamErr.Error(), "rate limited upstream") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}
