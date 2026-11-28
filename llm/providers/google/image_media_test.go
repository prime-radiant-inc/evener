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

func requirePNGInteractionImage(t *testing.T, img map[string]any) {
	t.Helper()
	if img["type"] != "image" {
		t.Fatalf("item type = %#v, want image", img["type"])
	}
	if img["mime_type"] != "image/png" {
		t.Fatalf("image mime_type = %#v, want image/png", img["mime_type"])
	}
	data, err := base64.StdEncoding.DecodeString(img["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || format != "png" {
		t.Fatalf("image bytes decode as (%q, %v), want png", format, err)
	}
}

func TestBuildBodyNormalizesUnsupportedInlineImageMediaType(t *testing.T) {
	t.Parallel()
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
			img := body["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			requirePNGInteractionImage(t, img)
		})
	}
}

func TestBuildBodyKeepsAcceptedInlineImageMediaType(t *testing.T) {
	t.Parallel()
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
			img := geminiBuild(t, req, protoRes(nil))["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			if img["mime_type"] != tc.want || img["data"] != base64.StdEncoding.EncodeToString(data) {
				t.Fatalf("accepted inline image changed: %#v", img)
			}
		})
	}
}

func TestBuildBodyDerivesInlineMediaTypeFromBytes(t *testing.T) {
	t.Parallel()
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
			img := geminiBuild(t, req, protoRes(nil))["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
			requirePNGInteractionImage(t, img)
			if tc.unchanged && img["data"] != base64.StdEncoding.EncodeToString(tc.data) {
				t.Fatalf("an unclaimed png must keep its bytes: %#v", img)
			}
		})
	}
}

func TestBuildBodyNormalizesUnsupportedToolResultImageMediaType(t *testing.T) {
	t.Parallel()
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shot", Arguments: []byte(`{}`)}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "shot", Content: "ok", ImageData: testBMP(t), ImageMediaType: "image/bmp"}}}},
	}}
	body := protoBuild(t, req, protoRes(func(c *registry.Caps) {
		c.InputModalities = []string{"text", "image"}
		c.MultimodalToolResults = new(true)
	}))
	fr := body["input"].([]any)[1].(map[string]any)
	img := fr["result"].([]map[string]any)[1]
	requirePNGInteractionImage(t, img)
}

func TestBuildBodyRefusesUndecodableClaimedRaster(t *testing.T) {
	t.Parallel()
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("not an image"), MediaType: "image/bmp"}}}},
	}}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("undecodable claimed raster error = %T %v, want *llm.ConfigurationError", err, err)
	}
}
