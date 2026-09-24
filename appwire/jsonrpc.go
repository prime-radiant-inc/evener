package appwire

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"strconv"

	"primeradiant.com/evener/invariant"
)

var unmarshalMessageFrame = json.Unmarshal

type MessageKind int

const (
	MessageInvalid MessageKind = iota
	MessageRequest
	MessageNotification
	MessageResponse
	MessageError
)

type ID struct {
	raw json.RawMessage
}

func NewIntID(v int64) ID {
	return ID{raw: json.RawMessage(strconv.FormatInt(v, 10))}
}

func (id ID) MarshalJSON() ([]byte, error) {
	if len(id.raw) == 0 {
		return []byte("null"), nil
	}
	return id.raw, nil
}

// IsZero reports an id that was never set, which omitzero frames leave out.
func (id ID) IsZero() bool { return len(id.raw) == 0 }

func (id *ID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return errors.New("request id must not be null")
	}
	id.raw = append(id.raw[:0], data...)
	// A successful decode rejected empty and null above, so raw now holds the
	// id token. Int64/String and the marshal-omits-empty-id logic all depend on
	// raw being non-empty for a decoded id.
	invariant.Hold(len(id.raw) > 0, "appwire: ID.UnmarshalJSON left raw empty after accepting %q", data)
	return nil
}

func (id ID) Int64() int64 {
	var n int64
	_ = json.Unmarshal(id.raw, &n)
	return n
}

func (id ID) String() string {
	var s string
	if err := json.Unmarshal(id.raw, &s); err == nil {
		return s
	}
	return string(id.raw)
}

type Request struct {
	ID     ID              `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response and ErrorResponse omit an empty id (omitzero, via ID.IsZero) so an
// id-less frame received off the wire round-trips faithfully: ID.MarshalJSON
// would otherwise render it as `null`, which ID.UnmarshalJSON rejects.
type Response struct {
	ID     ID  `json:"id,omitzero"`
	Result any `json:"result"`
}

type ErrorResponse struct {
	ID    ID        `json:"id,omitzero"`
	Error WireError `json:"error"`
}

type Message struct {
	Request      *Request
	Notification *Notification
	Response     *Response
	Error        *ErrorResponse
}

func (m Message) Kind() MessageKind {
	switch {
	case m.Request != nil:
		return MessageRequest
	case m.Notification != nil:
		return MessageNotification
	case m.Response != nil:
		return MessageResponse
	case m.Error != nil:
		return MessageError
	default:
		return MessageInvalid
	}
}

func (m Message) IDString() string {
	switch {
	case m.Request != nil:
		return m.Request.ID.String()
	case m.Response != nil:
		return m.Response.ID.String()
	case m.Error != nil:
		return m.Error.ID.String()
	default:
		return ""
	}
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var probe struct {
		JSONRPC *json.RawMessage `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Method  string           `json:"method"`
		Result  json.RawMessage  `json:"result"`
		Error   json.RawMessage  `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.JSONRPC != nil {
		return errors.New("jsonrpc field is not part of AppWire")
	}
	// A frame decoded into a reused Message must replace whatever it held, or a
	// stale pointer survives alongside the new one and Kind() picks the wrong one.
	*m = Message{}
	switch {
	case len(probe.Error) > 0:
		var resp ErrorResponse
		if err := unmarshalMessageFrame(data, &resp); err != nil {
			return err
		}
		m.Error = &resp
	case len(probe.Result) > 0:
		var resp Response
		if err := unmarshalMessageFrame(data, &resp); err != nil {
			return err
		}
		m.Response = &resp
	case probe.Method != "" && probe.ID != nil:
		var req Request
		if err := unmarshalMessageFrame(data, &req); err != nil {
			return err
		}
		m.Request = &req
	case probe.Method != "":
		var notif Notification
		if err := unmarshalMessageFrame(data, &notif); err != nil {
			return err
		}
		m.Notification = &notif
	default:
		return errors.New("invalid JSON-RPC message")
	}
	// Every non-error branch above populates exactly one frame pointer, so a
	// cleanly decoded Message has a single populated field and Kind() resolves it
	// unambiguously.
	if invariant.Enabled {
		set := 0
		if m.Request != nil {
			set++
		}
		if m.Notification != nil {
			set++
		}
		if m.Response != nil {
			set++
		}
		if m.Error != nil {
			set++
		}
		invariant.Hold(set == 1, "appwire: decoded Message has %d populated frames, want exactly 1", set)
	}
	return nil
}

// MarshalJSONTo writes the frame straight into the enclosing encoder. A
// MarshalJSON here would hand back separately encoded bytes that the caller
// then copies and re-validates, a second pass over every thread snapshot the
// daemon sends.
func (m Message) MarshalJSONTo(enc *jsontext.Encoder) error {
	switch {
	case m.Request != nil:
		return jsonv2.MarshalEncode(enc, m.Request)
	case m.Notification != nil:
		return jsonv2.MarshalEncode(enc, m.Notification)
	case m.Response != nil:
		return jsonv2.MarshalEncode(enc, m.Response)
	case m.Error != nil:
		return jsonv2.MarshalEncode(enc, m.Error)
	default:
		return errors.New("invalid JSON-RPC message")
	}
}

func RequestMessage(id ID, method string, params any) Message {
	return Message{Request: &Request{ID: id, Method: method, Params: mustRaw(params)}}
}

func NotificationMessage(method string, params any) Message {
	return Message{Notification: &Notification{Method: method, Params: mustRaw(params)}}
}

func ResponseMessage(id ID, result any) Message {
	return Message{Response: &Response{ID: id, Result: result}}
}

func ErrorMessage(id ID, err WireError) Message {
	return Message{Error: &ErrorResponse{ID: id, Error: err}}
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
