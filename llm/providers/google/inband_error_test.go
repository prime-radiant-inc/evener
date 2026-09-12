package google

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// collectStream serves body as an SSE response and drains the stream the
// protocol decodes from it, returning the text and the terminal error.
func collectStream(t *testing.T, body string) (text string, streamErr error) {
	t.Helper()
	srv, _ := protoServer(t, 200, body)
	stream, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), protoReq(""), protoLive(srv))
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

// Gemini reports a mid-stream rejection as its standard error envelope inside
// an HTTP 200 stream. It must surface as a typed provider error carrying the
// envelope's status, not as the generic incomplete-stream error.
func TestInbandError_ResourceExhausted_TypedRateLimit(t *testing.T) {
	body := "data: {\"error\":{\"code\":429,\"message\":\"Resource has been exhausted (e.g. check quota).\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
	_, streamErr := collectStream(t, body)
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
	if !strings.Contains(streamErr.Error(), "Resource has been exhausted") {
		t.Fatalf("provider message lost: %v", streamErr)
	}
}

// Gemini can report a rate limit as an ambiguous 400 whose gRPC status names
// the real cause; the in-band path reclassifies it exactly like the non-2xx
// path does.
func TestInbandError_AmbiguousCode_ReclassifiedByGRPCStatus(t *testing.T) {
	body := "data: {\"error\":{\"code\":400,\"message\":\"quota exceeded\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
	_, streamErr := collectStream(t, body)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if llm.Kind(streamErr) != llm.KindRateLimit {
		t.Fatalf("Kind = %v, want KindRateLimit: %v", llm.Kind(streamErr), streamErr)
	}
}

// An error envelope arriving after real content must still terminate with the
// typed error; the already-delivered deltas stay delivered.
func TestInbandError_AfterContent(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial \"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]}}]}\n\n" +
		"data: {\"error\":{\"code\":503,\"message\":\"The model is overloaded\",\"status\":\"UNAVAILABLE\"}}\n\n"
	text, streamErr := collectStream(t, body)
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
	if le.StatusCode() != 503 || llm.Kind(streamErr) != llm.KindServer {
		t.Fatalf("got status=%d kind=%v, want 503/KindServer: %v", le.StatusCode(), llm.Kind(streamErr), streamErr)
	}
}

// Unparseable line noise keeps today's behavior: forwarded raw, and a stream
// that then ends without a finishReason still reports the generic error.
func TestInbandError_LineNoiseStillSkipped(t *testing.T) {
	body := "data: this is not json\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"x\"}]}}]}\n\n"
	text, streamErr := collectStream(t, body)
	if text != "x" {
		t.Fatalf("delivered text = %q, want %q", text, "x")
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "ended without completion") {
		t.Fatalf("expected generic incomplete-stream error for noise+EOF, got: %v", streamErr)
	}
}
