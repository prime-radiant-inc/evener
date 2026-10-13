package responses

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/bmp"

	"primeradiant.com/evener/llm"
)

// bitmapFixture encodes a 2x2 BMP — a raster OpenAI's image input does not
// accept on the wire.
func bitmapFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode BMP fixture: %v", err)
	}
	return buf.Bytes()
}

func rasterFixturePNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return buf.Bytes()
}

// imageURLs collects every input_image URL in a built Responses body.
func imageURLs(body map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if t["type"] == "input_image" {
				if u, ok := t["image_url"].(string); ok {
					out = append(out, u)
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		case []map[string]any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(body)
	return out
}

func assertPNGDataURI(t *testing.T, url string) {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("image_url = %q, want a png data URI", url)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		t.Fatalf("data URI payload is not base64: %v", err)
	}
	if _, format, err := image.Decode(bytes.NewReader(payload)); err != nil || format != "png" {
		t.Fatalf("data URI payload decodes as (%q, %v), want png", format, err)
	}
}

// A tool-result BMP must reach the wire as a PNG data URI. OpenAI rejects
// image/bmp in an input_image (#4464).
func TestBuildBody_ToolResultBitmapRidesAsPNGDataURI(t *testing.T) {
	req := userReq("look")
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID:     "call_1",
			Name:           "read_file",
			Content:        "read image",
			ImageData:      bitmapFixture(t),
			ImageMediaType: "image/bmp",
		}},
	}})
	body := build(t, req, resolved(openaiCaps))
	urls := imageURLs(body)
	if len(urls) != 1 {
		t.Fatalf("image URLs = %d, want 1: %s", len(urls), jsonOf(t, body))
	}
	assertPNGDataURI(t, urls[0])
}

// A user-message BMP takes the same path.
func TestBuildBody_UserBitmapRidesAsPNGDataURI(t *testing.T) {
	req := userReq("look")
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: bitmapFixture(t), MediaType: "image/bmp"}},
	}})
	body := build(t, req, resolved(openaiCaps))
	urls := imageURLs(body)
	if len(urls) != 1 {
		t.Fatalf("image URLs = %d, want 1: %s", len(urls), jsonOf(t, body))
	}
	assertPNGDataURI(t, urls[0])
}

// A supported raster keeps its bytes and media type on the wire (the
// non-regression guard for #4464).
func TestBuildBody_SupportedRasterKeepsItsMediaType(t *testing.T) {
	png := rasterFixturePNG(t)
	req := userReq("look")
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: png, MediaType: "image/png"}},
	}})
	body := build(t, req, resolved(openaiCaps))
	urls := imageURLs(body)
	if len(urls) != 1 {
		t.Fatalf("image URLs = %d, want 1: %s", len(urls), jsonOf(t, body))
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(urls[0], prefix) {
		t.Fatalf("image_url = %q, want a png data URI", urls[0])
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(urls[0], prefix))
	if err != nil || !bytes.Equal(payload, png) {
		t.Fatalf("png bytes changed (err=%v, got %d want %d)", err, len(payload), len(png))
	}
}

// normalizeImageInput is the one rule both this builder and the Anthropic
// builder enforce: a BMP or TIFF becomes PNG, a supported raster passes
// through, and undecodable bytes are an error.
func TestNormalizeImageInput(t *testing.T) {
	data, mediaType, err := normalizeImageInput(bitmapFixture(t), "image/bmp")
	if err != nil {
		t.Fatalf("normalizeImageInput(bmp): %v", err)
	}
	if mediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", mediaType)
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || format != "png" {
		t.Fatalf("normalized bytes decode as (%q, %v), want png", format, err)
	}
	if _, _, err := normalizeImageInput([]byte("not a raster"), "image/png"); err == nil {
		t.Fatal("normalizeImageInput accepted non-raster bytes")
	}
}
