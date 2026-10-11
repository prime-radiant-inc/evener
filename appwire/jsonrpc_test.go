package appwire

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	raw := []byte(`{"id":7,"method":"thread/list","params":{"limit":25}}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Kind() != MessageRequest {
		t.Fatalf("Kind=%v, want request", msg.Kind())
	}
	if msg.Request.ID.Int64() != 7 {
		t.Fatalf("id=%v, want 7", msg.Request.ID)
	}
	if msg.Request.Method != "thread/list" {
		t.Fatalf("method=%q", msg.Request.Method)
	}
	if string(msg.Request.Params) != `{"limit":25}` {
		t.Fatalf("params=%s", msg.Request.Params)
	}
}

func TestNotificationRoundTrip(t *testing.T) {
	raw := []byte(`{"method":"thread/status/changed","params":{"threadId":"th_1"}}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Kind() != MessageNotification {
		t.Fatalf("Kind=%v, want notification", msg.Kind())
	}
	if msg.Notification.Method != "thread/status/changed" {
		t.Fatalf("method=%q", msg.Notification.Method)
	}
}

func TestErrorResponseEncoding(t *testing.T) {
	msg := ErrorMessage(NewIntID(3), InvalidParams("threadId is required"))
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		ID    int64 `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if bytes.Contains(data, []byte(`"jsonrpc"`)) {
		t.Fatalf("encoded JSON-RPC-lite envelope included jsonrpc: %s", data)
	}
	if decoded.ID != 3 {
		t.Fatalf("decoded envelope=%+v", decoded)
	}
	if decoded.Error.Code != CodeInvalidParams {
		t.Fatalf("code=%d, want %d", decoded.Error.Code, CodeInvalidParams)
	}
}

// TestIDLessFrameRoundTrips pins the codec contract for response/error frames
// that arrive without an id: the decoder accepts them, and re-encoding omits the
// empty id (rather than emitting a `null` the decoder would reject) so the frame
// survives a decode→encode→decode round trip.
func TestIDLessFrameRoundTrips(t *testing.T) {
	for _, raw := range []string{`{"error":{}}`, `{"result":{}}`} {
		var m Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("%s: decode: %v", raw, err)
		}
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("%s: re-marshal: %v", raw, err)
		}
		if bytes.Contains(encoded, []byte(`"id"`)) {
			t.Fatalf("%s: re-encoded frame leaked an id: %s", raw, encoded)
		}
		var m2 Message
		if err := json.Unmarshal(encoded, &m2); err != nil {
			t.Fatalf("%s: re-encoded %s failed to re-decode: %v", raw, encoded, err)
		}
	}
}

func TestRejectsJSONRPCField(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"thread/list"}`)
	var msg Message
	if err := json.Unmarshal(raw, &msg); err == nil {
		t.Fatal("expected jsonrpc field to be rejected")
	}
}

// TestWireFramesEncodeInOnePass guards the daemon's reply path: a hub
// liveness probe's thread snapshot and root diagnostics are large, and a
// MarshalJSON on any wrapper around them hands encoding/json separately
// encoded bytes that it then copies and re-validates, a second pass over the
// whole payload per wrapper. The frame types write into the enclosing encoder
// (MarshalJSONTo) or are plain structs, so none of them may implement
// json.Marshaler.
func TestWireFramesEncodeInOnePass(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[Message](),
		reflect.TypeFor[Response](),
		reflect.TypeFor[ErrorResponse](),
		reflect.TypeFor[EvenerDiagnostics](),
	} {
		if typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType) {
			t.Errorf("%s implements json.Marshaler, so every frame carrying it is encoded twice", typ.Name())
		}
	}
	if _, ok := any(Message{}).(jsonv2.MarshalerTo); !ok {
		t.Error("Message must implement MarshalerTo to write its frame into the enclosing encoder")
	}
}

// TestFramesKeepEncodingJSONSemantics pins that a frame written through
// MarshalJSONTo encodes its payload exactly as encoding/json would on its own:
// nil slices as null, HTML-escaped strings, sorted map keys, and error data
// that embeds ErrorData kept flat, so evenerErrorInfo stays a top-level field
// of data where clients read it.
func TestFramesKeepEncodingJSONSemantics(t *testing.T) {
	type result struct {
		Nil  []string       `json:"nil"`
		HTML string         `json:"html"`
		Map  map[string]int `json:"map"`
	}
	in := result{HTML: "<a&b>", Map: map[string]int{"z": 1, "a": 2}}
	hostField := InvalidHostField("name", "bad name")
	lifecycle := LifecycleUnavailable("retiring")
	type errorFrame struct {
		ID    int       `json:"id"`
		Error WireError `json:"error"`
	}
	for name, tc := range map[string]struct {
		frame Message
		want  any
	}{
		"result": {ResponseMessage(NewIntID(3), in), struct {
			ID     int    `json:"id"`
			Result result `json:"result"`
		}{3, in}},
		"host field error": {Message{Error: &ErrorResponse{ID: NewIntID(4), Error: hostField}}, errorFrame{4, hostField}},
		"lifecycle error":  {Message{Error: &ErrorResponse{ID: NewIntID(4), Error: lifecycle}}, errorFrame{4, lifecycle}},
	} {
		framed, err := json.Marshal(tc.frame)
		if err != nil {
			t.Fatalf("%s: marshal frame: %v", name, err)
		}
		want, err := json.Marshal(tc.want)
		if err != nil {
			t.Fatalf("%s: marshal want: %v", name, err)
		}
		if !bytes.Equal(framed, want) {
			t.Errorf("%s: frame = %s, want %s", name, framed, want)
		}
	}
}
