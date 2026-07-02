//go:build serffuzz

package agent

import (
	"testing"
	"time"
)

// FuzzCtClampJobBlockTimeout drives clampJobBlockTimeout — the max_wait_ms
// clamp shared by the read-window and job-stop wait paths — over arbitrary ms.
// Oracles: determinism; the result always lands in the supported window
// [minJobBlockTimeoutMS, maxJobBlockTimeoutMS]; monotone non-decreasing in ms;
// and idempotence under re-clamp of the clamped value.
func FuzzCtClampJobBlockTimeout(f *testing.F) {
	f.Add(500)
	f.Add(1500)
	f.Add(999999)
	f.Add(-42)

	f.Fuzz(func(t *testing.T, ms int) {
		// Bound magnitude so the +1 monotonicity probe cannot overflow int.
		ms %= 1 << 30

		d := clampJobBlockTimeout(ms)
		if d2 := clampJobBlockTimeout(ms); d != d2 {
			t.Fatalf("non-deterministic: %v vs %v", d, d2)
		}

		minD := time.Duration(minJobBlockTimeoutMS) * time.Millisecond
		maxD := time.Duration(maxJobBlockTimeoutMS) * time.Millisecond
		if d < minD || d > maxD {
			t.Fatalf("out of window: %v not in [%v,%v] (ms=%d)", d, minD, maxD, ms)
		}

		// Monotone non-decreasing in ms.
		if more := clampJobBlockTimeout(ms + 1); more < d {
			t.Fatalf("not monotone: f(%d)=%v > f(%d)=%v", ms, d, ms+1, more)
		}

		// Idempotent: re-clamping the clamped ms-equivalent is stable.
		if reclamped := clampJobBlockTimeout(int(d / time.Millisecond)); reclamped != d {
			t.Fatalf("not idempotent: reclamp of %v -> %v", d, reclamped)
		}
	})
}
