package agent

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func validPNGFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return data.Bytes()
}

func validWebPFixture(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRjAAAABXRUJQVlA4ICQAAABwAQCdASoBAAEAAgA0JYwCdAGIQAD++ZNsGW2xURhNJHYAAAA=")
	if err != nil {
		t.Fatalf("decode WebP fixture: %v", err)
	}
	return data
}
