package appwire

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":7,"method":"thread/list","params":{"limit":25}}`)
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
	raw := []byte(`{"jsonrpc":"2.0","method":"thread/status/changed","params":{"threadId":"th_1"}}`)
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
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if decoded.JSONRPC != "2.0" || decoded.ID != 3 {
		t.Fatalf("decoded envelope=%+v", decoded)
	}
	if decoded.Error.Code != CodeInvalidParams {
		t.Fatalf("code=%d, want %d", decoded.Error.Code, CodeInvalidParams)
	}
}
