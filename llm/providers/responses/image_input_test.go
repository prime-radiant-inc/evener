package responses

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
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

// multiFrameGIF encodes a two-frame GIF (an animation).
func multiFrameGIF(t *testing.T) []byte {
	t.Helper()
	frame := func(c color.Color) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{c, color.White})
		return p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{frame(color.Black), frame(color.RGBA{R: 0xff})},
		Delay: []int{0, 0},
	}); err != nil {
		t.Fatalf("encode GIF fixture: %v", err)
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

// An animated GIF is flattened to its first frame: OpenAI's input_image carries
// a single still, and gif decoding must survive a second frame.
func TestNormalizeImageInput_FlattensAnimatedGIF(t *testing.T) {
	frames := multiFrameGIF(t)
	all, err := gif.DecodeAll(bytes.NewReader(frames))
	if err != nil || len(all.Image) != 2 {
		t.Fatalf("fixture is not a two-frame GIF: frames=%d err=%v", len(all.Image), err)
	}
	data, mediaType, err := normalizeImageInput(frames, "image/gif")
	if err != nil {
		t.Fatalf("normalizeImageInput(animated gif): %v", err)
	}
	if mediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png (first frame)", mediaType)
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || format != "png" {
		t.Fatalf("flattened bytes decode as (%q, %v), want png", format, err)
	}
}

// A GIF whose first frame reads but whose later frame data is corrupt must be
// rejected locally, not passed through to the provider as-is: image.Decode
// reads only the first frame, so gif.DecodeAll is the only full-file check.
func TestNormalizeImageInput_RejectsCorruptLaterGIFFrame(t *testing.T) {
	full := multiFrameGIF(t)
	truncated := full[:len(full)-10]
	if _, format, err := image.Decode(bytes.NewReader(truncated)); err != nil || format != "gif" {
		t.Fatalf("fixture no longer exhibits a readable first frame: (%q, %v)", format, err)
	}
	if _, _, err := normalizeImageInput(truncated, "image/gif"); err == nil {
		t.Fatal("normalizeImageInput passed through a GIF with a corrupt later frame")
	}
}
