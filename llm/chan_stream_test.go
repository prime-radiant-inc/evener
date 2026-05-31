package llm

import (
	"context"
	"testing"
	"time"
)

func TestChanStream_Close_UnblocksBlockedSend(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	s := NewChanStream(cancel)

	// Fill the buffer. The constructor uses a fixed buffer size, so the next send
	// should block until Close closes the channel.
	for i := 0; i < 128; i++ {
		s.Send(StreamEvent{Type: StreamEventProviderEvent, Raw: map[string]any{"i": i}})
	}

	sent := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(sent)
		s.Send(StreamEvent{Type: StreamEventProviderEvent})
		// Simulate a producer that finishes after being unblocked by Close.
		s.CloseSend()
		close(done)
	}()
	<-sent

	// Give the goroutine a moment to become blocked on Send.
	time.Sleep(20 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Close blocked; expected it to return promptly")
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("blocked Send did not unblock after Close")
	}
}

func TestChanStream_SendAfterClose_DoesNotPanic(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	s := NewChanStream(cancel)
	s.CloseSend()
	_ = s.Close()
	s.Send(StreamEvent{Type: StreamEventProviderEvent})
}

// TestChanStream_NoRaceProducerVsConsumerClose drives the single-producer
// contract under -race: one producer goroutine Sends a stream then CloseSends,
// while the consumer reads Events() and triggers Close mid-stream. Confirms the
// removed Send recover() was unnecessary — no race, no send-on-closed panic.
func TestChanStream_NoRaceProducerVsConsumerClose(t *testing.T) {
	for i := 0; i < 50; i++ {
		_, cancel := context.WithCancel(context.Background())
		s := NewChanStream(cancel)
		go func() {
			defer s.CloseSend()
			for j := 0; j < 100; j++ {
				s.Send(StreamEvent{Type: StreamEventTextDelta, Delta: "x"})
			}
		}()
		n := 0
		for range s.Events() {
			n++
			if n == 5 {
				go func() { _ = s.Close() }()
			}
		}
	}
}
