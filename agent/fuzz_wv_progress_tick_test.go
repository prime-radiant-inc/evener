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
//   - mutual exclusion: a coalesced tick never also fires;
//   - a tick that neither fires nor coalesces keeps the goroutine alive only to
//     retry a fired one-shot's end;
//   - a LIVE tick either fires (the breaker policy removed the condition
//     suppression branch: self-influence classifies at the fire site, never
//     gates here) or coalesces, unless it is retrying a fired one-shot's end,
//     which never fires again. Coalescing is the only way a live tick delivers
//     nothing, and it is exactly the quiet job-targeted tick: never a timer,
//     never a session target, never a target in transition, never the first
//     tick, and never with new output;
//   - a coalesced tick keeps its goroutine alive (it may never go inert) and
//     asks for no one-shot end;
//   - a fire routes exactly one way, and keeps the goroutine alive unless it is
//     a one-shot timer's single fire, which is also its last;
//   - a one-shot end is asked for exactly on a one-shot's fire and on every
//     retry tick, and never for a repeating timer.
func FuzzWvProgressTick(f *testing.F) {
	f.Add(false, true, false, true, true, false, false, false, false, false, int64(0), int64(0), "dlg_1")
	f.Add(false, true, true, false, false, true, false, false, false, false, int64(0), int64(0), "*")
	f.Add(true, true, true, true, true, false, false, false, false, false, int64(0), int64(0), "job_x")
	f.Add(false, true, true, false, false, true, true, false, false, false, int64(0), int64(0), "*")
	f.Add(false, false, false, false, false, false, false, false, false, false, int64(0), int64(0), "")
	// The coalescing shape: a quiet job-targeted tick that has already delivered.
	f.Add(false, true, false, true, false, false, false, false, true, false, int64(64), int64(64), "job_x")
	// The same quiet shape after its target vanished: the liveness gate answers
	// first, so the tick is gated out rather than coalesced.
	f.Add(false, true, false, false, false, false, false, false, true, false, int64(64), int64(64), "job_x")

	f.Fuzz(func(t *testing.T, closing, stillRegistered, sessionTarget, targetRunning, hasSend, oneShot, firedPendingEnd, timer, tickFired, targetTransitioning bool, targetOutputBytes, lastTickOutputBytes int64, target string) {
		snap := progressTickSnapshot{
			closing:             closing,
			stillRegistered:     stillRegistered,
			sessionTarget:       sessionTarget,
			targetRunning:       targetRunning,
			hasSend:             hasSend,
			oneShot:             oneShot,
			firedPendingEnd:     firedPendingEnd,
			timer:               timer,
			tickFired:           tickFired,
			targetTransitioning: targetTransitioning,
			targetOutputBytes:   targetOutputBytes,
			lastTickOutputBytes: lastTickOutputBytes,
			target:              target,
		}
		dec := decideProgressTick(snap)
		if dec2 := decideProgressTick(snap); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}
		if dec.sendDelivery && dec.recordBudget {
			t.Fatalf("send delivery and budget notification are mutually exclusive: %+v", dec)
		}
		if dec.fire && dec.coalesce {
			t.Fatalf("a tick cannot both fire and coalesce: %+v", dec)
		}
		retrying := stillRegistered && !closing && firedPendingEnd
		if !dec.fire && !dec.coalesce && dec.keepAlive != retrying {
			t.Fatalf("only a fired one-shot's end retry keeps the goroutine alive without firing or coalescing: %+v", dec)
		}
		if stillRegistered && !closing && !retrying && (sessionTarget || targetRunning) && !dec.fire && !dec.coalesce {
			t.Fatalf("live tick must fire or coalesce: %+v", dec)
		}
		// Coalescing is licensed ONLY by the quiet job-targeted shape. Anything
		// else that coalesced would be the #499 failure returning: a tick that had
		// something to say and went silent anyway. targetRunning is part of the
		// license rather than a detail of the code: a tick whose concrete target is
		// gone is gated out by the liveness check, which runs before the coalescing
		// gate — the watch has no target left to report on, so it neither coalesces
		// nor fires, and this oracle must not license a decision the code never
		// makes.
		wantCoalesce := stillRegistered && !closing && !retrying &&
			targetRunning && !timer && !sessionTarget && !targetTransitioning && tickFired &&
			targetOutputBytes <= lastTickOutputBytes
		if dec.coalesce != wantCoalesce {
			t.Fatalf("coalesce = %t, want %t for this snapshot: %+v", dec.coalesce, wantCoalesce, snap)
		}
		if dec.coalesce {
			if !dec.keepAlive {
				t.Fatalf("a coalesced tick must keep its goroutine alive: %+v", dec)
			}
			if dec.sendDelivery || dec.recordBudget || dec.endOneShot || dec.notifyJobID != "" {
				t.Fatalf("a coalesced tick must deliver nothing and end nothing: %+v", dec)
			}
		}
		if retrying && (dec.fire || !dec.endOneShot) {
			t.Fatalf("an end retry must ask for the end and deliver nothing: %+v", dec)
		}
		if dec.fire {
			if dec.keepAlive == oneShot {
				t.Fatalf("a fire keeps the goroutine alive unless it is a one-shot's last: %+v", dec)
			}
			if dec.sendDelivery == dec.recordBudget {
				t.Fatalf("a firing tick must route exactly one way: %+v", dec)
			}
			if dec.endOneShot != oneShot {
				t.Fatalf("only a one-shot's fire ends the watch: %+v", dec)
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
