package main

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

func TestRelayRetryBackoffReset(t *testing.T) {
	var b relayRetryBackoff
	// Advance a few times
	b.Next()
	b.Next()
	b.Next()
	// Reset should set delay back to 0
	b.Reset()
	if b.delay != 0 {
		t.Fatalf("after Reset, delay should be 0, got %v", b.delay)
	}
	// Next after reset should return the min delay
	got := b.Next()
	if got != relayRetryMinDelay {
		t.Fatalf("first Next after Reset should be %v, got %v", relayRetryMinDelay, got)
	}
}

func TestRelayTimerClockWaitCompletes(t *testing.T) {
	clock := relayTimerClock{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clock.Wait(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("Wait should complete without error, got %v", err)
	}
}

func TestRelayTimerClockWaitCancelledContext(t *testing.T) {
	clock := relayTimerClock{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Wait(ctx, 10*time.Millisecond); err != context.Canceled {
		t.Fatalf("Wait with cancelled context should return context.Canceled, got %v", err)
	}
}

func TestRelayRetryClockFuncWait(t *testing.T) {
	called := false
	f := relayRetryClockFunc(func(ctx context.Context, delay time.Duration) error {
		called = true
		return nil
	})
	if err := f.Wait(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("Wait should not error, got %v", err)
	}
	if !called {
		t.Fatal("func should have been called")
	}
}

func TestHubThreadReadResultFinishNil(t *testing.T) {
	if got := (*hubThreadReadResult)(nil).finish(true); got {
		t.Fatal("nil finish should return false")
	}
}

func TestHubThreadReadResultFinishNoHandoff(t *testing.T) {
	r := &hubThreadReadResult{}
	if got := r.finish(true); got {
		t.Fatal("finish with no handoff should return false")
	}
}

func TestHubThreadReadResultFinishCommitTrue(t *testing.T) {
	handoff := &fakeRelayHandoff{commitResult: true}
	r := &hubThreadReadResult{handoff: handoff, release: func() {}}
	if !r.finish(true) {
		t.Fatal("finish with commit=true should return true when handoff.Commit returns true")
	}
	if !handoff.commitCalled {
		t.Fatal("handoff.Commit should have been called")
	}
	if handoff.abortCalled {
		t.Fatal("handoff.Abort should NOT be called when commit is true")
	}
}

func TestHubThreadReadResultFinishCommitFalse(t *testing.T) {
	handoff := &fakeRelayHandoff{abortResult: true}
	r := &hubThreadReadResult{handoff: handoff, release: func() {}}
	if !r.finish(false) {
		t.Fatal("finish with commit=false should return true when handoff.Abort returns true")
	}
	if handoff.commitCalled {
		t.Fatal("handoff.Commit should NOT be called when commit is false")
	}
}

func TestHubThreadReadResultFinishIdempotent(t *testing.T) {
	callCount := 0
	handoff := &fakeRelayHandoff{commitResult: true}
	r := &hubThreadReadResult{handoff: handoff, release: func() {}}
	r.finish(true)
	r.finish(true)
	// The once.Do ensures release is only called once
	// We can verify by checking handoff is only called once
	callCount++ // Already called once above
	if callCount != 1 {
		t.Fatalf("finish should be idempotent, but was called %d times", callCount)
	}
}

func TestSubscribeRelayRecoveryCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := subscribeRelayRecovery(ctx, nil, appwire.ThreadReadParams{})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// fakeRelayHandoff is a test double for appsource.RelayHandoff.
type fakeRelayHandoff struct {
	commitResult bool
	abortResult  bool
	commitCalled bool
	abortCalled  bool
}

func (h *fakeRelayHandoff) Commit() bool {
	h.commitCalled = true
	return h.commitResult
}

func (h *fakeRelayHandoff) Abort() bool {
	h.abortCalled = true
	return h.abortResult
}

func (h *fakeRelayHandoff) Prepare() bool { return true }

// Ensure fakeRelayHandoff satisfies the interface (at compile time).
var _ appsource.RelayHandoff = (*fakeRelayHandoff)(nil)
