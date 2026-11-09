package llm

// Regression tests for #4941: a provider-specific passthrough event the
// adapter classifies as carrying content must commit the attempt the same way
// a text delta does. Retrying after it would publish the same content twice.
// Control-only passthrough frames stay retryable so a stream that truncates
// before any content recovers.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// passthroughCommitOptions is the retry policy both tests share: enough budget
// that the retry would happen if the commitment check missed the event.
func passthroughCommitOptions(client *Client) GenerateOptions {
	prompt := "prompt"
	return GenerateOptions{
		Client:   client,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
		RetryPolicy: &RetryPolicy{
			MaxRetries:        1,
			BaseDelay:         time.Millisecond,
			MaxDelay:          10 * time.Millisecond,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

// TestStreamGenerate_ContentBearingProviderEventBlocksRetry reproduces the
// reported refusal double-publish: the adapter emits a content-bearing
// passthrough, then the stream truncates. A retry would resend the refusal to
// the caller, so the high-level stream must stop after one attempt.
func TestStreamGenerate_ContentBearingProviderEventBlocksRetry(t *testing.T) {
	c, a := newScriptedStreamClient(
		// Attempt 1: a content-bearing passthrough, then truncation.
		replayEvents(StreamEvent{
			Type:           StreamEventProviderEvent,
			Raw:            map[string]any{"type": "response.refusal.delta", "delta": "I cannot help with that."},
			ContentBearing: true,
		}),
		// Attempt 2 must never run: it would publish different content after
		// the caller already saw the refusal.
		replayEvents(finishEvent(Response{Model: "m", Message: Assistant("settled")})),
	)

	res, err := StreamGenerate(context.Background(), passthroughCommitOptions(c))
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	var rawSeen int
	for ev := range res.Events() {
		if ev.Type == StreamEventProviderEvent {
			rawSeen++
		}
	}
	if rawSeen != 1 {
		t.Fatalf("raw content events forwarded = %d, want 1 (no replay)", rawSeen)
	}
	if got := len(a.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1: a content-bearing passthrough must commit the attempt", got)
	}
	if _, rerr := res.Response(); rerr == nil {
		t.Fatal("Response() error = nil, want the truncation surfaced")
	} else if _, ok := errors.AsType[*StreamError](rerr); !ok {
		t.Fatalf("Response() error = %T (%v), want *StreamError", rerr, rerr)
	}
}

// TestStreamGenerate_ControlOnlyProviderEventStillRetries pins the other side
// of the classification: a passthrough the adapter does NOT mark carries no
// content, so a truncation after it still recovers through the retry budget.
func TestStreamGenerate_ControlOnlyProviderEventStillRetries(t *testing.T) {
	c, a := newScriptedStreamClient(
		replayEvents(
			StreamEvent{Type: StreamEventStreamStart},
			StreamEvent{
				Type: StreamEventProviderEvent,
				Raw:  map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}},
			},
		),
		replayEvents(finishEvent(Response{Model: "m", Message: Assistant("ok")})),
	)

	res, err := StreamGenerate(context.Background(), passthroughCommitOptions(c))
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck
	for range res.Events() {
	}
	if got := len(a.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2: a control-only passthrough must not block retry", got)
	}
	resp, rerr := res.Response()
	if rerr != nil {
		t.Fatalf("Response error: %v", rerr)
	}
	if resp == nil || resp.Text() != "ok" {
		t.Fatalf("response = %+v, want ok", resp)
	}
}
