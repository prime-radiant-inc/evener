package appwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
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

// TestResponseFrameEncodesItsResultOnce guards the daemon's reply path: a
// hub liveness probe's thread snapshot is large, and every JSON-RPC wrapper
// that marshals its result separately makes the encoder copy and re-validate
// the whole snapshot again. Framing a result must allocate about what encoding
// the result alone allocates, not a multiple of it.
func TestResponseFrameEncodesItsResultOnce(t *testing.T) {
	var result ThreadListResponse
	for i := range 50 {
		result.Data = append(result.Data, Thread{ID: fmt.Sprintf("thread-%d", i), Preview: "a preview line long enough to grow the encoder's buffer"})
	}
	bytesPerEncode := func(v any) uint64 {
		const runs = 20
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			if _, err := json.Marshal(v); err != nil {
				t.Fatalf("marshal: %v", err)
			}
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / runs
	}
	bare := bytesPerEncode(result)
	for name, frame := range map[string]Message{
		"with id":    ResponseMessage(NewIntID(7), result),
		"without id": {Response: &Response{Result: result}},
	} {
		if framed := bytesPerEncode(frame); framed > bare*3/2 {
			t.Errorf("%s: framing the result allocated %d bytes against %d for the result alone; the result is being re-encoded", name, framed, bare)
		}
	}
}
