package llm

import (
	"bytes"
	"image"
	pngenc "image/png"
	"testing"

	"golang.org/x/image/bmp"
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

// NormalizeImageBlock keeps a supported raster's bytes and media type, turns a
// raster the image blocks reject (BMP, TIFF) into a PNG, and errors on bytes
// that are not a decodable raster. The Anthropic and OpenAI builders both rely
// on it (#4464).
func TestNormalizeImageBlock(t *testing.T) {
	var png bytes.Buffer
	if err := pngenc.Encode(&png, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if got, mt, err := NormalizeImageBlock(png.Bytes()); err != nil || mt != "image/png" || !bytes.Equal(got, png.Bytes()) {
		t.Fatalf("NormalizeImageBlock(png) = (%d bytes, %q, %v), want the png unchanged", len(got), mt, err)
	}

	var bitmap bytes.Buffer
	if err := bmp.Encode(&bitmap, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	got, mt, err := NormalizeImageBlock(bitmap.Bytes())
	if err != nil {
		t.Fatalf("NormalizeImageBlock(bmp): %v", err)
	}
	if mt != "image/png" {
		t.Fatalf("NormalizeImageBlock(bmp) media type = %q, want image/png", mt)
	}
	if _, format, err := image.Decode(bytes.NewReader(got)); err != nil || format != "png" {
		t.Fatalf("NormalizeImageBlock(bmp) bytes decode as (%q, %v), want png", format, err)
	}

	for name, data := range map[string][]byte{
		"empty":     nil,
		"not-image": []byte("this is not an image at all"),
	} {
		if _, _, err := NormalizeImageBlock(data); err == nil {
			t.Errorf("NormalizeImageBlock(%s) accepted non-raster bytes", name)
		}
	}
}

// TestDocumentFileData pins the payload rule every document-capable builder
// shares: bytes ride as an application/pdf data URI (the blank media type
// defaults to pdf), a URL rides alone, and bytes win when both are set so a
// caller that supplies both never silently switches sources.
func TestDocumentFileData(t *testing.T) {
	const pdfDataURI = "data:application/pdf;base64,JVBERi0xLjcgc3R1Yg=="
	cases := []struct {
		name string
		doc  *DocumentData
		want string
	}{
		{"nil document", nil, ""},
		{"bytes", &DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: "application/pdf"}, pdfDataURI},
		{"bytes with a blank media type default to pdf", &DocumentData{Data: []byte("%PDF-1.7 stub")}, pdfDataURI},
		{"bytes with a padded media type", &DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: " application/pdf "}, pdfDataURI},
		{"url", &DocumentData{URL: "https://example.com/doc.pdf"}, "https://example.com/doc.pdf"},
		{"bytes win over a url", &DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: "application/pdf", URL: "https://example.com/doc.pdf"}, pdfDataURI},
		{"neither payload", &DocumentData{MediaType: "application/pdf"}, ""},
	}
	for _, tc := range cases {
		if got := DocumentFileData(tc.doc); got != tc.want {
			t.Errorf("%s: DocumentFileData = %q, want %q", tc.name, got, tc.want)
		}
	}
}
