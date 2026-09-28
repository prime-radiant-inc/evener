package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// subscriptionAdmissionMethods covers the two methods
// decodeSubscriptionAdmissionParams branches on, plus one it does not apply
// to, so the fuzzer exercises both admitted decode paths (thread/read into
// ThreadReadParams, thread/unsubscribe into ThreadUnsubscribeParams) and the
// early "not our method" refusal.
var subscriptionAdmissionMethods = []string{
	appwire.MethodThreadRead,
	appwire.MethodThreadUnsubscribe,
	"totally/unknown",
}

// FuzzDecodeSubscriptionAdmissionParams drives the real
// decodeSubscriptionAdmissionParams seam: the wire decode NewServer's
// SubscriptionAdmissionResolver runs on every thread/read and
// thread/unsubscribe request's client-supplied params, before any admission
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

	f.Fuzz(func(t *testing.T, methodIdx int, params []byte) {
		idx := methodIdx % len(subscriptionAdmissionMethods)
		if idx < 0 {
			idx += len(subscriptionAdmissionMethods)
		}
		msg := appwire.Message{Request: &appwire.Request{
			Method: subscriptionAdmissionMethods[idx],
			Params: params,
		}}
		decoded, ok := decodeSubscriptionAdmissionParams(msg)
		if !ok {
			return
		}
		if _, err := json.Marshal(decoded); err != nil {
			t.Fatalf("decoded params failed to marshal: %v\n method=%q params=%q", err, subscriptionAdmissionMethods[idx], params)
		}
	})
}
