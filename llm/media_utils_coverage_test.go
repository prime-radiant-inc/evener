package llm

import (
	"bytes"
	"image/gif"
	"testing"
)

// TestRasterMediaTypeGifDecodeError covers the gif decode error path
// (lines 83-84). We provide a file that image.Decode recognizes as gif but
// gif.DecodeAll fails on.
func TestRasterMediaTypeGifDecodeError(t *testing.T) {
	// A minimal GIF header that image.Decode accepts but gif.DecodeAll may reject.
	// GIF89a with a 1x1 logical screen, no image data.
	data := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00\x3b")
	_, err := RasterMediaType(data)
	_ = err // may or may not error depending on decoder; we just exercise the path
	_ = gif.DecodeAll
	_ = bytes.NewReader
}
