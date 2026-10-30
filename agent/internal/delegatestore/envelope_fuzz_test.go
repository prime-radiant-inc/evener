//go:build evenerfuzz

package delegatestore

import (
	"testing"
)

// envelopePayload pairs a kind with a function that attaches its payload. The
// pairs are derived from Event's own payload fields (JSON name x belongs to
// "delegate_x"), not from eventKindSpecs, so this target stays an independent
// oracle: a table row bound to the wrong payload, or a row missing for a
// payload field, disagrees with the expectation below.
//
// A kind with no payload field is invisible to this enumeration;
// TestEventKindTableComplete owns that case. Order is Event field declaration
// order, which fixes what the f.Add seed indices below mean.
type envelopePayload struct {
	kind   EventKind
	attach func(*Event)
}

func envelopePayloads(tb testing.TB) []envelopePayload {
	tb.Helper()
	fields := eventPayloadFields(tb)
	payloads := make([]envelopePayload, 0, len(fields))
	for _, field := range fields {
		payloads = append(payloads, envelopePayload{
			kind: eventKindForField(field),
			attach: func(event *Event) {
				attachEventPayloadField(event, field)
			},
		})
	}
	return payloads
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
// Enumerating the cross product is the point. Every kind against every payload
// combination is far more than a table of hand-written cases covers, and the
// pairs are derived from Event's fields rather than from the table the
// validator walks, so a row whose payload binding is wrong or missing fails the
// derived expectation.
func FuzzDelegateEventEnvelope(f *testing.F) {
	payloads := envelopePayloads(f)
	f.Add(0, uint16(1), "dlg_1")     // created, matching payload
	f.Add(0, uint16(0), "dlg_1")     // created, no payload
	f.Add(0, uint16(3), "dlg_1")     // created, two payloads
	f.Add(1, uint16(1), "dlg_1")     // run started, created's payload
	f.Add(0, uint16(1), "")          // matching payload, no delegate id
	f.Add(99, uint16(1), "dlg_1")    // unknown kind
	f.Add(3, uint16(1023), "dlg_1")  // every payload at once
	f.Add(8, uint16(256), "dlg_1")   // attention changed, matching payload
	f.Add(9, uint16(512), "dlg_1")   // update posted, matching payload
	f.Add(10, uint16(1024), "dlg_1") // intent stated, matching payload

	f.Fuzz(func(t *testing.T, kindIndex int, payloadMask uint16, delegateID string) {
		if len(delegateID) > 256 {
			t.Skip()
		}

		event := Event{DelegateID: delegateID, Seq: 1}
		knownKind := kindIndex >= 0 && kindIndex < len(payloads)
		if knownKind {
			event.Kind = payloads[kindIndex].kind
		} else {
			event.Kind = EventKind("not-a-kind")
		}

		attached := 0
		matching := false
		for i, entry := range payloads {
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
