package google

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

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

func TestInbandError_ResourceExhausted_TypedRateLimit(t *testing.T) {
	t.Parallel()
	body := "event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"resource_exhausted\",\"message\":\"Resource has been exhausted (e.g. check quota).\"}}\n\n"
	_, streamErr := collectStream(t, body)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if _, ok := errors.AsType[*llm.StreamError](streamErr); ok {
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

func TestInbandError_GatewayTimeout_TypedTimeout(t *testing.T) {
	t.Parallel()
	body := "event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"gateway_timeout\",\"message\":\"deadline exceeded\"}}\n\n"
	_, streamErr := collectStream(t, body)
	if streamErr == nil {
		t.Fatal("expected a terminal stream error, got none")
	}
	if llm.Kind(streamErr) != llm.KindTimeout {
		t.Fatalf("Kind = %v, want KindTimeout: %v", llm.Kind(streamErr), streamErr)
	}
}

func TestInbandError_AfterContent(t *testing.T) {
	t.Parallel()
	body := "event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"partial \"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"answer\"}}\n\n" +
		"event: error\ndata: {\"event_type\":\"error\",\"error\":{\"code\":\"unavailable\",\"message\":\"The model is overloaded\"}}\n\n"
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

func TestInbandError_LineNoiseStillSkipped(t *testing.T) {
	t.Parallel()
	body := "data: this is not json\n\n" +
		"event: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"model_output\"}}\n\n" +
		"event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"x\"}}\n\n"
	text, streamErr := collectStream(t, body)
	if text != "x" {
		t.Fatalf("delivered text = %q, want %q", text, "x")
	}
	if _, ok := errors.AsType[*llm.StreamError](streamErr); !ok {
		t.Fatalf("expected generic incomplete-stream error for noise+EOF, got: %v", streamErr)
	}
}
