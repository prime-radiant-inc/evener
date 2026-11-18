package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"reflect"
)

// boundFrameMinSize is the smallest frame whose response result is decoded
// straight into storage its waiting request allocated. Below it the header walk
// costs more than the second pass over the payload it replaces, so small frames
// keep the ordinary decode.
const boundFrameMinSize = 64 << 10

// frameBinder resolves the storage a response frame's result belongs in, from
// the frame's raw id token. A nil result leaves the frame on the ordinary decode
// path, which is what an unknown, late, duplicate or cancelled id gets.
type frameBinder func(idToken []byte) any

// boundRecvTransport is a transport that can decode a response frame's result
// into storage its binder resolves for the frame's id, instead of leaving the
// bytes for the client to scan a second time. The storage belongs to one pending
// request and is never the value the caller passed in, so a decode that lands
// after the caller gave up writes nothing anyone reads.
type boundRecvTransport interface {
	Transport
	recvBound(context.Context, frameBinder) (Message, any, error)
}

// requestTarget reports how out takes a directly decoded result. It returns
// nils when out cannot take one: a target that already holds a value cannot,
// because the ordinary path decodes into that value and leaves members the
// frame omits alone, which replacing the whole value could not do.
func requestTarget(out any) (newStorage func() any, copyInto func(any)) {
	if out == nil {
		return nil, nil
	}
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.IsNil() || !v.Elem().IsZero() {
		return nil, nil
	}
	elem := v.Elem()
	return func() any { return reflect.New(elem.Type()).Interface() },
		func(decoded any) { elem.Set(reflect.ValueOf(decoded).Elem()) }
}

// decodeFrameBound decodes data the way decodeFrame does, and additionally
// decodes a response frame's result into storage the binder resolves for its id
// when the frame is large enough to pay for the header walk. The returned storage
// is set only when one pass both validated the frame and filled it; every other
// frame, including one whose result does not fit its type, takes the ordinary
// path and reports what it reports today.
func decodeFrameBound(data []byte, bind frameBinder) (Message, any, error) {
	if len(data) >= boundFrameMinSize && bind != nil {
		if idToken, ok := responseFrameIDToken(data); ok {
			if storage := bind(idToken); storage != nil {
				if msg, ok := decodeResponseInto(data, idToken, storage); ok {
					if err := validateBoundModelControlResponse(storage, data); err != nil {
						return Message{}, nil, err
					}
					return msg, storage, nil
				}
			}
		}
	}
	var msg Message
	if err := decodeFrame(&msg, data); err != nil {
		return Message{}, nil, err
	}
	return msg, nil, nil
}

// validateBoundModelControlResponse applies the required-field check the
// ordinary decode path runs, so a large response cannot skip it by taking the
// bound path. Size decides which path a frame takes, and a contract that held
// only below the bound's threshold would accept an incomplete model-control
// envelope whenever the transcript happened to be big enough.
//
// The check reads the frame's result member because it is about which members
// are present, and an omitted bool is indistinguishable from false once
// decoded. It is one streaming walk that skips the members it does not need
// and validates the result where it finds it, so a frame costs the same two
// passes here as it does on the ordinary path; see #6758 for folding presence
// detection into the single decode.
func validateBoundModelControlResponse(storage any, data []byte) error {
	var method string
	switch storage.(type) {
	case *ThreadReadResponse:
		method = MethodThreadRead
	case *ThreadModelSetResponse:
		method = MethodThreadModelSet
	default:
		return nil
	}
	if err := validateResponseResultPresence(method, data); err != nil {
		// Wrap the refusal the way the ordinary path does, so a frame refused
		// here is refused indistinguishably from the same frame below the bound.
		return responseDecodeError{method: method, err: err}
	}
	return nil
}

// validateResponseResultPresence runs validateRawModelControlResponse over a
// frame's result member, which is the shape it validates. The result bytes are
// read and validated before the walk reads anything else, because a jsontext
// value aliases the decoder's buffer and is only defined until the next read.
//
// A frame that cannot be walked is an error rather than a pass: this is the
// only presence check the bound path runs, and a frame whose members cannot be
// read is exactly the frame whose omissions cannot be ruled out.
func validateResponseResultPresence(method string, data []byte) error {
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != jsontext.KindBeginObject {
		// Not a response envelope, so there is no result member to check. The
		// caller only reaches the walk for a frame that already decoded into a
		// response struct, which is always an object.
		return nil
	}
	for {
		key, err := dec.ReadToken()
		if err != nil {
			return err
		}
		if key.Kind() == jsontext.KindEndObject {
			return nil
		}
		if key.String() == "result" {
			value, err := dec.ReadValue()
			if err != nil {
				return err
			}
			return validateRawModelControlResponse(method, value)
		}
		if err := dec.SkipValue(); err != nil {
			return err
		}
	}
}

// decodeResponseInto decodes data into a response whose result already lives in
// storage, and reports false for every frame the ordinary decoder must classify
// or reject itself: a jsonrpc, error or method member, a result member that did
// not fit storage, and an id this decode reads differently from the token that
// resolved storage.
//
// The result member is handed the address in Result, so the decoder fills storage
// from the frame's own bytes: the member is neither copied into a json.RawMessage
// nor parsed into a tree first, which is what makes this one pass instead of two.
// Response.Result therefore carries the filled value rather than the wire bytes on
// this path, and the message is delivered only to the request that owns it.
func decodeResponseInto(data, idToken []byte, storage any) (Message, bool) {
	var probe struct {
		JSONRPC *json.RawMessage `json:"jsonrpc"`
		ID      json.RawMessage  `json:"id"`
		Method  string           `json:"method"`
		Result  any              `json:"result"`
		Error   json.RawMessage  `json:"error"`
	}
	probe.Result = storage
	if err := json.Unmarshal(data, &probe); err != nil {
		// A result that does not fit storage is the caller's error to see. The
		// ordinary path decodes the frame again and reports it from there.
		return Message{}, false
	}
	// A null result clears the interface rather than reaching a target's own
	// unmarshaler, which may reject the null or keep it. The ordinary path calls
	// that unmarshaler, so it decides.
	if probe.Result == nil {
		return Message{}, false
	}
	if probe.JSONRPC != nil || len(probe.Error) > 0 || probe.Method != "" {
		return Message{}, false
	}
	// The id the frame ends up carrying must be the token that resolved storage,
	// or a later member named a response other than the one that filled it.
	if !bytes.Equal(probe.ID, idToken) {
		return Message{}, false
	}
	resp := Response{Result: storage}
	if len(probe.ID) > 0 {
		if err := resp.ID.UnmarshalJSON(probe.ID); err != nil {
			return Message{}, false
		}
	}
	return Message{Response: &resp}, true
}

// responseFrameIDToken returns the raw id token of a response frame, and reports
// false for every envelope the ordinary decoder would read differently: a method
// member, a repeated id, and a result that is missing or repeated. A repeated
// result merges its members into one value here, where the ordinary decoder keeps
// only the last, so such a frame stays on the ordinary path.
func responseFrameIDToken(data []byte) ([]byte, bool) {
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	tok, err := dec.ReadToken()
	if err != nil || tok.Kind() != jsontext.KindBeginObject {
		return nil, false
	}
	var id []byte
	results := 0
	for {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, false
		}
		if key.Kind() == jsontext.KindEndObject {
			break
		}
		switch key.String() {
		case "result":
			results++
			if err := dec.SkipValue(); err != nil {
				return nil, false
			}
		case "id":
			if id != nil {
				return nil, false
			}
			value, err := dec.ReadValue()
			if err != nil {
				return nil, false
			}
			// A decoder value aliases the decoder's own buffer and is only
			// defined until the next read, so the id is copied before the walk
			// goes on to skip the members after it.
			id = append([]byte(nil), value...)
		case "method":
			return nil, false
		default:
			if err := dec.SkipValue(); err != nil {
				return nil, false
			}
		}
	}
	if id == nil || results != 1 {
		return nil, false
	}
	return id, true
}
