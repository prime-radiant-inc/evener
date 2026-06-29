package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
)

// handleMessageMethods covers the request methods the connection state machine
// branches on: ping (answered before the gate), initialize (flips the gate), and
// ordinary/unknown methods (gated until initialized, then routed to the router
// which returns MethodNotFound). Indexing keeps method selection cheap.
var handleMessageMethods = []string{
	appwire.MethodPing,
	appwire.MethodInitialize,
	appwire.MethodInitialized,
	"thread/list",
	"totally/unknown",
}

// FuzzHandleMessage drives the real appserver request-handling seam:
// Connection.HandleMessage routes a decoded AppWire message through the ping
// fast-path, the initialize gate, and the router's typed param decode
// (HandleTyped → json.Unmarshal into InitializeParams), returning a response
// message. The fuzzer feeds a request with a fuzzed method + fuzzed params
// bytes, optionally after a real initialize handshake (preInit) so both the
// gated ("initialize required") and routed (Dispatch / WireError) branches are
// reachable, and also exercises the notification path. The oracle is floor "no
// panic" plus that every response message re-serializes through the real wire
// codec (appwire.Message.MarshalJSON).
func FuzzHandleMessage(f *testing.F) {
	f.Add(1, []byte(`{"protocolVersion":"1","clientInfo":{"name":"x"}}`), false, false)
	f.Add(0, []byte(`{}`), true, false)
	f.Add(3, []byte(`{"includeArchived":true}`), true, false)
	f.Add(1, []byte(`not json`), false, false)
	f.Add(4, []byte(`{}`), true, true) // notification path
	f.Add(2, []byte(`null`), false, true)

	f.Fuzz(func(t *testing.T, methodIdx int, params []byte, preInit, asNotification bool) {
		srv := NewServer(ServerConfig{ServerName: "fuzz", Version: "0"})
		conn := srv.NewConnection("fuzz-conn")
		ctx := context.Background()

		if preInit {
			initReq := appwire.Request{ID: appwire.NewIntID(1), Method: appwire.MethodInitialize, Params: json.RawMessage(`{}`)}
			resp := conn.HandleMessage(ctx, appwire.Message{Request: &initReq})
			assertSerializable(t, resp, "initialize handshake")
		}

		idx := methodIdx % len(handleMessageMethods)
		if idx < 0 {
			idx += len(handleMessageMethods)
		}
		method := handleMessageMethods[idx]

		var msg appwire.Message
		if asNotification {
			msg = appwire.Message{Notification: &appwire.Notification{Method: method, Params: json.RawMessage(params)}}
		} else {
			req := appwire.Request{ID: appwire.NewIntID(2), Method: method, Params: json.RawMessage(params)}
			msg = appwire.Message{Request: &req}
		}

		resp := conn.HandleMessage(ctx, msg)
		assertSerializable(t, resp, "fuzzed message")
	})
}

func assertSerializable(t *testing.T, msg appwire.Message, where string) {
	t.Helper()
	if msg.Kind() == appwire.MessageInvalid {
		return // an empty/invalid message (e.g. notification ack) carries nothing to encode
	}
	if _, err := json.Marshal(msg); err != nil {
		t.Fatalf("%s: response message failed to marshal: %v", where, err)
	}
}
