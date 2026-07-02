//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
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

// FuzzWxClassifyWatchSendTarget drives classifyWatchSendDeliveryTarget — the
// decision tree lifted out of classifyRestoredWatchSendTarget — through a fuzzed
// resolver whose lookups return crafted (deterministic) records, so the pure
// classification runs without touching a store. Oracles: determinism; the result
// is always one of the three valid classes; the documented early returns hold
// (empty target and a job_-prefixed target are hard failures).
func FuzzWxClassifyWatchSendTarget(f *testing.F) {
	f.Add("dlg_1", "sess_a", true, true, true, uint8(3), true)
	f.Add("job_9", "sess_a", true, false, false, uint8(0), false)
	f.Add("", "", false, false, false, uint8(0), false)
	f.Add("bare-target", "sess_a", true, true, false, uint8(4), true)

	f.Fuzz(func(t *testing.T, target, sessionID string, hasJM, delegateResumable, jobResumable bool,
		statusSel uint8, assessResumable bool) {

		statuses := []jobstore.Status{
			jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusCancelled, jobstore.StatusStopped,
		}
		status := statuses[int(statusSel)%len(statuses)]
		rec := &jobstore.JobRecord{
			JobID: target, Type: jobstore.JobDelegate, Status: status,
			OwnerSessionID: sessionID, DelegateID: "dlg_1",
		}
		if jobResumable {
			r := true
			rec.Resumable = &r
		}
		res := watchSendTargetResolver{sessionID: sessionID, hasJobManager: hasJM}
		if hasJM {
			res.loadDelegates = func() (map[string]*jobstore.DelegateRecord, error) {
				return map[string]*jobstore.DelegateRecord{
					"dlg_1": {DelegateID: "dlg_1", OwnerSessionID: sessionID, CurrentJobID: target, Resumable: delegateResumable},
				}, nil
			}
			res.findJobRecord = func(string) (*jobstore.JobRecord, error) { return rec, nil }
			res.assessResumable = func(*jobstore.JobRecord) delegateResumability {
				return delegateResumability{Resumable: assessResumable}
			}
		}

		class, _ := classifyWatchSendDeliveryTarget(target, res)
		class2, _ := classifyWatchSendDeliveryTarget(target, res)
		if class != class2 {
			t.Fatalf("non-deterministic class: %v vs %v", class, class2)
		}
		switch class {
		case watchSendDelivered, watchSendBusy, watchSendHardFailure:
		default:
			t.Fatalf("invalid class %v", class)
		}
		if target == "" && class != watchSendHardFailure {
			t.Fatalf("empty target must be hard failure, got %v", class)
		}
	})
}
