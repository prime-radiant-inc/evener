package apilog

import (
	"testing"
)

// TestDecodeHeaderValueInvalidUTF8 covers the invalid UTF-8 path (line 37-38).
func TestDecodeHeaderValueInvalidUTF8(t *testing.T) {
	v := EncodedHeaderValue{
		Encoding:  BodyUTF8,
		Data:      "\xff\xfe",
		ByteCount: 2,
	}
	if _, err := DecodeHeaderValue(v); err == nil {
		t.Fatal("invalid UTF-8 should error")
	}
}

// TestDecodeHeaderValueBase64Error covers the base64 decode error path
// (lines 44-45).
func TestDecodeHeaderValueBase64Error(t *testing.T) {
	v := EncodedHeaderValue{
		Encoding:  BodyBase64,
		Data:      "not-valid-base64!!!",
		ByteCount: 14,
	}
	if _, err := DecodeHeaderValue(v); err == nil {
		t.Fatal("invalid base64 should error")
	}
}

// TestDecodeHeaderValueUnknownEncoding covers the unknown encoding path
// (line 47).
func TestDecodeHeaderValueUnknownEncoding(t *testing.T) {
	v := EncodedHeaderValue{
		Encoding:  "unknown",
		Data:      "",
		ByteCount: 0,
	}
	if _, err := DecodeHeaderValue(v); err == nil {
		t.Fatal("unknown encoding should error")
	}
}

// TestEncodedHeaderUnmarshalJSONNil covers the nil encoded map path
// (lines 89-92).
func TestEncodedHeaderUnmarshalJSONNil(t *testing.T) {
	var h EncodedHeader
	if err := h.UnmarshalJSON([]byte("null")); err != nil {
		t.Fatalf("UnmarshalJSON(null): %v", err)
	}
	if h != nil {
		t.Fatal("null should produce nil header")
	}
}

// TestEncodedHeaderUnmarshalJSONDecodeError covers the decode error path
// (lines 98-100).
func TestEncodedHeaderUnmarshalJSONDecodeError(t *testing.T) {
	var h EncodedHeader
	// A valid JSON map with an invalid header value (wrong encoding).
	data := []byte(`{"X-Bad":[{"encoding":"bogus","data":"","byte_count":0}]}`)
	if err := h.UnmarshalJSON(data); err == nil {
		t.Fatal("invalid header value should error on UnmarshalJSON")
	}
}
