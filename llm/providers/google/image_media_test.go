package google

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/gif"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func encodedRaster(t *testing.T, encode func(*bytes.Buffer) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := encode(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	return encodedRaster(t, func(buf *bytes.Buffer) error {
		return png.Encode(buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	})
}

func testBMP(t *testing.T) []byte {
	t.Helper()
	return encodedRaster(t, func(buf *bytes.Buffer) error {
		return bmp.Encode(buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	})
}

func testGIF(t *testing.T) []byte {
	t.Helper()
	return encodedRaster(t, func(buf *bytes.Buffer) error {
		return gif.Encode(buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	})
}

func requirePNGInlineData(t *testing.T, inline map[string]any) {
	t.Helper()
	if inline["mimeType"] != "image/png" {
		t.Fatalf("inlineData mimeType = %#v, want image/png", inline["mimeType"])
	}
	data, err := base64.StdEncoding.DecodeString(inline["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || format != "png" {
		t.Fatalf("inlineData bytes decode as (%q, %v), want png", format, err)
	}
}

// TestBuildBodyNormalizesUnsupportedInlineImageMediaType pins that Gemini's
// inline image parts never carry a raster media type the API rejects. BMP and
// GIF are accepted by the tool layer and by other providers, but Gemini's
// inline set is png/jpeg/webp/heic/heif, so the builder must re-encode them as
// PNG instead of writing the claimed type verbatim (#4584).
func TestBuildBodyNormalizesUnsupportedInlineImageMediaType(t *testing.T) {
	for name, tc := range map[string]struct {
		data      []byte
		mediaType string
	}{
		"bmp": {data: testBMP(t), mediaType: "image/bmp"},
		"gif": {data: testGIF(t), mediaType: "image/gif"},
	} {
		t.Run(name, func(t *testing.T) {
			req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: tc.data, MediaType: tc.mediaType}}}},
			}}
			body := geminiBuild(t, req, protoRes(nil))
			inline := body["contents"].([]map[string]any)[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
			requirePNGInlineData(t, inline)
		})
	}
}

// TestBuildBodyKeepsAcceptedInlineImageMediaType pins that a media type Gemini
// accepts still rides through byte-for-byte, so normalization cannot re-encode
// every image on the hot path. HEIC and HEIF are accepted by the wire but not
// readable by the local decoder, so the pass-through is their only path.
func TestBuildBodyKeepsAcceptedInlineImageMediaType(t *testing.T) {
	for _, tc := range []struct {
		name    string
		claimed string
		want    string
	}{
		{name: "png", claimed: "image/png", want: "image/png"},
		{name: "jpeg", claimed: "image/jpeg", want: "image/jpeg"},
		{name: "webp", claimed: "image/webp", want: "image/webp"},
		{name: "heic", claimed: "image/heic", want: "image/heic"},
		{name: "heif", claimed: "image/heif", want: "image/heif"},
		{name: "heic format name", claimed: "heic", want: "image/heic"},
		{name: "heif format name", claimed: "heif", want: "image/heif"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("claimed " + tc.name)
			req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: data, MediaType: tc.claimed}}}},
			}}
			inline := geminiBuild(t, req, protoRes(nil))["contents"].([]map[string]any)[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
			if inline["mimeType"] != tc.want || inline["data"] != base64.StdEncoding.EncodeToString(data) {
				t.Fatalf("accepted inline image changed: %#v", inline)
			}
		})
	}
}

// TestBuildBodyDerivesInlineMediaTypeFromBytes pins the unclaimed case: a
// client that sends no media type must still get the bytes' real type, not a
// fabricated image/png that bypasses normalization (#4584).
func TestBuildBodyDerivesInlineMediaTypeFromBytes(t *testing.T) {
	for name, tc := range map[string]struct {
		data      []byte
		unchanged bool
	}{
		"png": {data: testPNG(t), unchanged: true},
		"bmp": {data: testBMP(t)},
		"gif": {data: testGIF(t)},
	} {
		t.Run(name, func(t *testing.T) {
			req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: tc.data}}}},
			}}
			inline := geminiBuild(t, req, protoRes(nil))["contents"].([]map[string]any)[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
			requirePNGInlineData(t, inline)
			if tc.unchanged && inline["data"] != base64.StdEncoding.EncodeToString(tc.data) {
				t.Fatalf("an unclaimed png must keep its bytes: %#v", inline)
			}
		})
	}
}

// TestBuildBodyNormalizesUnsupportedToolResultImageMediaType covers the
// tool-result arm: a BMP read result on a multimodal Gemini row must re-encode
// to PNG inside the functionResponse instead of carrying image/bmp.
func TestBuildBodyNormalizesUnsupportedToolResultImageMediaType(t *testing.T) {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shot", Arguments: []byte(`{}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shot", Content: "ok", ImageData: testBMP(t), ImageMediaType: "image/bmp"}}}},
	}}
	body := protoBuild(t, req, protoRes(func(c *registry.Caps) {
		c.InputModalities = []string{"text", "image"}
		c.MultimodalToolResults = new(true)
	}))
	parts := body["contents"].([]map[string]any)[1]["parts"].([]map[string]any)
	fr := parts[0]["functionResponse"].(map[string]any)
	inline := fr["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
	requirePNGInlineData(t, inline)
}

// TestBuildBodyRefusesUndecodableClaimedRaster pins the loud refusal: a claimed
// unsupported raster whose bytes are not decodable must fail the build as a
// configuration error, not reach the provider for an opaque 400.
func TestBuildBodyRefusesUndecodableClaimedRaster(t *testing.T) {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("not an image"), MediaType: "image/bmp"}}}},
	}}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("undecodable claimed raster error = %T %v, want *llm.ConfigurationError", err, err)
	}
}
