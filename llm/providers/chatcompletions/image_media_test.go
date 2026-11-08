package chatcompletions

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/bmp"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func chatImageMessageWith(data []byte, mediaType string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "look"},
		{Kind: llm.ContentImage, Image: &llm.ImageData{Data: data, MediaType: mediaType}},
	}}
}

func testBMP(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageURLFromChatParts(t *testing.T, parts []map[string]any) string {
	t.Helper()
	if len(parts) != 2 {
		t.Fatalf("content parts = %#v, want text and image", parts)
	}
	return parts[1]["image_url"].(map[string]any)["url"].(string)
}

// TestToChatMessagesNormalizesUnsupportedImageMediaType pins that a user image
// whose claimed type the Chat Completions image block rejects (BMP) rides as a
// PNG data URI, never as image/bmp (#4584).
func TestToChatMessagesNormalizesUnsupportedImageMediaType(t *testing.T) {
	out, err := toChatMessages([]llm.Message{chatImageMessageWith(testBMP(t), "image/bmp")}, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	url := imageURLFromChatParts(t, out[0]["content"].([]map[string]any))
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image URL = %q, want a PNG data URI", url)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil || format != "png" {
		t.Fatalf("data URI bytes decode as (%q, %v), want png", format, err)
	}
}

// TestToChatMessagesKeepsAcceptedImageMediaType pins the byte-for-byte path for
// a media type the image block already accepts.
func TestToChatMessagesKeepsAcceptedImageMediaType(t *testing.T) {
	data := testPNG(t)
	out, err := toChatMessages([]llm.Message{chatImageMessageWith(data, "image/png")}, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if got := imageURLFromChatParts(t, out[0]["content"].([]map[string]any)); got != want {
		t.Fatalf("accepted image URL changed: %q, want %q", got, want)
	}
}

// TestToChatMessagesRefusesUndecodableClaimedRaster pins the loud refusal for
// bytes that cannot be normalized into an accepted media type.
func TestToChatMessagesRefusesUndecodableClaimedRaster(t *testing.T) {
	_, err := toChatMessages([]llm.Message{chatImageMessageWith([]byte("not an image"), "image/bmp")}, registry.Caps{}, false)
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("undecodable claimed raster error = %T %v, want *llm.ConfigurationError", err, err)
	}
}
