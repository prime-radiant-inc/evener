package appwire

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		return fmt.Errorf("request id must not be null")
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
	JSONRPC string          `json:"jsonrpc"`
	ID      ID              `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      ID     `json:"id"`
	Result  any    `json:"result"`
}

type ErrorResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      ID        `json:"id"`
	Error   WireError `json:"error"`
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
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Method  string           `json:"method"`
		Result  json.RawMessage  `json:"result"`
		Error   json.RawMessage  `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.JSONRPC != "2.0" {
		return fmt.Errorf("jsonrpc=%q, want 2.0", probe.JSONRPC)
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
		return fmt.Errorf("invalid JSON-RPC message")
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
		return nil, fmt.Errorf("invalid JSON-RPC message")
	}
}

func RequestMessage(id ID, method string, params any) Message {
	return Message{Request: &Request{JSONRPC: "2.0", ID: id, Method: method, Params: mustRaw(params)}}
}

func NotificationMessage(method string, params any) Message {
	return Message{Notification: &Notification{JSONRPC: "2.0", Method: method, Params: mustRaw(params)}}
}

func ResponseMessage(id ID, result any) Message {
	return Message{Response: &Response{JSONRPC: "2.0", ID: id, Result: result}}
}

func ErrorMessage(id ID, err WireError) Message {
	return Message{Error: &ErrorResponse{JSONRPC: "2.0", ID: id, Error: err}}
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
