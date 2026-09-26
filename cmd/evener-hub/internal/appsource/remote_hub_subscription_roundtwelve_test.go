package appsource

import (
	"encoding/json"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The read path masks a remote thread's capability set (fromRemoteThread), so a
// client never sees an action this controller cannot forward. A notification is
// the other half of that same answer: a status frame refreshes the very set the
// read answered with, so a frame carrying the remote daemon's own flags would
// re-enable Send/Steer/Interrupt on a thread whose mutations still fail closed.
func TestRemoteHubStatusNotificationMasksUnforwardedCapabilities(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("unexpected method %q", method)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "S",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:S"},
		}}}
	})
	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	// The subscription's first frame is the source's own leading resync.
	if first, ok := recvNotification(t, out); !ok || first.Method != appwire.NotifyEvenerThreadResync {
		t.Fatalf("first notification = %+v, want the leading resync", first)
	}

	daemonSet := allRemoteThreadCapabilities()
	if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID:     "S",
		Ref:          "local:S",
		Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Capabilities: &daemonSet,
	}); err != nil {
		t.Fatalf("push status: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before the status notification")
	}
	status := decodeNotificationParams[appwire.ThreadStatusChangedParams](t, n)
	if status.Capabilities == nil {
		t.Fatal("status capabilities = nil, want a masked set where the daemon sent one")
	}
	if *status.Capabilities != maskedRemoteThreadCapabilities {
		t.Fatalf("status capabilities = %+v, want %+v like the read path", *status.Capabilities, maskedRemoteThreadCapabilities)
	}
	if status.Ref != "host:S" || status.ThreadID != "S" {
		t.Fatalf("status = %+v, want ref host:S and threadId S", status)
	}
}

// A thread/started frame carries a whole Thread, whose nested evener.capabilities
// is the same answer the read's snapshot gave. It must be masked too, while every
// other field of the nested thread stays byte-for-byte.
func TestRemoteHubNestedThreadNotificationMasksUnforwardedCapabilities(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != appwire.MethodThreadRead {
			t.Errorf("unexpected method %q", method)
		}
		return scriptedReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:     "S",
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:S"},
		}}}
	})
	out, err := remote.source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if first, ok := recvNotification(t, out); !ok || first.Method != appwire.NotifyEvenerThreadResync {
		t.Fatalf("first notification = %+v, want the leading resync", first)
	}

	if err := remote.push(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: "S",
		Ref:      "local:S",
		Thread: appwire.Thread{
			ID:     "S",
			Source: "local",
			Evener: appwire.EvenerThread{
				Ref:          "local:S",
				ParentRef:    "local:P",
				InstanceID:   "xyz",
				Capabilities: allRemoteThreadCapabilities(),
			},
		},
	}); err != nil {
		t.Fatalf("push started: %v", err)
	}
	n, ok := recvNotification(t, out)
	if !ok {
		t.Fatal("channel closed before the started notification")
	}
	started := decodeNotificationParams[appwire.ThreadStartedParams](t, n)
	if started.Thread.Evener.Capabilities != maskedRemoteThreadCapabilities {
		t.Fatalf("nested capabilities = %+v, want %+v like the read path",
			started.Thread.Evener.Capabilities, maskedRemoteThreadCapabilities)
	}
	if started.Thread.Source != "host" || started.Thread.Evener.Ref != "host:S" ||
		started.Thread.Evener.ParentRef != "host:P" || started.Thread.Evener.InstanceID != "xyz" {
		t.Fatalf("nested thread = %+v, want refs translated and opaque fields untouched", started.Thread.Evener)
	}
}

// The capability mask is a filter on one field, not a re-mint of the payload: a
// notification's other fields — including ones this hub does not understand —
// must reach the client exactly as the remote sent them.
func TestRemoteHubNotificationMaskPreservesUnrelatedFields(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	daemonSet, err := json.Marshal(allRemoteThreadCapabilities())
	if err != nil {
		t.Fatalf("marshal capability set: %v", err)
	}
	params := []byte(fmt.Sprintf(
		`{"threadId":"S","ref":"local:S","status":{"type":"active"},"capabilities":%[1]s,`+
			`"future":{"kept":true},"thread":{"id":"S","source":"local",`+
			`"evener":{"ref":"local:S","parentRef":"local:P","instanceId":"inst-1","capabilities":%[1]s},`+
			`"turns":[{"transcriptRef":"local:child"}]}}`, daemonSet))

	translated, threadID, ok := source.translateNotification(appwire.Notification{
		Method: appwire.NotifyThreadStatusChanged,
		Params: params,
	})
	if !ok || threadID != "S" {
		t.Fatalf("translateNotification = (%+v, %q, %v), want thread S routed", translated, threadID, ok)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(translated.Params, &fields); err != nil {
		t.Fatalf("decode translated params %s: %v", translated.Params, err)
	}
	var statusCaps appwire.ThreadCapabilities
	if err := json.Unmarshal(fields["capabilities"], &statusCaps); err != nil {
		t.Fatalf("decode translated capabilities %s: %v", fields["capabilities"], err)
	}
	if statusCaps != maskedRemoteThreadCapabilities {
		t.Fatalf("status capabilities = %+v, want %+v", statusCaps, maskedRemoteThreadCapabilities)
	}
	if string(fields["status"]) != `{"type":"active"}` {
		t.Fatalf("status field = %s, want it untouched", fields["status"])
	}
	if string(fields["future"]) != `{"kept":true}` {
		t.Fatalf("unknown field = %s, want it passed through byte-for-byte", fields["future"])
	}
	if string(fields["ref"]) != `"host:S"` {
		t.Fatalf("ref = %s, want it translated to host:S", fields["ref"])
	}

	var thread struct {
		Source string `json:"source"`
		Evener struct {
			Ref          string                     `json:"ref"`
			ParentRef    string                     `json:"parentRef"`
			InstanceID   string                     `json:"instanceId"`
			Capabilities appwire.ThreadCapabilities `json:"capabilities"`
		} `json:"evener"`
		Turns []struct {
			TranscriptRef string `json:"transcriptRef"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(fields["thread"], &thread); err != nil {
		t.Fatalf("decode translated thread %s: %v", fields["thread"], err)
	}
	if thread.Evener.Capabilities != maskedRemoteThreadCapabilities {
		t.Fatalf("nested capabilities = %+v, want %+v", thread.Evener.Capabilities, maskedRemoteThreadCapabilities)
	}
	if thread.Source != "host" || thread.Evener.Ref != "host:S" || thread.Evener.ParentRef != "host:P" ||
		thread.Evener.InstanceID != "inst-1" {
		t.Fatalf("nested thread = %+v, want refs translated and opaque fields untouched", thread)
	}
	if len(thread.Turns) != 1 || thread.Turns[0].TranscriptRef != "local:child" {
		t.Fatalf("nested turns = %+v, want the opaque turn payload passed through", thread.Turns)
	}
}
