//go:build evenerfuzz

package delegatestore

import (
	"testing"
)

// eventKinds is every kind validateEventEnvelope recognises, paired with the
// payload it demands. Order matters only in that the bitmask below indexes it.
var envelopeKinds = []struct {
	kind   EventKind
	attach func(*Event)
}{
	{EventDelegateCreated, func(e *Event) { e.Created = &DelegateCreated{} }},
	{EventDelegateRunStarted, func(e *Event) { e.RunStarted = &RunStarted{} }},
	{EventDelegateTerminalPrepared, func(e *Event) { e.TerminalPrepared = &TerminalPrepared{} }},
	{EventDelegateRunFinished, func(e *Event) { e.RunFinished = &RunFinished{} }},
	{EventDelegateResumabilityClosed, func(e *Event) { e.ResumabilityClosed = &ResumabilityClosed{} }},
	{EventDelegateSubtreeStopRequested, func(e *Event) { e.SubtreeStopRequested = &SubtreeStopRequested{} }},
	{EventDelegateSubtreeStopCompleted, func(e *Event) { e.SubtreeStopCompleted = &SubtreeStopCompleted{} }},
	{EventDelegateDeliveryAcknowledged, func(e *Event) { e.DeliveryAcknowledged = &DeliveryAcknowledged{} }},
}

// FuzzDelegateEventEnvelope drives the envelope rule over the cross product of
// every event kind against every combination of attached payloads.
//
// The delegate log is replayed to rebuild state after a restart, so an event
// whose payload does not match its kind is a corruption that folds into state
// as something that never happened — a run recorded as started because a
// RunStarted payload rode along on a differently-kinded event, say. The rule is
// deliberately strict: exactly one payload, and it must be the kind's own.
//
// Enumerating the cross product is the point. Eight kinds against 256 payload
// combinations is far more than a table of hand-written cases covers, and the
// switch in validateEventEnvelope is maintained by hand — a kind added to the
// enum but forgotten there is exactly what this finds.
func FuzzDelegateEventEnvelope(f *testing.F) {
	f.Add(0, uint8(1), "dlg_1")   // created, matching payload
	f.Add(0, uint8(0), "dlg_1")   // created, no payload
	f.Add(0, uint8(3), "dlg_1")   // created, two payloads
	f.Add(1, uint8(1), "dlg_1")   // run started, created's payload
	f.Add(0, uint8(1), "")        // matching payload, no delegate id
	f.Add(99, uint8(1), "dlg_1")  // unknown kind
	f.Add(3, uint8(255), "dlg_1") // every payload at once

	f.Fuzz(func(t *testing.T, kindIndex int, payloadMask uint8, delegateID string) {
		if len(delegateID) > 256 {
			t.Skip()
		}

		event := Event{DelegateID: delegateID, Seq: 1}
		knownKind := kindIndex >= 0 && kindIndex < len(envelopeKinds)
		if knownKind {
			event.Kind = envelopeKinds[kindIndex].kind
		} else {
			event.Kind = EventKind("not-a-kind")
		}

		attached := 0
		matching := false
		for i, entry := range envelopeKinds {
			if payloadMask&(1<<uint(i)) == 0 {
				continue
			}
			entry.attach(&event)
			attached++
			if knownKind && i == kindIndex {
				matching = true
			}
		}

		wantAccept := delegateID != "" && knownKind && attached == 1 && matching
		err := validateEventEnvelope(event)
		if wantAccept != (err == nil) {
			t.Fatalf("validateEventEnvelope(kind=%q id=%q payloads=%d matching=%v) = %v, want accept=%v",
				event.Kind, delegateID, attached, matching, err, wantAccept)
		}

		// Whatever the envelope refuses must never reach state. Fold is the
		// door the replay path actually goes through, so the guarantee has to
		// hold there and not only in the validator.
		if err != nil {
			if _, foldErr := Fold([]Event{event}); foldErr == nil {
				t.Fatalf("Fold accepted an event the envelope rejected: kind=%q payloads=%d", event.Kind, attached)
			}
		}
	})
}
