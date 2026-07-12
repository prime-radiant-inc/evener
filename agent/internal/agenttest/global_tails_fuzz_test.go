//go:build serffuzz

package agenttest

import (
	"testing"
	"time"
)

// FuzzGlobalAgenttestTails covers deterministic adapter and virtual-clock edge
// contracts that require deliberately constructed state rather than host time.
func FuzzGlobalAgenttestTails(f *testing.F) {
	f.Add(int64(0))
	f.Fuzz(func(t *testing.T, n int64) {
		adapter := &ModelTrackingAdapter{Provider: "tracked"}
		if adapter.Name() != "tracked" {
			t.Fatal("tracking adapter lost provider name")
		}

		clock := NewFakeClockAt(time.Unix(n%1000, 0).UTC())
		callback := make(chan struct{})
		clock.AfterFunc(0, func() { close(callback) })
		<-callback
		if got := <-clock.NewTimer(0).C(); !got.Equal(clock.Now()) {
			t.Fatalf("immediate timer fired at %v, want %v", got, clock.Now())
		}

		// Exercise the unsatisfied-blocker retention branch without scheduling or
		// wall-clock handshakes: these are the exact locked-state invariants used
		// by BlockUntil/addWaiterLocked.
		clock.mu.Lock()
		b := &blocker{count: 2, ch: make(chan struct{})}
		clock.blockers = append(clock.blockers, b)
		clock.addWaiterLocked(&fakeWaiter{until: clock.now.Add(time.Second), ch: make(chan time.Time, 1)})
		if len(clock.blockers) != 1 {
			clock.mu.Unlock()
			t.Fatal("unsatisfied blocker was not retained")
		}
		clock.addWaiterLocked(&fakeWaiter{until: clock.now.Add(time.Second), ch: make(chan time.Time, 1)})
		if len(clock.blockers) != 0 {
			clock.mu.Unlock()
			t.Fatal("satisfied blocker was retained")
		}
		clock.mu.Unlock()
		<-b.ch
	})
}
