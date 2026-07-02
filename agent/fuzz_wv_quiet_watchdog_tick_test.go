//go:build serffuzz

package agent

import (
	"testing"
	"time"
)

// FuzzWvQuietWatchdogTick drives decideQuietWatchdogTick — the pure decision core
// lifted out of fireQuietWatchdogTick — over adversarial timing snapshots.
//
// Oracles (beyond never-panic):
//   - determinism: the same snapshot yields the same decision;
//   - latch monotonicity: an already-notified stretch never fires again;
//   - quiet < window never latches the notified flag (it clears it);
//   - a fire always latches the notified flag;
//   - the window boundary is inclusive: quiet == window latches;
//   - a dead tick (closing or non-delegate) never fires or latches.
func FuzzWvQuietWatchdogTick(f *testing.F) {
	f.Add(false, true, int64(0), int64(600), int64(300), false)
	f.Add(false, true, int64(0), int64(300), int64(300), false)
	f.Add(false, true, int64(0), int64(600), int64(300), true)
	f.Add(false, true, int64(0), int64(100), int64(300), false)
	f.Add(true, true, int64(0), int64(600), int64(300), false)
	f.Add(false, false, int64(0), int64(600), int64(300), false)

	f.Fuzz(func(t *testing.T, closing, isDelegate bool, lastSec, nowSec, windowSec int64, alreadyNotified bool) {
		base := time.Unix(1_700_000_000, 0).UTC()
		snap := quietTickSnapshot{
			closing:         closing,
			isDelegate:      isDelegate,
			last:            base.Add(time.Duration(lastSec) * time.Second),
			now:             base.Add(time.Duration(nowSec) * time.Second),
			window:          time.Duration(windowSec) * time.Second,
			alreadyNotified: alreadyNotified,
		}
		dec := decideQuietWatchdogTick(snap)
		if dec2 := decideQuietWatchdogTick(snap); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}
		if alreadyNotified && dec.fire {
			t.Fatalf("already-notified stretch must not fire again: %+v", dec)
		}
		if dec.fire && !dec.newNotifiedLatch {
			t.Fatalf("a fire must latch the notified flag: %+v", dec)
		}
		if !dec.keepAlive {
			if dec.fire || dec.newNotifiedLatch {
				t.Fatalf("dead tick must not fire or latch: %+v", dec)
			}
		} else {
			quiet := snap.now.Sub(snap.last)
			if quiet < snap.window && dec.newNotifiedLatch {
				t.Fatalf("quiet<window must clear the latch: quiet=%v window=%v dec=%+v", quiet, snap.window, dec)
			}
			if quiet >= snap.window && !dec.newNotifiedLatch {
				t.Fatalf("quiet>=window must latch: quiet=%v window=%v dec=%+v", quiet, snap.window, dec)
			}
		}
	})
}
