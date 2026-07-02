//go:build serffuzz

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
//   - a tick that does not keep the goroutine alive never fires;
//   - a LIVE tick always fires (the breaker policy removed the suppression
//     branch: self-influence classifies at the fire site, never gates here);
//   - a fire always keeps the goroutine alive and routes exactly one way.
func FuzzWvProgressTick(f *testing.F) {
	f.Add(false, true, false, true, true, "dlg_1")
	f.Add(false, true, true, false, false, "*")
	f.Add(true, true, true, true, true, "job_x")
	f.Add(false, false, false, false, false, "")

	f.Fuzz(func(t *testing.T, closing, stillRegistered, sessionTarget, targetRunning, hasSend bool, target string) {
		snap := progressTickSnapshot{
			closing:         closing,
			stillRegistered: stillRegistered,
			sessionTarget:   sessionTarget,
			targetRunning:   targetRunning,
			hasSend:         hasSend,
			target:          target,
		}
		dec := decideProgressTick(snap)
		if dec2 := decideProgressTick(snap); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}
		if dec.sendDelivery && dec.recordBudget {
			t.Fatalf("send delivery and budget notification are mutually exclusive: %+v", dec)
		}
		if !dec.keepAlive && dec.fire {
			t.Fatalf("dead tick must not fire: %+v", dec)
		}
		if stillRegistered && !closing && (sessionTarget || targetRunning) && !dec.fire {
			t.Fatalf("live tick must fire under the breaker policy (no suppression branch): %+v", dec)
		}
		if dec.fire {
			if !dec.keepAlive {
				t.Fatalf("fire implies keepAlive: %+v", dec)
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
