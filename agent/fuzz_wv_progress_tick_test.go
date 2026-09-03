//go:build evenerfuzz

package agent

import (
	"testing"
)

// FuzzWvProgressTick drives decideProgressTick — the pure decision core lifted
// out of fireProgressTick — over adversarial tick snapshots.
//
// Oracles (beyond never-panic):
//   - determinism: the same snapshot yields the same decision;
//   - mutual exclusion: a send delivery and a budget-counted notification are
//     never both requested by one tick;
//   - a tick that does not fire never keeps the goroutine alive;
//   - a LIVE tick always fires (the breaker policy removed the suppression
//     branch: self-influence classifies at the fire site, never gates here);
//   - a fire routes exactly one way, and keeps the goroutine alive unless it is
//     a one-shot timer's single fire, which is also its last.
func FuzzWvProgressTick(f *testing.F) {
	f.Add(false, true, false, true, true, false, "dlg_1")
	f.Add(false, true, true, false, false, true, "*")
	f.Add(true, true, true, true, true, false, "job_x")
	f.Add(false, false, false, false, false, false, "")

	f.Fuzz(func(t *testing.T, closing, stillRegistered, sessionTarget, targetRunning, hasSend, oneShot bool, target string) {
		snap := progressTickSnapshot{
			closing:         closing,
			stillRegistered: stillRegistered,
			sessionTarget:   sessionTarget,
			targetRunning:   targetRunning,
			hasSend:         hasSend,
			oneShot:         oneShot,
			target:          target,
		}
		dec := decideProgressTick(snap)
		if dec2 := decideProgressTick(snap); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}
		if dec.sendDelivery && dec.recordBudget {
			t.Fatalf("send delivery and budget notification are mutually exclusive: %+v", dec)
		}
		if !dec.fire && dec.keepAlive {
			t.Fatalf("gated-out tick must not keep the goroutine alive: %+v", dec)
		}
		if stillRegistered && !closing && (sessionTarget || targetRunning) && !dec.fire {
			t.Fatalf("live tick must fire under the breaker policy (no suppression branch): %+v", dec)
		}
		if dec.fire {
			if dec.keepAlive == oneShot {
				t.Fatalf("a fire keeps the goroutine alive unless it is a one-shot's last: %+v", dec)
			}
			if dec.sendDelivery == dec.recordBudget {
				t.Fatalf("a firing tick must route exactly one way: %+v", dec)
			}
		}
		// A budget-counted notification for a session target carries no job id;
		// for a concrete target it echoes the target.
		if dec.recordBudget {
			if sessionTarget && dec.notifyJobID != "" {
				t.Fatalf("session-target notification must have empty job id: %+v", dec)
			}
			if !sessionTarget && dec.notifyJobID != target {
				t.Fatalf("concrete-target notification must echo target %q: %+v", target, dec)
			}
		}
	})
}
