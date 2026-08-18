//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provenance"
)

// FuzzWxEvaluateWatchEvent drives evaluateWatchEvent — the pure decision core
// lifted out of onSessionEvent — with adversarial watch snapshots and events,
// asserting the invariants that the effectful onSessionEvent wrapper relies on.
// Oracles (beyond never-panic):
//   - determinism: the same (snapshot, event) yields the same decision;
//   - inactive target never matches or sends;
//   - a send decision implies the watch both matched AND has a send target;
//   - eventCount only ever holds or advances by one (throttle accounting).
func FuzzWxEvaluateWatchEvent(f *testing.F) {
	kinds := []events.EventKind{
		events.EventJobStarted, events.EventJobFinished, events.EventContextCompaction,
		events.EventError, events.EventCommunicate,
	}
	f.Add("job_1", true, false, uint8(1), "job_1", true, "dlg_2", 0, 0, uint8(2), "hi")
	f.Add("*", true, true, uint8(2), "sess_x", false, "", 3, 5, uint8(0), "")
	f.Add("", false, false, uint8(0), "w", false, "", 0, 0, uint8(1), "x")

	f.Fuzz(func(t *testing.T, target string, active, wildcard bool, kindSel uint8,
		watchID string, hasSend bool, sendTo string, triggerEvery, eventCount int,
		triggerSel uint8, dataText string) {

		kind := kinds[int(kindSel)%len(kinds)]
		snap := watchEventSnapshot{
			target:         target,
			targetActive:   active,
			wildcardEvents: wildcard,
			eventKinds:     map[events.EventKind]bool{kind: true},
			watchID:        watchID,
			generation:     "g1",
			triggerKind:    kinds[int(triggerSel)%len(kinds)],
			triggerEvery:   triggerEvery,
			eventCount:     eventCount,
			hasSend:        hasSend,
			sendTo:         sendTo,
		}
		ev := events.SessionEvent{Kind: kind, Data: events.JobStartedData{JobID: dataText}}

		dec := evaluateWatchEvent(snap, ev)
		if dec2 := evaluateWatchEvent(snap, ev); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}
		if !active && (dec.matched || dec.send) {
			t.Fatalf("inactive target must not match/send: %+v", dec)
		}
		if dec.send && !dec.matched {
			t.Fatalf("send implies matched: %+v", dec)
		}
		if dec.send && !snap.hasSend {
			t.Fatalf("send decision without a send target: %+v", dec)
		}
		if dec.eventCount < eventCount || dec.eventCount > eventCount+1 {
			t.Fatalf("eventCount %d out of [%d,%d]", dec.eventCount, eventCount, eventCount+1)
		}

		// Inform+breaker invariant: the routing decision is provenance-
		// INDEPENDENT. An event carrying this watch's own (watch_id,
		// generation) key must decide identically to the bare event — the echo
		// is delivered and classified at the observation site, never gated
		// here (the runaway fuse in recordWatchSend bounds the loop).
		selfEv := ev
		selfEv.Provenance = provenance.WithWatch(nil, snap.watchID, snap.generation, "wd_fuzz", "sess_fuzz", snap.target)
		if selfDec := evaluateWatchEvent(snap, selfEv); selfDec != dec {
			t.Fatalf("self-influenced provenance changed the decision: %+v vs %+v", selfDec, dec)
		}
	})
}
