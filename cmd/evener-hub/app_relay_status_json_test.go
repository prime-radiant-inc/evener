package hub

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

// The hub stamps four capability answers onto every relayed status frame it
// forwards for a local thread: the closed-thread set, forkFromTurn, pageBefore
// and voiceArm. Those answers are the frame's wire contract, and they must not
// move when the hub stops making them one JSON pass at a time.
//
// The want values below are byte-for-byte what a subscriber received from the
// four-stamper implementation (the fan-out's sequence at f91b487b28): every
// stamper re-encoded the params map, so the object's keys come out sorted and
// its values compact. A frame the hub has nothing to add is passed through as
// the daemon sent it, so those rows keep their original spacing.
func TestRelayedStatusFrameParamsAreTheSameJSONAsBeforeTheOnePassStamp(t *testing.T) {
	thread := appwire.Thread{
		ID:        "status-json",
		SessionID: "status-json",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:status-json"},
	}
	deliveries := make(chan appsource.RelayDelivery, 1)
	acknowledged := make(chan struct{}, 1)
	handoff := &recordingRelayHandoff{
		committed: make(chan struct{}),
		aborted:   make(chan struct{}),
		onCommit:  func() {},
	}
	client := relayedNotificationClient(t, thread, deliveries, handoff)

	frames := []string{
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"},"capabilities":{"send":true,"compact":true,"futureAction":true},"somethingNewer":{"a":1}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"closed"}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"closed"},"capabilities":{"send":false}}`,
		`{"threadId":"status-json","capabilities":{"send":true}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":null,"capabilities":{"send":true}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":"not-an-object","capabilities":{"send":true}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"},"capabilities":false}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"},"capabilities":null}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"restartRequired"},"capabilities":{"send":true,"forkFromTurn":true}}`,
		// An open status with no capability set: the old path left this one
		// untouched (patchStatusCapabilities refuses to fabricate a set), so the
		// one-pass stamp must not add one either.
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"}}`,
	}
	want := []string{
		`{"capabilities":{"compact":true,"forkFromTurn":true,"futureAction":true,"pageBefore":true,"send":true,"voiceArm":false},"ref":"local:status-json","somethingNewer":{"a":1},"status":{"type":"active"},"threadId":"status-json"}`,
		`{"capabilities":{"changeModel":true,"changeVisionModel":true,"clear":true,"commandInput":true,"compact":true,"forkFromTurn":true,"goal":true,"interrupt":false,"pageBefore":true,"queue":true,"rename":true,"send":true,"sharedNotes":true,"shutdown":true,"skillInput":true,"steer":false,"voiceArm":false},"ref":"local:status-json","status":{"type":"closed"},"threadId":"status-json"}`,
		`{"capabilities":{"changeModel":true,"changeVisionModel":true,"clear":true,"commandInput":true,"compact":true,"forkFromTurn":true,"goal":true,"interrupt":false,"pageBefore":true,"queue":true,"rename":true,"send":true,"sharedNotes":true,"shutdown":true,"skillInput":true,"steer":false,"voiceArm":false},"ref":"local:status-json","status":{"type":"closed"},"threadId":"status-json"}`,
		`{"capabilities":{"forkFromTurn":true,"pageBefore":true,"send":true,"voiceArm":false},"threadId":"status-json"}`,
		`{"capabilities":{"forkFromTurn":true,"pageBefore":true,"send":true,"voiceArm":false},"ref":"local:status-json","status":null,"threadId":"status-json"}`,
		`{"threadId":"status-json","ref":"local:status-json","status":"not-an-object","capabilities":{"send":true}}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"},"capabilities":false}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"},"capabilities":null}`,
		`{"capabilities":{"forkFromTurn":false,"pageBefore":true,"send":true,"voiceArm":false},"ref":"local:status-json","status":{"type":"restartRequired"},"threadId":"status-json"}`,
		`{"threadId":"status-json","ref":"local:status-json","status":{"type":"active"}}`,
	}
	for i, params := range frames {
		deliveries <- appsource.RelayDelivery{
			Notification: appwire.Notification{
				Method: appwire.NotifyThreadStatusChanged,
				Params: json.RawMessage(params),
			},
			Acknowledge: func() { acknowledged <- struct{}{} },
		}
		var got appwire.Notification
		select {
		case got = <-client.Notifications():
		case <-time.After(10 * time.Second):
			t.Fatalf("frame %d: no notification", i)
		}
		<-acknowledged
		if got.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("frame %d: method = %q", i, got.Method)
		}
		if string(got.Params) != want[i] {
			t.Errorf("frame %d params changed\n got: %s\nwant: %s", i, got.Params, want[i])
		}
	}
}
