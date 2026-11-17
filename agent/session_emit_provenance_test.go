package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// sendEventContext adopts the provenance it is handed instead of copying it
// (#6413/#6619). Every caller that holds a stamp something else can read or
// mutate therefore passes provenance.Clone of it, and five emits under that
// rule hold the copy that keeps a live envelope from sharing storage with a
// descriptor, a jobstore event, or a recorded transcript turn. These tests own
// those five sites one apiece: each asserts the delivered envelope's stamp is a
// different pointer from the record the emit path was handed, and that mutating
// the delivered stamp leaves the record untouched. Deleting the site's Clone
// makes its own test fail and no other.

// storedProvenance is the shared-record stamp the emit paths below are handed:
// the kind of value a descriptor, a jobstore event or a recorded turn keeps
// holding after the emit returns.
func storedProvenance() *provenance.Causal {
	return provenance.WithWatch(nil, "watch_stored", "gen_stored", "delivery_stored", "session_parent", "caller")
}

// requireIndependentStamp fails unless delivered is a different pointer from
// held with the same content, and mutating delivered leaves held unchanged.
// held is the shared record's own stamp; delivered is the one the live envelope
// carries. It mutates delivered, so it must be the last check on that stamp.
func requireIndependentStamp(t *testing.T, delivered, held *provenance.Causal) {
	t.Helper()
	if held == nil || delivered == nil {
		t.Fatalf("stamp fixture lost its provenance: delivered=%#v held=%#v", delivered, held)
	}
	if len(held.WatchKeys) == 0 || len(held.Chain) == 0 {
		t.Fatalf("stamp fixture holds nothing to mutate: %#v", held)
	}
	if delivered == held {
		t.Fatal("the delivered envelope shares its provenance pointer with the record the emit path was handed; " +
			"a consumer that rewrites the envelope would rewrite the record")
	}
	if !reflect.DeepEqual(delivered, held) {
		t.Fatalf("delivered provenance = %#v, want a copy of %#v", delivered, held)
	}
	heldWatchID := held.WatchKeys[0].WatchID
	heldDeliveryID := held.Chain[0].DeliveryID
	delivered.WatchKeys[0].WatchID = heldWatchID + "_mutated"
	delivered.Chain[0].DeliveryID = heldDeliveryID + "_mutated"
	if held.WatchKeys[0].WatchID != heldWatchID || held.Chain[0].DeliveryID != heldDeliveryID {
		t.Fatalf("mutating the delivered stamp rewrote the record's: %#v", held)
	}
}

// takeEventOfKind drains the session's already-buffered stream and returns the
// first event of kind, skipping the activity invalidations an emit may also
// publish. Every emit these tests exercise is synchronous into a buffered
// channel, so nothing here waits.
func takeEventOfKind(t *testing.T, s *Session, kind events.EventKind) events.SessionEvent {
	t.Helper()
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == kind {
				return ev
			}
		default:
			t.Fatalf("the session's stream carried no %s event", kind)
			return events.SessionEvent{}
		}
	}
}

// takeOnlyEvent returns the single event a stream carries and fails when a
// second one followed it, so a fixture cannot quietly deliver more than the
// shape it claims.
func takeOnlyEvent(t *testing.T, s *Session, want events.EventKind) events.SessionEvent {
	t.Helper()
	ev := takeEventOfKind(t, s, want)
	select {
	case extra := <-s.Events():
		t.Fatalf("the stream carried a second event %q beyond %q", extra.Kind, want)
	default:
	}
	return ev
}

// delegateProvenanceEmitFixture is a two-level delegate tree whose grandchild
// row is owned by the live root runtime and has the middle subagent's live
// runtime as an ancestor: the shape where emitDelegateUpdate hands one
// descriptor provenance to two sessions' streams.
type delegateProvenanceEmitFixture struct {
	middle *Session
	root   *Session
	stored *provenance.Causal
}

func newDelegateProvenanceEmitFixture() delegateProvenanceEmitFixture {
	stored := storedProvenance()
	middle := &Session{id: "session-middle", events: make(chan events.SessionEvent, 4)}
	root := &Session{id: "session-root", events: make(chan events.SessionEvent, 4)}
	row := delegateSnapshot{
		id:        "dlg_grandchild",
		parentID:  "dlg_parent",
		lifecycle: delegateLifecycleIdle,
		phase:     delegatestore.PhaseClosed,
		resumable: true,
		revision:  3,
		descriptor: delegatestore.Descriptor{
			OwnerSessionID:   "session-root",
			ParentDelegateID: "dlg_parent",
			ChildSessionID:   "session-grandchild",
			Task:             "inspect the emit path's provenance",
			Provenance:       stored,
		},
	}
	controller := &delegateTreeController{
		rootSessionID: "session-root",
		rootRuntime:   root,
		durable: delegatestore.State{
			"dlg_parent": {
				DelegateID: "dlg_parent",
				Descriptor: delegatestore.Descriptor{OwnerSessionID: "session-root", ChildSessionID: "session-middle"},
			},
			"dlg_grandchild": {DelegateID: "dlg_grandchild", Descriptor: row.descriptor},
		},
		live: map[string]*delegateLiveState{"dlg_parent": {runtime: middle}},
	}
	emitter := &Session{id: "session-root", events: make(chan events.SessionEvent, 8), delegateController: controller}
	emitter.emitDelegateUpdate(delegateUpdatePlan{rows: []delegateSnapshot{row}})
	return delegateProvenanceEmitFixture{middle: middle, root: root, stored: stored}
}

// A grandchild row has a live ancestor subagent, and that ancestor's runtime is
// not the row's owner: delegate_bootstrap's ancestor loop hands it the
// descriptor's stamp, so it must take its own copy.
func TestDelegateUpdateWithALiveAncestorCopiesTheDescriptorProvenance(t *testing.T) {
	t.Parallel()
	fixture := newDelegateProvenanceEmitFixture()
	ev := takeOnlyEvent(t, fixture.middle, events.EventDelegateUpdated)
	requireIndependentStamp(t, ev.Provenance, fixture.stored)
}

// The row's owner runtime is the other delegate_bootstrap emit: the root thread
// carries the same descriptor stamp, and it must not share storage with the
// descriptor either.
func TestDelegateUpdateWithALiveOwnerCopiesTheDescriptorProvenance(t *testing.T) {
	t.Parallel()
	fixture := newDelegateProvenanceEmitFixture()
	ev := takeOnlyEvent(t, fixture.root, events.EventDelegateUpdated)
	requireIndependentStamp(t, ev.Provenance, fixture.stored)
}

// emitJobStarted announces a job with the jobstore event's provenance. That
// event outlives the announcement, so the envelope must carry its own copy.
func TestEmitJobStartedCopiesTheJobstoreProvenance(t *testing.T) {
	t.Parallel()
	session := &Session{id: "session_job_started_emit", events: make(chan events.SessionEvent, 4)}
	jm := &jobManager{emit: session.emitWithJobTreeRevision}
	stored := storedProvenance()
	jm.emitJobStarted(jobstore.Event{
		Kind:       jobstore.EventJobStarted,
		JobID:      "job_emit_started",
		Type:       jobstore.JobShell,
		Provenance: stored,
	}, nil)
	ev := takeEventOfKind(t, session, events.EventJobStarted)
	requireIndependentStamp(t, ev.Provenance, stored)
}

// emitJobFinished is the same contract at the job's other boundary, over the
// finished event's own provenance.
func TestEmitJobFinishedCopiesTheJobstoreProvenance(t *testing.T) {
	t.Parallel()
	session := &Session{id: "session_job_finished_emit", events: make(chan events.SessionEvent, 4)}
	jm := &jobManager{emit: session.emitWithJobTreeRevision}
	stored := storedProvenance()
	jm.emitJobFinished(jobstore.Event{
		Kind:       jobstore.EventJobFinished,
		JobID:      "job_emit_finished",
		Type:       jobstore.JobShell,
		Status:     jobstore.StatusCompleted,
		Provenance: stored,
	}, nil)
	ev := takeEventOfKind(t, session, events.EventJobFinished)
	requireIndependentStamp(t, ev.Provenance, stored)
}

// deliverCommunicate records its COMMUNICATE turn with the session's active
// causal provenance and then announces the same message live. The recorded turn
// retains that stamp for the life of the record, so the live envelope must carry
// its own copy: the recorded turn here is the one the transcript hook captured,
// which is the value the durable entry holds.
func TestDeliverCommunicateCopiesTheRecordedTurnProvenance(t *testing.T) {
	s, _ := newExecutionSession(t)
	s.replaceActiveProvenance(storedProvenance())
	var recorded *provenance.Causal
	s.SetTranscriptRecordedFunc(func(rec transcript.Record) {
		if rec.Turn.Kind == schema.TurnCommunicate {
			recorded = rec.Turn.Causal
		}
	})
	if err := s.deliverCommunicate(events.CommunicateData{CallID: "comm_provenance", Message: "result", EndTurn: true}); err != nil {
		t.Fatalf("deliverCommunicate: %v", err)
	}
	if recorded == nil {
		t.Fatal("the recorded communicate turn retained no causal provenance")
	}
	ev := takeEventOfKind(t, s, events.EventCommunicate)
	requireIndependentStamp(t, ev.Provenance, recorded)
}
