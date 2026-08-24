package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCovObservedAPILogErrorAPILogFailureWasObserved exercises the
// apiLogFailureWasObserved method on observedAPILogError (apilog.go line 51),
// which is never called directly in normal flow.
func TestCovObservedAPILogErrorAPILogFailureWasObserved(t *testing.T) {
	err := markAPILogErrorObserved(errors.New("fail"))
	var observed apiLogObservedFailure
	if !errors.As(err, &observed) {
		t.Fatalf("markAPILogErrorObserved result should satisfy apiLogObservedFailure: %T", err)
	}
	// Calling the method directly to cover line 51.
	observed.apiLogFailureWasObserved()
}

// TestCovNewAPILoggerDirError covers the ensurePrivateAPILogDirectory error
// path in NewAPILogger (apilog.go lines 80-81). We force MkdirAll to fail
// by nesting the directory under a regular file.
func TestCovNewAPILoggerDirError(t *testing.T) {
	// A path under a regular file cannot have a directory created.
	regular := filepath.Join(t.TempDir(), "regular_file")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// filepath.Dir of this path is the regular file. os.Stat succeeds on
	// the regular file, so ensurePrivateAPILogDirectory returns nil. We
	// need MkdirAll to fail: use a nested path under the regular file.
	path := filepath.Join(regular, "subdir", "api.jsonl")
	_, err := NewAPILogger(path)
	if err == nil {
		t.Fatal("NewAPILogger under a regular file should error")
	}
}

// TestCovPumpClosingChannelReturn covers the closing-channel return path in
// pump() (apilog.go line 280). This happens when Close() is called while
// the inner stream is still active.
func TestCovPumpClosingChannelReturn(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_pump_close")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)

	inner := newTestStreamWithEvents()
	stream := newAPIAttemptSettlementStream(ctx, inner, group)

	// Close before the inner stream finishes — this triggers the
	// closing-channel path in pump.
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestCovPumpStreamEventError covers the StreamEventError case in pump()
// (apilog.go line 290).
func TestCovPumpStreamEventError(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_pump_error")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)

	inner := newTestStreamWithError(errors.New("provider error"))
	stream := newAPIAttemptSettlementStream(ctx, inner, group)

	// Drain the output channel until it closes.
	for range stream.Events() {
	}
	// Wait for pump to finish.
	<-stream.done

	// The settlement should record the error outcome.
	_, settlements, _ := sink.snapshot()
	if len(settlements) != 1 {
		t.Fatalf("settlements = %d, want 1", len(settlements))
	}
}

// TestCovPumpClosingDuringForward covers the closing-channel return inside
// the forward select in pump() (apilog.go line 295). This happens when
// Close() is called while pump is blocked trying to forward an event to
// s.out. We fill s.out (capacity 128) by sending 129 events without
// draining, so the pump blocks on the forward, and Close() hits the
// closing path.
func TestCovPumpClosingDuringForward(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_pump_fwd_close2")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)

	// Create a stream that sends 129 events. The pump forwards 128
	// (filling s.out's buffer), then blocks on the 129th forward.
	inner := newTestStreamBlockingAfter(129)
	stream := newAPIAttemptSettlementStream(ctx, inner, group)

	// Wait for the inner stream to finish sending all 129 events, then
	// yield to let the pump process them.
	<-inner.allSent
	// Yield repeatedly to give the pump goroutine time to forward 128
	// events and block on the 129th.
	for i := 0; i < 200; i++ {
		runtime.Gosched()
	}

	// Don't drain s.out — the pump fills the 128-capacity buffer, then
	// blocks on the forward. Close() triggers the closing path.
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// testStreamWithEvents is a simple Stream that emits a finish event.
type testStreamWithEvents struct {
	events chan StreamEvent
	done   chan struct{}
}

func newTestStreamWithEvents() *testStreamWithEvents {
	s := &testStreamWithEvents{
		events: make(chan StreamEvent, 1),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		s.events <- StreamEvent{Type: StreamEventFinish}
		close(s.events)
	}()
	return s
}

func (s *testStreamWithEvents) Events() <-chan StreamEvent { return s.events }
func (s *testStreamWithEvents) Close() error {
	select {
	case <-s.done:
	default:
	}
	return nil
}

// testStreamWithError is a Stream that emits an error event.
type testStreamWithError struct {
	events chan StreamEvent
	done   chan struct{}
}

func newTestStreamWithError(err error) *testStreamWithError {
	s := &testStreamWithError{
		events: make(chan StreamEvent, 1),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		s.events <- StreamEvent{Type: StreamEventError, Err: err}
		close(s.events)
	}()
	return s
}

func (s *testStreamWithError) Events() <-chan StreamEvent { return s.events }
func (s *testStreamWithError) Close() error {
	select {
	case <-s.done:
	default:
	}
	return nil
}

// testStreamBlockingAfter is a Stream that sends n events into a buffered
// channel (capacity n) and then blocks. The events channel stays open so the
// pump does not see it close. allSent is closed once all n events have been
// queued.
type testStreamBlockingAfter struct {
	events  chan StreamEvent
	done    chan struct{}
	allSent chan struct{}
}

func newTestStreamBlockingAfter(n int) *testStreamBlockingAfter {
	s := &testStreamBlockingAfter{
		events:  make(chan StreamEvent, n),
		done:    make(chan struct{}),
		allSent: make(chan struct{}),
	}
	go func() {
		for i := 0; i < n; i++ {
			s.events <- StreamEvent{Type: StreamEventTextDelta, Delta: "x"}
		}
		close(s.allSent)
		// Block until Close is called; keep events channel open.
		<-s.done
	}()
	return s
}

func (s *testStreamBlockingAfter) Events() <-chan StreamEvent { return s.events }
func (s *testStreamBlockingAfter) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}
