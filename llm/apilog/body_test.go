package apilog

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEncodedBodyUTF8ExactRoundTrip(t *testing.T) {
	want := []byte("{\"text\":\"line\\nquote\\\"\"}")
	encoded := EncodeBody(want)
	if encoded.Encoding != BodyUTF8 {
		t.Fatalf("Encoding = %q, want %q", encoded.Encoding, BodyUTF8)
	}
	if encoded.Data != string(want) {
		t.Fatalf("Data = %q, want %q", encoded.Data, want)
	}
	if encoded.ByteCount != len(want) {
		t.Fatalf("ByteCount = %d, want %d", encoded.ByteCount, len(want))
	}
	if !encoded.Exact || encoded.CredentialValuesExcluded {
		t.Fatalf("body truth = (exact=%t, credential_values_excluded=%t), want (true, false)", encoded.Exact, encoded.CredentialValuesExcluded)
	}

	data, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EncodedBody
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBody(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DecodeBody() = %q, want %q", got, want)
	}
}

func TestEncodedBodyBase64ExactRoundTrip(t *testing.T) {
	want := []byte{0x00, 0xff, 0x80, '\n'}
	encoded := EncodeBody(want)
	if encoded.Encoding != BodyBase64 {
		t.Fatalf("Encoding = %q, want %q", encoded.Encoding, BodyBase64)
	}
	if encoded.ByteCount != len(want) {
		t.Fatalf("ByteCount = %d, want %d", encoded.ByteCount, len(want))
	}
	got, err := DecodeBody(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DecodeBody() = %v, want %v", got, want)
	}
}

func TestEncodedBodyEveryByteValueRoundTrips(t *testing.T) {
	want := make([]byte, 256)
	for i := range want {
		want[i] = byte(i)
	}
	got, err := DecodeBody(EncodeBody(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("DecodeBody(EncodeBody(data)) did not preserve all byte values")
	}
}

func TestEncodedBodyRejectsInvalidEncodingOrLength(t *testing.T) {
	tests := []struct {
		name string
		body EncodedBody
	}{
		{"unknown encoding", EncodedBody{Encoding: "hex", Data: "00", ByteCount: 1}},
		{"invalid base64", EncodedBody{Encoding: BodyBase64, Data: "%%%", ByteCount: 1}},
		{"wrong UTF-8 byte count", EncodedBody{Encoding: BodyUTF8, Data: "two", ByteCount: 2}},
		{"negative byte count", EncodedBody{Encoding: BodyUTF8, ByteCount: -1}},
		{"invalid UTF-8 payload", EncodedBody{Encoding: BodyUTF8, Data: string([]byte{0xff}), ByteCount: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeBody(tt.body); err == nil {
				t.Fatal("DecodeBody() accepted invalid encoded body")
			}
		})
	}
}

func TestEncodedBodyDecodeRequiresExplicitTruthFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing exact",
			body: `{"encoding":"utf8","data":"body","byte_count":4,"credential_values_excluded":false}`,
		},
		{
			name: "missing credential exclusion",
			body: `{"encoding":"utf8","data":"body","byte_count":4,"exact":true}`,
		},
		{
			name: "null exact",
			body: `{"encoding":"utf8","data":"body","byte_count":4,"exact":null,"credential_values_excluded":false}`,
		},
		{
			name: "unknown field",
			body: `{"encoding":"utf8","data":"body","byte_count":4,"exact":true,"credential_values_excluded":false,"future":true}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body EncodedBody
			if err := json.Unmarshal([]byte(tt.body), &body); err == nil {
				t.Fatal("json.Unmarshal() accepted a body without the strict truth contract")
			}
		})
	}
}

func TestEncodedBodyRejectsExactCredentialExcludedContent(t *testing.T) {
	body := EncodeBody([]byte("credential"))
	body.CredentialValuesExcluded = true
	if _, err := DecodeBody(body); err == nil {
		t.Fatal("DecodeBody() accepted exact credential-excluded content")
	}

	body = EncodedBody{CredentialValuesExcluded: true}
	if got, err := DecodeBody(body); err != nil {
		t.Fatalf("DecodeBody() rejected omitted credential content: %v", err)
	} else if got != nil {
		t.Fatalf("DecodeBody() = %v, want nil omitted content", got)
	}
}
