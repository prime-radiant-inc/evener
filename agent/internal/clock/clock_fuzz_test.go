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
