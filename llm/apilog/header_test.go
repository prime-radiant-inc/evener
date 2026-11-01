package apilog

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestEncodedHeaderValueRoundTripsUTF8AndBinaryBytes(t *testing.T) {
	for _, tt := range []struct {
		name string
		want []byte
	}{
		{name: "UTF-8", want: []byte("visible header value")},
		{name: "binary", want: []byte{'a', 0xff, 0x00, 'b'}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeHeaderValue(tt.want)
			data, err := json.Marshal(encoded)
			if err != nil {
				t.Fatal(err)
			}
			var decoded EncodedHeaderValue
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			got, err := DecodeHeaderValue(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("header value = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEncodedHeaderRoundTripsInvalidUTF8Values(t *testing.T) {
	want := EncodedHeader{
		"X-Bytes": {string([]byte{'a', 0xff, 0x00, 'b'}), "second"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"X-Bytes":["`)) {
		t.Fatalf("encoded header used lossy JSON strings: %s", data)
	}
	var got EncodedHeader
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("header = %#v, want %#v", got, want)
	}
}

func TestEncodedHeaderValueRejectsUnknownFields(t *testing.T) {
	var value EncodedHeaderValue
	if err := json.Unmarshal([]byte(`{"encoding":"utf8","data":"value","byte_count":5,"future":true}`), &value); err == nil {
		t.Fatal("json.Unmarshal() accepted an unknown encoded header field")
	}
}

// A header value that cannot decode fails the whole header decode, while a
// well-formed value round-trips through the map.
func TestEncodedHeaderRejectsAnUndecodableValue(t *testing.T) {
	var bad EncodedHeader
	if err := json.Unmarshal([]byte(`{"x":[{"encoding":"base64","data":"!!!!","byte_count":4}]}`), &bad); err == nil {
		t.Fatal("an undecodable header value must fail the header decode")
	}
	var ok EncodedHeader
	if err := json.Unmarshal([]byte(`{"x":[{"encoding":"utf8","data":"v","byte_count":1}]}`), &ok); err != nil {
		t.Fatalf("a well-formed header value failed to decode: %v", err)
	}
	if v := ok["x"]; len(v) != 1 || v[0] != "v" {
		t.Fatalf("decoded header = %v, want the round-tripped value", ok)
	}
}
