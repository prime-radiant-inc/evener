package llm

import (
	"bytes"
	"image"
	pngenc "image/png"
	"testing"
)

func TestRasterMediaTypeDecodesFullyAndRejectsTruncatedImages(t *testing.T) {
	var png bytes.Buffer
	if err := pngenc.Encode(&png, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if got, err := RasterMediaType(png.Bytes()); err != nil || got != "image/png" {
		t.Fatalf("RasterMediaType(png) = (%q, %v), want (\"image/png\", nil)", got, err)
	}

	// Deciding by full decode rather than by sniffing the header is the entire
	// contract: a valid header followed by truncated pixel data has to be
	// rejected here, not admitted into model history to fail at the provider.
	full := png.Bytes()
	if got, err := RasterMediaType(full[:len(full)/2]); err == nil {
		t.Fatalf("RasterMediaType(truncated png) = (%q, nil), want an error", got)
	}

	for name, data := range map[string][]byte{
		"empty":     nil,
		"not-image": []byte("this is not an image at all"),
	} {
		if got, err := RasterMediaType(data); err == nil {
			t.Errorf("RasterMediaType(%s) = (%q, nil), want an error", name, got)
		}
	}
}

func TestIsImageFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"photo.png", true},
		{"photo.PNG", true},
		{"photo.jpg", true},
		{"photo.jpeg", true},
		{"photo.gif", true},
		{"photo.webp", true},
		{"photo.bmp", true},
		{"photo.svg", false}, // SVG is XML, not a raster image
		{"code.go", false},
		{"readme.md", false},
		{"data.json", false},
		{"/path/to/image.png", true},
		{"", false},
		{"noext", false},
	}
	for _, tt := range tests {
		got := IsImageFile(tt.path)
		if got != tt.want {
			t.Errorf("IsImageFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
