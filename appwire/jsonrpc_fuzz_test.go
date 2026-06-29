package appwire

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzMessageDecode fuzzes the AppWire frame decoder. Beyond the floor "no
// panic" oracle it asserts a decode→encode→decode fixed point: any frame that
// decodes cleanly must re-marshal and re-decode to an equal value. The
// accessors that callers reach for on a decoded frame (Kind, IDString, and the
// ID conversions) must also never panic.
func FuzzMessageDecode(f *testing.F) {
	seeds := []string{
		`{"id":1,"method":"thread/list","params":{}}`,
		`{"id":"abc","method":"ping"}`,
		`{"method":"some/notification","params":{"x":1}}`,
		`{"id":2,"result":{"ok":true}}`,
		`{"id":3,"error":{"code":-32600,"message":"bad"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"x"}`,
		`{"id":null,"method":"x"}`,
		`{"id":9999999999999999999999,"method":"x"}`,
		`{"id":{"nested":"object"},"method":"x"}`,
		// Id-less error/result frames: the decoder accepts them (it only probes
		// for the error/result field), and Response/ErrorResponse.MarshalJSON
		// omit the empty id so they re-encode to an id-less frame and round-trip
		// cleanly. Kept as seeds so that fixed point stays pinned.
		`{"error":{}}`,
		`{"result":{}}`,
		`{}`,
		`null`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return // rejected input: nothing further to assert
		}

		// Accessors on a decoded frame must never panic.
		_ = m.Kind()
		_ = m.IDString()
		for _, id := range messageIDs(m) {
			_ = id.Int64()
			_ = id.String()
		}

		// Re-marshal must always succeed and never panic. This first marshal is
		// the normalization pass: encoding/json sorts map keys, compacts raw
		// messages, and replaces invalid UTF-8 — so we compare normalized forms,
		// not the raw input.
		encoded, err := json.Marshal(m)
		if err != nil {
			// A value that decoded but cannot re-encode is itself a defect.
			t.Fatalf("decoded frame failed to re-marshal: %v\n input=%q\n value=%#v", err, raw, m)
		}

		// Fixed point: decoding the normalized bytes and re-marshaling must
		// reproduce the same bytes. This holds for every frame the codec decodes
		// cleanly, including id-less response/error frames now that their
		// MarshalJSON omits the empty id instead of emitting an unreadable null.
		var m2 Message
		if err := json.Unmarshal(encoded, &m2); err != nil {
			t.Fatalf("re-marshaled frame failed to re-decode: %v\n input=%q\n encoded=%q", err, raw, encoded)
		}
		encoded2, err := json.Marshal(m2)
		if err != nil {
			t.Fatalf("re-decoded frame failed to re-marshal: %v\n encoded=%q\n value=%#v", err, encoded, m2)
		}
		if !bytes.Equal(encoded, encoded2) {
			t.Fatalf("encode is not idempotent after normalization:\n input=%q\n once=%q\n twice=%q", raw, encoded, encoded2)
		}
	})
}

// messageIDs returns the IDs carried by a decoded message, for the accessor
// panic-hunt.
func messageIDs(m Message) []ID {
	switch {
	case m.Request != nil:
		return []ID{m.Request.ID}
	case m.Response != nil:
		return []ID{m.Response.ID}
	case m.Error != nil:
		return []ID{m.Error.ID}
	default:
		return nil
	}
}
