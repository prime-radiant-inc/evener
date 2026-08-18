//go:build serffuzz

package apilog

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

// FuzzAPILogHeaderCodec drives the header codec, whose entire reason for
// existing is that HTTP header values are BYTES and JSON strings are not.
// Provider responses carry headers evener did not choose, and api.jsonl is
// evidence — a header that does not survive the round trip byte-for-byte is a
// forensic record of something that never happened.
//
// Oracles:
//
//   - Byte-exact round trip. Marshal then Unmarshal returns the identical map,
//     including values that are not valid UTF-8, which is the case the base64
//     branch exists for and the case a naive encoder silently corrupts.
//   - The encoding is chosen by the data, not guessed: valid UTF-8 stays UTF-8
//     (readable in the log), anything else goes base64.
//   - ByteCount is a tamper check, not decoration. A value whose count does not
//     match the bytes it decodes to must be refused, and refused at
//     UnmarshalJSON too, so a corrupt record cannot enter memory looking intact.
func FuzzAPILogHeaderCodec(f *testing.F) {
	f.Add("Content-Type", []byte("application/json"), []byte("utf-8"), 1)
	f.Add("X-Raw", []byte{0xff, 0xfe, 0x00}, []byte{}, 0)
	f.Add("X-Empty", []byte{}, []byte{}, -1)
	f.Add("X-Mixed", []byte("ok"), []byte{0x80}, 2)
	f.Add("", []byte("no name"), []byte("v"), 0)

	f.Fuzz(func(t *testing.T, name string, v1, v2 []byte, countDelta int) {
		if len(v1) > 4096 || len(v2) > 4096 || len(name) > 256 {
			t.Skip()
		}
		// A JSON object key must be valid UTF-8; encoding/json substitutes
		// U+FFFD otherwise, so an invalid name could not round-trip and the
		// limitation belongs to JSON rather than to this codec.
		if !utf8.ValidString(name) {
			t.Skip()
		}

		header := EncodedHeader{name: {string(v1), string(v2)}}
		data, err := json.Marshal(header)
		if err != nil {
			t.Fatalf("marshalling header %q: %v", name, err)
		}
		var back EncodedHeader
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshalling %s: %v", data, err)
		}
		if !reflect.DeepEqual(header, back) {
			t.Fatalf("header round trip changed the bytes:\n  in  %#v\n  out %#v", header, back)
		}

		for _, raw := range [][]byte{v1, v2} {
			value := EncodeHeaderValue(raw)
			wantEncoding := BodyBase64
			if utf8.Valid(raw) {
				wantEncoding = BodyUTF8
			}
			if value.Encoding != wantEncoding {
				t.Fatalf("EncodeHeaderValue(%q) chose %q, want %q", raw, value.Encoding, wantEncoding)
			}
			if value.ByteCount != len(raw) {
				t.Fatalf("EncodeHeaderValue(%q) recorded ByteCount %d, want %d", raw, value.ByteCount, len(raw))
			}
			decoded, err := DecodeHeaderValue(value)
			if err != nil {
				t.Fatalf("DecodeHeaderValue of its own output failed: %v", err)
			}
			if !reflect.DeepEqual(decoded, raw) && !(len(decoded) == 0 && len(raw) == 0) {
				t.Fatalf("value round trip changed the bytes: in %q out %q", raw, decoded)
			}

			// A count that disagrees with the payload means the record was
			// edited or truncated; decoding must say so rather than hand back
			// bytes under a length nobody wrote.
			if countDelta != 0 {
				tampered := value
				tampered.ByteCount = value.ByteCount + countDelta
				if _, err := DecodeHeaderValue(tampered); err == nil {
					t.Fatalf("DecodeHeaderValue accepted ByteCount %d for %d bytes",
						tampered.ByteCount, len(raw))
				}
				// The same lie must not survive the JSON door either.
				encoded, err := json.Marshal(map[string][]EncodedHeaderValue{name: {tampered}})
				if err != nil {
					t.Fatalf("marshalling tampered value: %v", err)
				}
				var reread EncodedHeader
				if err := json.Unmarshal(encoded, &reread); err == nil {
					t.Fatalf("UnmarshalJSON accepted a tampered ByteCount %d for %d bytes",
						tampered.ByteCount, len(raw))
				}
			}
		}
	})
}
