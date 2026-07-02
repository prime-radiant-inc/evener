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
//   - suppression keeps the goroutine alive but never fires;
//   - a fire always keeps the goroutine alive and routes exactly one way.
func FuzzWvProgressTick(f *testing.F) {
	f.Add(false, true, false, true, false, true, "dlg_1")
	f.Add(false, true, true, false, false, false, "*")
	f.Add(false, true, false, true, true, false, "dlg_2")
	f.Add(true, true, true, true, false, true, "job_x")
	f.Add(false, false, false, false, false, false, "")

	f.Fuzz(func(t *testing.T, closing, stillRegistered, sessionTarget, targetRunning, suppressed, hasSend bool, target string) {
		snap := progressTickSnapshot{
			closing:         closing,
			stillRegistered: stillRegistered,
			sessionTarget:   sessionTarget,
			targetRunning:   targetRunning,
			suppressed:      suppressed,
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
		if suppressed && stillRegistered && !closing && (sessionTarget || targetRunning) {
			if !dec.keepAlive || dec.fire {
				t.Fatalf("suppressed live tick must keep alive without firing: %+v", dec)
			}
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
