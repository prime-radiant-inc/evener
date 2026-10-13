package anthropic

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"

	"primeradiant.com/evener/llm"
)

// webpFixture is a 1x1 WebP (the same bytes the tool layer's raster test uses).
const webpFixture = "UklGRjAAAABXRUJQVlA4ICQAAABwAQCdASoBAAEAAgA0JYwCdAGIQAD++ZNsGW2xURhNJHYAAAA="

// rasterFixture encodes a 2x2 raster in the named format.
func rasterFixture(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xff})
	img.Set(1, 1, color.RGBA{R: 0x44, G: 0x55, B: 0x66, A: 0xff})
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	case "bmp":
		err = bmp.Encode(&buf, img)
	case "tiff":
		err = tiff.Encode(&buf, img, nil)
	default:
		t.Fatalf("unsupported raster fixture format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s fixture: %v", format, err)
	}
	return buf.Bytes()
}

func webpBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(webpFixture)
	if err != nil {
		t.Fatalf("decode webp fixture: %v", err)
	}
	return data
}

// imageSources returns the base64 source of every image block in the emitted
// messages, walking both plain image blocks and image blocks nested in a
// tool_result's content array.
func imageSources(t *testing.T, msgs []map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range msgs {
		blocks, _ := m["content"].([]map[string]any)
		for _, b := range blocks {
			switch b["type"] {
			case "image":
				src, _ := b["source"].(map[string]any)
				out = append(out, src)
			case "tool_result":
				content, _ := b["content"].([]map[string]any)
				for _, c := range content {
					if c["type"] == "image" {
						src, _ := c["source"].(map[string]any)
						out = append(out, src)
					}
				}
			}
		}
	}
	return out
}

func decodeSourceData(t *testing.T, src map[string]any) []byte {
	t.Helper()
	encoded, _ := src["data"].(string)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("source data is not base64: %v", err)
	}
	return data
}

func userImageMessage(data []byte, mediaType string) []llm.Message {
	return []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: data, MediaType: mediaType}},
	}}}
}

// A user image supplied as a local path is read and normalized the same way:
// the path's .bmp extension is not a media type Anthropic's image block
// accepts.
func TestToAnthropicMessages_LocalPathBitmapIsConverted(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "scan.bmp")
	if err := os.WriteFile(path, rasterFixture(t, "bmp"), 0o600); err != nil {
		t.Fatalf("write bmp fixture: %v", err)
	}
	_, msgs, err := toAnthropicMessages([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: path}},
	}}})
	if err != nil {
		t.Fatalf("local-path bmp: %v", err)
	}
	sources := imageSources(t, msgs)
	if len(sources) != 1 {
		t.Fatalf("image blocks = %d, want 1", len(sources))
	}
	if mt, _ := sources[0]["media_type"].(string); mt != "image/png" {
		t.Fatalf("media_type = %q, want image/png", mt)
	}
	_, format, err := image.Decode(bytes.NewReader(decodeSourceData(t, sources[0])))
	if err != nil || format != "png" {
		t.Fatalf("converted bytes decode as (%q, %v), want png", format, err)
	}
}

func toolResultMessage(data []byte, mediaType string) []llm.Message {
	return []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID:     "call_1",
			Name:           "read_file",
			Content:        "read image",
			ImageData:      data,
			ImageMediaType: mediaType,
		}},
	}}}
}

// A user-message image whose media type Anthropic rejects (image/bmp,
// image/tiff) must reach the wire re-encoded as a media type Anthropic's image
// block accepts. Before #4464 the builder wrote image/bmp verbatim and the API
// rejected the whole turn.
func TestToAnthropicMessages_UserRasterConvertedToAcceptedMediaType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, format, claimed string }{
		{name: "BMP", format: "bmp", claimed: "image/bmp"},
		{name: "TIFF", format: "tiff", claimed: "image/tiff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := rasterFixture(t, tc.format)
			_, msgs, err := toAnthropicMessages(userImageMessage(data, tc.claimed))
			if err != nil {
				t.Fatalf("user %s image: %v", tc.name, err)
			}
			sources := imageSources(t, msgs)
			if len(sources) != 1 {
				t.Fatalf("image blocks = %d, want 1", len(sources))
			}
			if mt, _ := sources[0]["media_type"].(string); mt != "image/png" {
				t.Fatalf("media_type = %q, want image/png", mt)
			}
			_, format, err := image.Decode(bytes.NewReader(decodeSourceData(t, sources[0])))
			if err != nil || format != "png" {
				t.Fatalf("converted bytes decode as (%q, %v), want png", format, err)
			}
		})
	}
}

// A tool-result image whose media type Anthropic rejects must also be
// re-encoded before it rides the tool_result's image block.
func TestToAnthropicMessages_ToolResultRasterConvertedToAcceptedMediaType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, format, claimed string }{
		{name: "BMP", format: "bmp", claimed: "image/bmp"},
		{name: "TIFF", format: "tiff", claimed: "image/tiff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := rasterFixture(t, tc.format)
			_, msgs, err := toAnthropicMessages(toolResultMessage(data, tc.claimed))
			if err != nil {
				t.Fatalf("tool-result %s image: %v", tc.name, err)
			}
			sources := imageSources(t, msgs)
			if len(sources) != 1 {
				t.Fatalf("image blocks = %d, want 1", len(sources))
			}
			if mt, _ := sources[0]["media_type"].(string); mt != "image/png" {
				t.Fatalf("media_type = %q, want image/png", mt)
			}
			_, format, err := image.Decode(bytes.NewReader(decodeSourceData(t, sources[0])))
			if err != nil || format != "png" {
				t.Fatalf("converted bytes decode as (%q, %v), want png", format, err)
			}
		})
	}
}

// A raster already in Anthropic's accepted set rides unchanged: the same bytes
// and the same media type. This is the non-regression guard for #4464.
func TestToAnthropicMessages_SupportedRastersRideUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, format, mediaType string }{
		{name: "PNG", format: "png", mediaType: "image/png"},
		{name: "JPEG", format: "jpeg", mediaType: "image/jpeg"},
		{name: "GIF", format: "gif", mediaType: "image/gif"},
		{name: "WebP", mediaType: "image/webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var data []byte
			if tc.format == "" {
				data = webpBytes(t)
			} else {
				data = rasterFixture(t, tc.format)
			}
			for _, kind := range []string{"user", "tool"} {
				messages := userImageMessage(data, tc.mediaType)
				if kind == "tool" {
					messages = toolResultMessage(data, tc.mediaType)
				}
				_, msgs, err := toAnthropicMessages(messages)
				if err != nil {
					t.Fatalf("%s %s: %v", kind, tc.name, err)
				}
				sources := imageSources(t, msgs)
				if len(sources) != 1 {
					t.Fatalf("%s %s: image blocks = %d, want 1", kind, tc.name, len(sources))
				}
				if mt, _ := sources[0]["media_type"].(string); mt != tc.mediaType {
					t.Fatalf("%s %s: media_type = %q, want %q", kind, tc.name, mt, tc.mediaType)
				}
				if got := decodeSourceData(t, sources[0]); !bytes.Equal(got, data) {
					t.Fatalf("%s %s: bytes changed, got %d want %d", kind, tc.name, len(got), len(data))
				}
			}
		})
	}
}

// A non-image media tool result contributes its text alone, exactly as before
// (#4209): an application/pdf result must not become an image block.
func TestToAnthropicMessages_ToolResultNonImageMediaStaysTextOnly(t *testing.T) {
	t.Parallel()
	_, msgs, err := toAnthropicMessages(toolResultMessage([]byte("%PDF-1.4"), "application/pdf"))
	if err != nil {
		t.Fatalf("pdf tool result: %v", err)
	}
	if got := len(imageSources(t, msgs)); got != 0 {
		t.Fatalf("image blocks = %d, want 0", got)
	}
	content, ok := msgs[0]["content"].([]map[string]any)[0]["content"].(string)
	if !ok || content == "" {
		t.Fatalf("tool_result content = %#v, want the text alone", msgs[0]["content"])
	}
}

// Bytes that are not a decodable raster are refused loudly at the builder: a
// configuration error naming the media type and the accepted formats, not an
// opaque provider 400.
func TestBuildBody_UndecodableImageIsConfigurationError(t *testing.T) {
	t.Parallel()
	res := protoRes(nil)
	req := protoReq("")
	req.Messages = append(req.Messages, userImageMessage([]byte("not a raster"), "image/bmp")...)
	body, err := (&Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err == nil {
		t.Fatal("BuildBody accepted undecodable image bytes")
	}
	if body != nil {
		t.Fatalf("BuildBody returned a body alongside the refusal: %#v", body)
	}
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("err = %T %v, want *llm.ConfigurationError", err, err)
	}
	if !errors.Is(err, image.ErrFormat) {
		t.Fatalf("err = %v, want the decoder's error preserved as the cause", err)
	}
}
