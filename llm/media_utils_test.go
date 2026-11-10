package llm

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	pngenc "image/png"
	"mime"
	"path/filepath"
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

// DataURI builds the data:<mime>;base64, prefix and streams the payload into
// one growing buffer. The previous EncodeToString + Sprintf path allocated
// four payload-sized buffers, so a 12 MB document made ~64 MB of transient
// garbage (#5488). Pin both the encoded output and the allocation count.
func TestDataURISinglePayloadAllocation(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	got := DataURI("application/pdf", payload)
	want := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(payload)
	if got != want {
		t.Fatalf("DataURI output differs from base64.StdEncoding over the same payload")
	}
	if allocs := testing.AllocsPerRun(20, func() { _ = DataURI("application/pdf", payload) }); allocs > 3 {
		t.Fatalf("DataURI allocates %.0f times for a 1 MiB payload, want <= 3", allocs)
	}
}

func BenchmarkDataURI(b *testing.B) {
	payload := bytes.Repeat([]byte{0xAB}, 1<<20)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		_ = DataURI("application/pdf", payload)
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

func grayPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := pngenc.Encode(&buf, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestNormalizeImageDecodedPixelBudget bounds the pixels image.Decode may
// allocate while normalizing an image the wire does not accept. Encoded bytes
// alone do not bound the decoded canvas: a small, highly compressed PNG can
// name a huge image and exhaust process memory. 16 MP matches
// llm/internal/imagerepair's existing JPEG budget.
func TestNormalizeImageDecodedPixelBudget(t *testing.T) {
	atBudget := grayPNG(t, 4000, 4000) // exactly 16,000,000 pixels
	got, mt, err := NormalizeImage(atBudget, ImageBlockMediaType)
	if err != nil || mt != "image/png" || !bytes.Equal(got, atBudget) {
		t.Fatalf("NormalizeImage(16 MP png) = (%d bytes, %q, %v), want the png unchanged", len(got), mt, err)
	}

	overBudget := grayPNG(t, 4001, 4001)
	_, _, err = NormalizeImageForWire(overBudget, "", ImageBlockMediaType, "oversized image refused")
	if _, ok := errors.AsType[*ConfigurationError](err); !ok {
		t.Fatalf("oversized image error = %T %v, want *ConfigurationError", err, err)
	}
}

// TestDocumentFileData pins the payload rule the document builders share:
// bytes ride as an application/pdf data URI (the blank media type defaults
// to pdf), and bytes win when both are set so a caller that supplies both
// never silently switches sources. A document without bytes carries no
// shared payload — whether its URL rides the wire is per protocol.
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
		{"url only", &DocumentData{URL: "https://example.com/doc.pdf"}, ""},
		{"bytes win over a url", &DocumentData{Data: []byte("%PDF-1.7 stub"), MediaType: "application/pdf", URL: "https://example.com/doc.pdf"}, pdfDataURI},
		{"neither payload", &DocumentData{MediaType: "application/pdf"}, ""},
	}
	for _, tc := range cases {
		if got := DocumentFileData(tc.doc); got != tc.want {
			t.Errorf("%s: DocumentFileData = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Each decodable raster format reports the media type its bytes establish.
func TestRasterMediaTypeOfEachDecodedFormat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var jpg, gifData bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifData, img, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", jpg.Bytes(), "image/jpeg"},
		{"gif", gifData.Bytes(), "image/gif"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := RasterMediaType(tc.data); err != nil || got != tc.want {
				t.Fatalf("RasterMediaType = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

// A multi-frame GIF whose first frame decodes but whose later frame is cut
// short is rejected: every frame must decode.
func TestRasterMediaTypeRejectsAGIFWithATruncatedFrame(t *testing.T) {
	frame := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black})
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{0, 0}}); err != nil {
		t.Fatal(err)
	}
	corrupted := buf.Bytes()[:buf.Len()-5]
	if _, format, err := image.Decode(bytes.NewReader(corrupted)); err != nil || format != "gif" {
		t.Fatalf("fixture header decode = format %q, error %v; want gif success", format, err)
	}
	if _, err := gif.DecodeAll(bytes.NewReader(corrupted)); err == nil {
		t.Fatal("fixture unexpectedly passed full GIF decode")
	}
	if got, err := RasterMediaType(corrupted); err == nil || got != "" {
		t.Fatalf("RasterMediaType(corrupted GIF) = (%q, %v), want empty type and decode error", got, err)
	}
}

func TestExpandTilde(t *testing.T) {
	t.Run("no tilde prefix returns trimmed input", func(t *testing.T) {
		if got := ExpandTilde("  relative/path  "); got != "relative/path" {
			t.Errorf("ExpandTilde = %q, want %q", got, "relative/path")
		}
		if got := ExpandTilde("/abs/path"); got != "/abs/path" {
			t.Errorf("ExpandTilde = %q, want %q", got, "/abs/path")
		}
	})

	t.Run("tilde expands to home", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := filepath.Join(home, "sub", "dir")
		if got := ExpandTilde("~/sub/dir"); got != want {
			t.Errorf("ExpandTilde = %q, want %q", got, want)
		}
	})

	t.Run("empty home leaves tilde path unchanged", func(t *testing.T) {
		t.Setenv("HOME", "")
		if got := ExpandTilde("~/sub"); got != "~/sub" {
			t.Errorf("ExpandTilde = %q, want %q (unchanged)", got, "~/sub")
		}
	})
}

func TestInferMimeTypeFromPath(t *testing.T) {
	// Register a deterministic charset-bearing type so the charset-strip branch
	// is exercised regardless of the host's /etc/mime.types.
	if err := mime.AddExtensionType(".charsettxt", "text/plain; charset=utf-8"); err != nil {
		t.Fatalf("AddExtensionType: %v", err)
	}

	tests := []struct {
		path string
		want string
	}{
		{"noextension", ""},
		{"", ""},
		{"image.png", "image/png"},
		{"doc.charsettxt", "text/plain"}, // charset stripped
		{"archive.zzzznope", ""},         // unknown extension
		{"UPPER.PNG", "image/png"},       // case-insensitive
	}
	for _, tt := range tests {
		if got := InferMimeTypeFromPath(tt.path); got != tt.want {
			t.Errorf("InferMimeTypeFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestDataURI(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		data     []byte
		want     string
	}{
		{"empty mime uses octet-stream", "", []byte("hi"), "data:application/octet-stream;base64,aGk="},
		{"whitespace mime uses octet-stream", "   ", []byte("hi"), "data:application/octet-stream;base64,aGk="},
		{"explicit mime", "image/png", []byte("hi"), "data:image/png;base64,aGk="},
		{"empty data", "text/plain", nil, "data:text/plain;base64,"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DataURI(tt.mimeType, tt.data); got != tt.want {
				t.Errorf("DataURI = %q, want %q", got, tt.want)
			}
		})
	}
}
