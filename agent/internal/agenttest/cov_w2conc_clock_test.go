package agenttest

import (
	"testing"
	"time"
)

// TestW2Conc_FakeTickerResetWhilePending pins fakeTicker.Reset on a still-armed
// ticker: the waiter is re-timed in place (not duplicated) and fires at the new
// period.
func TestW2Conc_FakeTickerResetWhilePending(t *testing.T) {
	c := NewFakeClock()
	tk := c.NewTicker(time.Second)
	defer tk.Stop()

	if got := c.BlockedCount(); got != 1 {
		t.Fatalf("armed ticker waiters = %d, want 1", got)
	}
	tk.Reset(2 * time.Second)
	if got := c.BlockedCount(); got != 1 {
		t.Fatalf("waiters after Reset of a pending ticker = %d, want 1 (re-timed in place)", got)
	}

	// Old period elapsed but not the new one: must not fire yet.
	c.Advance(time.Second)
	select {
	case <-tk.C():
		t.Fatal("ticker fired at the old period after Reset to a longer one")
	default:
	}
	// New period reached: fires.
	c.Advance(time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("ticker did not fire at the new period after Reset")
	}
}

// TestW2Conc_FakeTickerResetAfterStop pins fakeTicker.Reset on a stopped ticker:
// Reset re-arms the removed waiter so it fires again.
func TestW2Conc_FakeTickerResetAfterStop(t *testing.T) {
	c := NewFakeClock()
	tk := c.NewTicker(time.Second)
	tk.Stop()
	if got := c.BlockedCount(); got != 0 {
		t.Fatalf("waiters after Stop = %d, want 0", got)
	}

	tk.Reset(time.Second)
	if got := c.BlockedCount(); got != 1 {
		t.Fatalf("waiters after Reset of a stopped ticker = %d, want 1 (re-armed)", got)
	}
	defer tk.Stop()

	c.Advance(time.Second)
	select {
	case <-tk.C():
	default:
		t.Fatal("re-armed ticker did not fire after Reset following Stop")
	}
}
