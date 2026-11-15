package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// subscriptionAdmissionMethods covers both read aliases, unsubscribe and
// non-admitted methods. Reads decode ThreadReadParams, unsubscribe decodes
// ThreadUnsubscribeParams, and pages must never open a subscription admission.
var subscriptionAdmissionMethods = []string{
	appwire.MethodThreadRead,
	appwire.MethodThreadUnsubscribe,
	"totally/unknown",
	appwire.MethodThreadHistoryRead,
	appwire.MethodThreadTurnsList,
	appwire.MethodThreadHistoryTurnsList,
}

// FuzzDecodeSubscriptionAdmissionParams drives the real
// decodeSubscriptionAdmissionParams seam: the wire decode NewServer's
// SubscriptionAdmissionResolver runs on thread reads and unsubscribes,
// using client-supplied params before any admission
// decision is made against live server state. The oracle is floor "no panic";
// a decoded params value is also re-marshaled to catch a decode that produces
// something the wire codec cannot round-trip.
func FuzzDecodeSubscriptionAdmissionParams(f *testing.F) {
	f.Add(0, []byte(`{"threadId":"t1","subscribe":true}`))
	f.Add(1, []byte(`{"threadId":"t1","ref":"local:t1"}`))
	f.Add(0, []byte(`{"threadId":"t1","subscribe":false}`))
	f.Add(2, []byte(`{}`))
	f.Add(0, []byte(`not json`))
	f.Add(1, []byte(`null`))
	f.Add(3, []byte(`{"threadId":"t1","subscribe":true}`))
	f.Add(3, []byte(`{"threadId":"t1","subscribe":false}`))
	f.Add(3, []byte(`not json`))
	f.Add(4, []byte(`{"threadId":"t1","subscribe":true}`))
	f.Add(5, []byte(`{"threadId":"t1","subscribe":true}`))

	f.Fuzz(func(t *testing.T, methodIdx int, params []byte) {
		idx := methodIdx % len(subscriptionAdmissionMethods)
		if idx < 0 {
			idx += len(subscriptionAdmissionMethods)
		}
		msg := appwire.Message{Request: &appwire.Request{
			Method: subscriptionAdmissionMethods[idx],
			Params: params,
		}}
		decoded, ok, _ := decodeSubscriptionAdmissionParams(msg)
		if !ok {
			return
		}
		if _, err := json.Marshal(decoded); err != nil {
			t.Fatalf("decoded params failed to marshal: %v\n method=%q params=%q", err, subscriptionAdmissionMethods[idx], params)
		}
	})
}
