package appwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

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

func (id *ID) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return errors.New("request id must not be null")
	}
	id.raw = append(id.raw[:0], data...)
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

type Response struct {
	ID     ID  `json:"id"`
	Result any `json:"result"`
}

// MarshalJSON omits the id field when the id is empty so an id-less frame
// received off the wire round-trips faithfully. ID.MarshalJSON would otherwise
// render an empty id as `null`, which ID.UnmarshalJSON rejects — leaving the
// codec unable to re-read its own output. A frame built with a real id is
// unaffected (identical bytes and field order).
func (r Response) MarshalJSON() ([]byte, error) {
	if len(r.ID.raw) == 0 {
		return json.Marshal(struct {
			Result any `json:"result"`
		}{r.Result})
	}
	return json.Marshal(struct {
		ID     ID  `json:"id"`
		Result any `json:"result"`
	}{r.ID, r.Result})
}

type ErrorResponse struct {
	ID    ID        `json:"id"`
	Error WireError `json:"error"`
}

// MarshalJSON omits the id field when the id is empty, mirroring Response: an
// id-less error frame must round-trip rather than re-encode to an unreadable
// `null` id.
func (e ErrorResponse) MarshalJSON() ([]byte, error) {
	if len(e.ID.raw) == 0 {
		return json.Marshal(struct {
			Error WireError `json:"error"`
		}{e.Error})
	}
	return json.Marshal(struct {
		ID    ID        `json:"id"`
		Error WireError `json:"error"`
	}{e.ID, e.Error})
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
	switch {
	case len(probe.Error) > 0:
		var resp ErrorResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return err
		}
		m.Error = &resp
	case len(probe.Result) > 0:
		var resp Response
		if err := json.Unmarshal(data, &resp); err != nil {
			return err
		}
		m.Response = &resp
	case probe.Method != "" && probe.ID != nil:
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			return err
		}
		m.Request = &req
	case probe.Method != "":
		var notif Notification
		if err := json.Unmarshal(data, &notif); err != nil {
			return err
		}
		m.Notification = &notif
	default:
		return errors.New("invalid JSON-RPC message")
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	switch {
	case m.Request != nil:
		return json.Marshal(m.Request)
	case m.Notification != nil:
		return json.Marshal(m.Notification)
	case m.Response != nil:
		return json.Marshal(m.Response)
	case m.Error != nil:
		return json.Marshal(m.Error)
	default:
		return nil, errors.New("invalid JSON-RPC message")
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
