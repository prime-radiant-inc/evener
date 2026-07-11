//go:build serffuzz

package clock

import (
	"testing"
	"time"
)

func FuzzRealTimerStopResetNonPositive(f *testing.F) {
	f.Add(int64(0), int64(-1))
	f.Add(int64(-1_000_000), int64(0))
	f.Add(int64(-9), int64(-17))

	f.Fuzz(func(t *testing.T, first, second int64) {
		timer := Real().NewTimer(fuzzNonPositiveDuration(first))
		assertFuzzStoppedTimerHasNoValue(t, timer, "initial")

		// Reset is intentionally exercised with another non-positive duration. We
		// never wait for it to fire; a successful Stop is the deterministic oracle.
		timer.Reset(fuzzNonPositiveDuration(second))
		assertFuzzStoppedTimerHasNoValue(t, timer, "after reset")
	})
}

// FuzzRealClockProgram exercises the real clock wrappers without waiting for
// wall-clock delivery: every scheduled primitive stays pending and is stopped
// before it can fire.
func FuzzRealClockProgram(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(-1))
	f.Add(int64(1_000_000_000))

	f.Fuzz(func(t *testing.T, value int64) {
		c := Real()
		if c.Now().IsZero() {
			t.Fatal("Real().Now returned zero time")
		}
		c.Sleep(0)

		d := fuzzPendingDuration(value)
		if after := c.After(d); after == nil {
			t.Fatal("Real().After returned nil channel")
		}

		afterFunc := c.AfterFunc(d, func() {})
		if afterFunc.C() != nil {
			t.Fatal("AfterFunc timer exposed a channel")
		}
		if !afterFunc.Stop() {
			t.Fatal("pending AfterFunc timer did not stop")
		}

		timer := c.NewTimer(d)
		if timer.C() == nil {
			t.Fatal("NewTimer returned nil channel")
		}
		if !timer.Stop() {
			t.Fatal("pending timer did not stop")
		}
		timer.Reset(d)
		if !timer.Stop() {
			t.Fatal("reset pending timer did not stop")
		}

		ticker := c.NewTicker(d)
		if ticker.C() == nil {
			t.Fatal("NewTicker returned nil channel")
		}
		ticker.Reset(d)
		ticker.Stop()
	})
}

func fuzzPendingDuration(value int64) time.Duration {
	return time.Hour + time.Duration(uint64(value)%uint64(time.Hour))
}

func fuzzNonPositiveDuration(value int64) time.Duration {
	if value > 0 {
		value = -value
	}
	return time.Duration(value)
}

func assertFuzzStoppedTimerHasNoValue(t *testing.T, timer Timer, phase string) {
	t.Helper()
	if !timer.Stop() {
		return // It had already fired; no timing-dependent expectation remains.
	}
	select {
	case value := <-timer.C():
		t.Fatalf("%s timer delivered %v after successful Stop", phase, value)
	default:
	}
}
