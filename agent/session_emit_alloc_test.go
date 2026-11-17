package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provenance"
)

// The emit path is how a session reports progress to every consumer, and a
// watch-driven turn — a delegate update, a notification wake — stamps its
// provenance onto every event it emits. #6413 measured that stamp at 6
// allocations and 577 B per event, on top of an emit path that allocates nothing
// when there is no provenance.
//
// These two tests pin the two halves of the fix that must both hold: the cost
// per event (a budget, not a duration, so it cannot flake on a loaded host) and
// the exact envelopes the stream carries (so a cheaper stamp can never change
// what a subscriber receives).

// emitAllocationBudget is the allocations one emitted event may spend on the
// watch-provenance stamp.
//
// One independently-owned stamp costs a Causal and one array per non-empty
// slice: the Causal itself, its WatchKeys, its Chain and its FireKeys. Nothing
// on the emit path may copy the stamp a second time — that second copy is what
// #6413 measured and what the budget forbids.
const emitAllocationBudget = 4

// TestEmitWithWatchProvenanceAllocationBudget is the measurement #6413 asks
// for: allocations per emitted event on a watch-driven turn.
//
// Not parallel: testing.AllocsPerRun counts every allocation in the process, so
// a concurrent test in this package would perturb the count.
func TestEmitWithWatchProvenanceAllocationBudget(t *testing.T) {
	watchSession := &Session{id: "session_alloc_watch", events: make(chan events.SessionEvent, 1)}
	watchSession.replaceActiveProvenance(provenance.WithWatch(nil, "watch_alloc", "wg_1", "wd_1", "session_parent", "caller"))
	plainSession := &Session{id: "session_alloc_plain", events: make(chan events.SessionEvent, 1)}

	// The payload's interface conversion is hoisted out of the measured region:
	// a caller boxed the payload, and the interface is reused for every emit, so
	// what the measurement reads is the emit path's own work.
	var payload events.EventData = events.AssistantTextDeltaData{Delta: "x"}

	watchAllocs := testing.AllocsPerRun(100, func() {
		watchSession.emit(payload)
		<-watchSession.events
	})
	t.Logf("emit under a watch provenance: %v allocations per event", watchAllocs)
	if watchAllocs > emitAllocationBudget {
		t.Fatalf("emit under a watch provenance allocates %v objects per event, budget %d: "+
			"the event's provenance stamp is being copied more than once per event", watchAllocs, emitAllocationBudget)
	}

	// The no-provenance path is the regression guard the audit named: it
	// allocates nothing, and it must stay that way.
	plainAllocs := testing.AllocsPerRun(100, func() {
		plainSession.emit(payload)
		<-plainSession.events
	})
	t.Logf("emit without provenance: %v allocations per event", plainAllocs)
	if plainAllocs != 0 {
		t.Fatalf("emit without provenance allocates %v objects per event, want 0", plainAllocs)
	}
}

// TestEmitStreamCarriesIdenticalEnvelopes pins the emission contract #6413 must
// not disturb: the same events, in the same order, with the same payloads and
// the same provenance content.
//
// It exercises every shape the fix touches — a plain emit, two emits under one
// watch stamp, a stamp widened mid-turn by steering, a stamp reset by fresh
// input, the diagnostic path that sends without the job fan-out, and a direct
// sendEvent — and asserts the ordered envelopes that arrive. It passed before
// the change and must pass after it unchanged.
func TestEmitStreamCarriesIdenticalEnvelopes(t *testing.T) {
	t.Parallel()
	s := &Session{id: "session_contract", events: make(chan events.SessionEvent, 16)}

	s.emit(events.AssistantTextStartData{Model: "model-1"})
	s.replaceActiveProvenance(provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_parent", "caller"))
	s.emit(events.AssistantTextDeltaData{Delta: "a"})
	s.emit(events.AssistantTextDeltaData{Delta: "b"})
	s.unionActiveProvenance(provenance.WithWatch(nil, "watch_B", "wg_2", "wd_2", "session_parent", "caller"))
	s.emit(events.CommunicateData{CallID: "call_1", Message: "hello", EndTurn: false})
	s.replaceActiveProvenance(nil)
	s.emit(events.AssistantTextEndData{Text: "ab", FinishReason: "stop", Model: "model-1"})
	watchC := provenance.WithWatch(nil, "watch_C", "wg_3", "wd_3", "session_parent", "caller")
	s.replaceActiveProvenance(watchC)
	s.emitDiagnosticWarning(events.WarningData{Message: "diagnostic"})
	s.emitWithProvenance(events.AssistantTextDeltaData{Delta: "c"}, nil)
	s.sendEvent(events.AssistantTextDeltaData{Delta: "d"}, watchC)

	watchA := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_parent", "caller")
	watchAB := provenance.Union(watchA, provenance.WithWatch(nil, "watch_B", "wg_2", "wd_2", "session_parent", "caller"))
	cases := []struct {
		name string
		kind events.EventKind
		data events.EventData
		prov *provenance.Causal
	}{
		{name: "plain emit", kind: events.EventAssistantTextStart, data: events.AssistantTextStartData{Model: "model-1"}},
		{name: "watch emit one", kind: events.EventAssistantTextDelta, data: events.AssistantTextDeltaData{Delta: "a"}, prov: watchA},
		{name: "watch emit two", kind: events.EventAssistantTextDelta, data: events.AssistantTextDeltaData{Delta: "b"}, prov: watchA},
		{name: "widened stamp", kind: events.EventCommunicate, data: events.CommunicateData{CallID: "call_1", Message: "hello", EndTurn: false}, prov: watchAB},
		{name: "reset stamp", kind: events.EventAssistantTextEnd, data: events.AssistantTextEndData{Text: "ab", FinishReason: "stop", Model: "model-1"}},
		{name: "diagnostic send", kind: events.EventWarning, prov: provenance.WithWatch(nil, "watch_C", "wg_3", "wd_3", "session_parent", "caller")},
		{name: "explicit nil stamp", kind: events.EventAssistantTextDelta, data: events.AssistantTextDeltaData{Delta: "c"}},
		{name: "direct send", kind: events.EventAssistantTextDelta, data: events.AssistantTextDeltaData{Delta: "d"}, prov: watchC},
	}
	for i, tc := range cases {
		ev := <-s.events
		if got := events.KindOf(ev.Data); got != tc.kind || ev.Kind != tc.kind {
			t.Fatalf("event %d (%s) kind = %q (payload %q), want %q", i, tc.name, ev.Kind, got, tc.kind)
		}
		if ev.SessionID != s.id {
			t.Fatalf("event %d (%s) session = %q, want %q", i, tc.name, ev.SessionID, s.id)
		}
		if ev.Timestamp.IsZero() {
			t.Fatalf("event %d (%s) carries no timestamp", i, tc.name)
		}
		if tc.data != nil && !reflect.DeepEqual(ev.Data, tc.data) {
			t.Fatalf("event %d (%s) payload = %#v, want %#v", i, tc.name, ev.Data, tc.data)
		}
		if !reflect.DeepEqual(ev.Provenance, tc.prov) {
			t.Fatalf("event %d (%s) provenance = %#v, want %#v", i, tc.name, ev.Provenance, tc.prov)
		}
	}
	select {
	case extra := <-s.events:
		t.Fatalf("stream carried an extra event %q beyond the emitted sequence", extra.Kind)
	default:
	}

	// Two events stamped from one active set must not share storage: a consumer
	// that annotates or rewrites one envelope's stamp may not reach the other's.
	// This is the aliasing hazard of emitting without a per-event copy, so it is
	// asserted structurally rather than assumed.
	aliasSession := &Session{id: "session_contract_alias", events: make(chan events.SessionEvent, 2)}
	aliasSession.replaceActiveProvenance(provenance.WithWatch(nil, "watch_D", "wg_4", "wd_4", "session_parent", "caller"))
	aliasSession.emit(events.AssistantTextDeltaData{Delta: "one"})
	aliasSession.emit(events.AssistantTextDeltaData{Delta: "two"})
	first, second := <-aliasSession.events, <-aliasSession.events
	if first.Provenance == nil || second.Provenance == nil {
		t.Fatalf("aliasing fixture lost its stamp: first=%#v second=%#v", first.Provenance, second.Provenance)
	}
	if first.Provenance == second.Provenance {
		t.Fatal("two events from one active set share one provenance pointer; " +
			"a consumer that rewrites one envelope would rewrite the other")
	}
	first.Provenance.WatchKeys[0].WatchID = "mutated"
	if !provenance.ContainsWatch(second.Provenance, "watch_D", "wg_4") {
		t.Fatalf("mutating the first event's stamp changed the second's: %#v", second.Provenance)
	}

	// The stamp is read, never consumed: the session's own active set still
	// names its watch after every emit above, and the two delta events share one
	// stamp value even though they are separate events.
	if active := s.activeCausalProvenance(); !reflect.DeepEqual(active, watchC) {
		t.Fatalf("active provenance after emitting = %#v, want %#v", active, watchC)
	}
}
