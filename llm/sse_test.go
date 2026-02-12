package llm

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseSSE_BasicEvent(t *testing.T) {
	input := "data: hello\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "hello" {
		t.Errorf("expected data 'hello', got %q", string(events[0].Data))
	}
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	input := "data: one\n\ndata: two\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if string(events[0].Data) != "one" {
		t.Errorf("expected 'one', got %q", events[0].Data)
	}
	if string(events[1].Data) != "two" {
		t.Errorf("expected 'two', got %q", events[1].Data)
	}
}

func TestParseSSE_WithEventType(t *testing.T) {
	input := "event: message\ndata: payload\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "message" {
		t.Errorf("expected event type 'message', got %q", events[0].Event)
	}
}

func TestParseSSE_ContextCancellation_WithTimeout(t *testing.T) {
	// The blocking path cannot detect context cancellation while stuck on
	// ReadString. When a StreamReadTimeout is configured, context cancellation
	// is handled via select, so it works reliably.
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		pw.Write([]byte("data: hello\n\n"))
		// Stall forever.
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(5*time.Second))
	// Context fires before the stream-read timeout.
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestParseSSE_StreamReadTimeout_FiresOnStall(t *testing.T) {
	// Create a reader that sends one event then stalls.
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("data: hello\n\n"))
		// Stall forever (don't write anything else, don't close).
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(500*time.Millisecond))

	if err == nil {
		t.Fatal("expected error from stream read timeout, got nil")
	}
	if !strings.Contains(err.Error(), "stream read timeout") {
		t.Errorf("expected 'stream read timeout' in error, got: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event before timeout, got %d", len(events))
	}
	pw.Close()
}

func TestParseSSE_StreamReadTimeout_ResetsOnData(t *testing.T) {
	// Verify that the timeout resets after each line of data, not just events.
	pr, pw := io.Pipe()
	go func() {
		// Write lines slowly but within the timeout window.
		pw.Write([]byte("data: first\n\n"))
		time.Sleep(100 * time.Millisecond)
		pw.Write([]byte("data: second\n\n"))
		time.Sleep(100 * time.Millisecond)
		pw.Write([]byte("data: third\n\n"))
		time.Sleep(100 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(300*time.Millisecond))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
}

func TestParseSSE_StreamReadTimeout_ZeroMeansNoTimeout(t *testing.T) {
	// Zero timeout should behave like the original (no per-read timeout).
	input := "data: hello\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(0))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestParseSSE_NoOptions_PreservesExistingBehavior(t *testing.T) {
	// Verify that calling ParseSSE without options still works identically.
	input := "event: delta\ndata: content\n\nevent: done\ndata: [DONE]\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Event != "delta" {
		t.Errorf("expected event type 'delta', got %q", events[0].Event)
	}
	if string(events[1].Data) != "[DONE]" {
		t.Errorf("expected '[DONE]', got %q", events[1].Data)
	}
}
