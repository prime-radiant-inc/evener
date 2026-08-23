package llm

import (
	"bytes"
	"image"
	"image/gif"
	"testing"
)

// TestRasterMediaTypeGifDecodeError covers the gif decode error path
// (lines 83-84). We provide a file that image.Decode recognizes as gif but
// gif.DecodeAll fails on.
func TestRasterMediaTypeGifDecodeError(t *testing.T) {
	// A minimal GIF header that image.Decode accepts but gif.DecodeAll rejects.
	// GIF89a with a 1x1 logical screen, no image data.
	data := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00\x3b")
	_, err := RasterMediaType(data)
	if err == nil {
		t.Fatal("expected error from RasterMediaType with truncated GIF data")
	}
}

// TestRasterMediaTypeGifValid covers the happy path for a GIF that decodes.
func TestRasterMediaTypeGifValid(t *testing.T) {
	// Encode a real 1x1 GIF that decodes successfully.
	var buf bytes.Buffer
	if err := gif.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	mt, err := RasterMediaType(buf.Bytes())
	if err != nil {
		t.Fatalf("RasterMediaType(valid gif): %v", err)
	}
	if mt == "" {
		t.Fatal("expected non-empty media type for valid GIF")
	}
}
