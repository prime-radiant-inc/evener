package appwire

import (
	"encoding/json"
	"testing"
)

// TestMessageKind covers every Kind() branch (jsonrpc.go:123-136).
func TestMessageKind(t *testing.T) {
	id := NewIntID(1)
	cases := []struct {
		name string
		msg  Message
		want MessageKind
	}{
		{"request", RequestMessage(id, "m", nil), MessageRequest},
		{"notification", NotificationMessage("m", nil), MessageNotification},
		{"response", ResponseMessage(id, "ok"), MessageResponse},
		{"error", ErrorMessage(id, InternalError("x")), MessageError},
		{"empty", Message{}, MessageInvalid},
	}
	for _, c := range cases {
		if got := c.msg.Kind(); got != c.want {
			t.Errorf("%s: Kind() = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestMessageIDString covers every IDString() branch (jsonrpc.go:138-149):
// request, response, and error frames return their id; a notification (and an
// empty message) returns "".
func TestMessageIDString(t *testing.T) {
	id := NewIntID(42)
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"request", RequestMessage(id, "m", nil), "42"},
		{"response", ResponseMessage(id, "ok"), "42"},
		{"error", ErrorMessage(id, InternalError("x")), "42"},
		{"notification", NotificationMessage("m", nil), ""},
		{"empty", Message{}, ""},
	}
	for _, c := range cases {
		if got := c.msg.IDString(); got != c.want {
			t.Errorf("%s: IDString() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestMessageUnmarshalDispatch covers each decode branch in
// Message.UnmarshalJSON (jsonrpc.go:165-192): error, result, request
// (method+id), notification (method only), and the invalid default.
func TestMessageUnmarshalDispatch(t *testing.T) {
	cases := []struct {
		name string
		data string
		want MessageKind
	}{
		{"error", `{"id":1,"error":{"code":-32603,"message":"boom"}}`, MessageError},
		{"response", `{"id":1,"result":"ok"}`, MessageResponse},
		{"request", `{"id":1,"method":"do"}`, MessageRequest},
		{"notification", `{"method":"ping"}`, MessageNotification},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m Message
			if err := json.Unmarshal([]byte(c.data), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if m.Kind() != c.want {
				t.Errorf("Kind() = %d, want %d", m.Kind(), c.want)
			}
		})
	}

	t.Run("invalid", func(t *testing.T) {
		var m Message
		if err := json.Unmarshal([]byte(`{"id":1}`), &m); err == nil {
			t.Fatal("decoding a frame with no method/result/error: want an error")
		}
	})
}

// TestMessageMarshalDispatch covers each branch of Message.MarshalJSON
// (jsonrpc.go:215-227): the four populated frame kinds round-trip and an empty
// Message is a marshal error.
func TestMessageMarshalDispatch(t *testing.T) {
	id := NewIntID(7)
	for _, m := range []Message{
		RequestMessage(id, "do", nil),
		NotificationMessage("ping", nil),
		ResponseMessage(id, "ok"),
		ErrorMessage(id, InternalError("boom")),
	} {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal %d: %v", m.Kind(), err)
		}
		var back Message
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("re-decode %d: %v", m.Kind(), err)
		}
		if back.Kind() != m.Kind() {
			t.Errorf("round-trip kind = %d, want %d", back.Kind(), m.Kind())
		}
	}

	if _, err := json.Marshal(Message{}); err == nil {
		t.Fatal("marshaling an empty Message: want an error")
	}
}

// TestRequestMessageParamsEncoded confirms RequestMessage/NotificationMessage
// encode params via mustRaw, and that a nil params stays absent.
func TestRequestMessageParamsEncoded(t *testing.T) {
	withParams := RequestMessage(NewIntID(1), "do", map[string]int{"n": 3})
	if string(withParams.Request.Params) != `{"n":3}` {
		t.Errorf("Params = %q, want {\"n\":3}", withParams.Request.Params)
	}
	if noParams := NotificationMessage("ping", nil); noParams.Notification.Params != nil {
		t.Errorf("nil params encoded to %q, want nil", noParams.Notification.Params)
	}
}
