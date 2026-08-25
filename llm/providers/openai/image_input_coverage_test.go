package openai

import (
	"image"
	"image/color"
	"testing"
)

// TestOpenAIImageMediaType covers the jpeg, gif, and webp branches (lines 64, 66).
func TestOpenAIImageMediaType(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"png", "image/png"},
		{"jpeg", "image/jpeg"},
		{"gif", "image/gif"},
		{"webp", "image/webp"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		got := openAIImageMediaType(tt.format)
		if got != tt.want {
			t.Errorf("openAIImageMediaType(%q) = %q, want %q", tt.format, got, tt.want)
		}
	}
}

// TestEncodeImageInputAsPNG covers the PNG encoding path (lines 54-55) with a
// valid image.
func TestEncodeImageInputAsPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	data, mediaType, err := encodeImageInputAsPNG(img)
	if err != nil {
		t.Fatalf("encodeImageInputAsPNG: %v", err)
	}
	if mediaType != "image/png" {
		t.Fatalf("mediaType = %q, want image/png", mediaType)
	}
	if len(data) == 0 {
		t.Fatal("data should not be empty")
	}
}

// TestNormalizeImageInputGifDecodeError covers the gif decode error path
// (lines 38-39).
func TestNormalizeImageInputGifDecodeError(t *testing.T) {
	// A truncated GIF that image.Decode accepts but gif.DecodeAll rejects.
	// Minimal GIF89a header with a 1x1 logical screen but no valid image data.
	data := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00\x3b")
	_, _, err := normalizeImageInput(data, "image/gif")
	if err == nil {
		t.Fatal("expected error from normalizeImageInput with truncated GIF data")
	}
}
