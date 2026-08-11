package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestToResponsesInputTranscodesBMPToolResultToPNG(t *testing.T) {
	imageURL := toolResultImageURL(t, "image/bmp", onePixelBMP())
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("image URL prefix = %q, want PNG data URI", imageURL[:min(len(imageURL), 32)])
	}

	encoded := strings.TrimPrefix(imageURL, "data:image/png;base64,")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode image data URI: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode transcoded PNG: %v", err)
	}
	if got := decoded.Bounds().Size(); got.X != 1 || got.Y != 1 {
		t.Fatalf("decoded size = %v, want 1x1", got)
	}
}

func TestToResponsesInputPreservesWebPToolResult(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("UklGRjAAAABXRUJQVlA4ICQAAABwAQCdASoBAAEAAgA0JYwCdAGIQAD++ZNsGW2xURhNJHYAAAA=")
	if err != nil {
		t.Fatalf("decode WebP fixture: %v", err)
	}

	if got := toolResultImageURL(t, "image/webp", data); got != llm.DataURI("image/webp", data) {
		t.Fatalf("WebP image URL was changed: %q", got)
	}
}

func TestNormalizeImageInputRejectsTruncatedNativeImage(t *testing.T) {
	valid := encodeImageInputPNG(t)
	const pngHeaderLength = 33
	truncated := valid[:pngHeaderLength]
	if _, _, err := image.DecodeConfig(bytes.NewReader(truncated)); err != nil {
		t.Fatalf("fixture must have a decodable PNG header: %v", err)
	}

	if _, _, err := normalizeImageInput(truncated, "image/png"); err == nil {
		t.Fatal("normalizeImageInput accepted truncated PNG pixel data")
	}
}

func TestToResponsesInputTranscodesTIFFToolResultToPNG(t *testing.T) {
	assertPNGDataURI(t, toolResultImageURL(t, "image/tiff", onePixelTIFF()))
}

func TestToResponsesInputTranscodesAnimatedGIFToolResultToPNG(t *testing.T) {
	var data bytes.Buffer
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.SetColorIndex(0, 0, 1)
	if err := gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{1, 1}}); err != nil {
		t.Fatalf("encode animated GIF fixture: %v", err)
	}

	assertPNGDataURI(t, toolResultImageURL(t, "image/gif", data.Bytes()))
}

func TestToResponsesInputKeepsPDFToolResultTextOnly(t *testing.T) {
	messages := []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID:     "call_pdf",
			Content:        "document content was extracted separately",
			ImageData:      []byte("%PDF-1.7"),
			ImageMediaType: "application/pdf",
		},
	}}}}

	_, items, err := toResponsesInput(messages, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one tool result", items)
	}
	item, _ := items[0].(map[string]any)
	if got := item["output"]; got != "document content was extracted separately" {
		t.Fatalf("tool output = %#v, want text-only content", got)
	}
}

func TestOpenAIRequestBuildersNormalizeByteBackedBMP(t *testing.T) {
	imagePart := llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{
		Data:      onePixelBMP(),
		MediaType: "image/bmp",
	}}

	t.Run("Responses", func(t *testing.T) {
		body, err := (&Adapter{}).buildRequestBody(llm.Request{
			Model:    "gpt-5.6-luna",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{imagePart}}},
		})
		if err != nil {
			t.Fatalf("buildRequestBody: %v", err)
		}
		images := collectInputImages(t, body)
		if len(images) != 1 {
			t.Fatalf("input images = %#v, want one", images)
		}
		assertPNGDataURI(t, images[0]["image_url"])
	})

	t.Run("ChatCompletions", func(t *testing.T) {
		parts, err := buildChatMultimodalParts([]llm.ContentPart{imagePart})
		if err != nil {
			t.Fatalf("buildChatMultimodalParts: %v", err)
		}
		if len(parts) != 1 {
			t.Fatalf("parts = %#v, want one", parts)
		}
		imageURL, _ := parts[0]["image_url"].(map[string]any)
		assertPNGDataURI(t, imageURL["url"])
	})
}

func TestOpenAIRequestBuildersNormalizeLocalBMP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.bmp")
	if err := os.WriteFile(path, onePixelBMP(), 0o600); err != nil {
		t.Fatalf("write BMP fixture: %v", err)
	}
	imagePart := llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: path}}

	t.Run("Responses", func(t *testing.T) {
		body, err := (&Adapter{}).buildRequestBody(llm.Request{
			Model:    "gpt-5.6-luna",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{imagePart}}},
		})
		if err != nil {
			t.Fatalf("buildRequestBody: %v", err)
		}
		images := collectInputImages(t, body)
		if len(images) != 1 {
			t.Fatalf("input images = %#v, want one", images)
		}
		assertPNGDataURI(t, images[0]["image_url"])
	})

	t.Run("ChatCompletions", func(t *testing.T) {
		parts, err := buildChatMultimodalParts([]llm.ContentPart{imagePart})
		if err != nil {
			t.Fatalf("buildChatMultimodalParts: %v", err)
		}
		if len(parts) != 1 {
			t.Fatalf("parts = %#v, want one", parts)
		}
		imageURL, _ := parts[0]["image_url"].(map[string]any)
		assertPNGDataURI(t, imageURL["url"])
	})
}

func assertPNGDataURI(t *testing.T, value any) {
	t.Helper()
	url, _ := value.(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image URL = %q, want PNG data URI", url)
	}
}

func toolResultImageURL(t *testing.T, mediaType string, data []byte) string {
	t.Helper()
	messages := []llm.Message{
		llm.User("inspect the image"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: "call_image", Name: "read_file"},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_image", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_image",
				Content:        "image content below",
				ImageData:      data,
				ImageMediaType: mediaType,
			},
		}}},
	}

	_, items, err := toResponsesInput(messages, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		if item["type"] != "function_call_output" || item["call_id"] != "call_image" {
			continue
		}
		output, _ := item["output"].([]any)
		if len(output) != 2 {
			t.Fatalf("tool output = %#v, want text and image", item["output"])
		}
		image, _ := output[1].(map[string]any)
		imageURL, _ := image["image_url"].(string)
		if imageURL == "" {
			t.Fatalf("image output = %#v, want image URL", image)
		}
		return imageURL
	}
	t.Fatalf("no image-bearing tool result in %#v", items)
	return ""
}

func onePixelBMP() []byte {
	const (
		fileHeaderSize = 14
		dibHeaderSize  = 40
		pixelDataSize  = 4
	)
	data := make([]byte, fileHeaderSize+dibHeaderSize+pixelDataSize)
	copy(data, "BM")
	binary.LittleEndian.PutUint32(data[2:6], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[10:14], fileHeaderSize+dibHeaderSize)
	binary.LittleEndian.PutUint32(data[14:18], dibHeaderSize)
	binary.LittleEndian.PutUint32(data[18:22], 1)
	binary.LittleEndian.PutUint32(data[22:26], 1)
	binary.LittleEndian.PutUint16(data[26:28], 1)
	binary.LittleEndian.PutUint16(data[28:30], 24)
	binary.LittleEndian.PutUint32(data[34:38], pixelDataSize)
	copy(data[fileHeaderSize+dibHeaderSize:], []byte{0x30, 0x20, 0x10, 0})
	return data
}

func onePixelTIFF() []byte {
	const (
		entryCount          = 9
		ifdOffset           = 8
		bitsPerSampleOffset = ifdOffset + 2 + entryCount*12 + 4
		pixelOffset         = bitsPerSampleOffset + 6
	)
	data := make([]byte, pixelOffset+3)
	copy(data, "II")
	binary.LittleEndian.PutUint16(data[2:4], 42)
	binary.LittleEndian.PutUint32(data[4:8], ifdOffset)
	binary.LittleEndian.PutUint16(data[ifdOffset:ifdOffset+2], entryCount)

	entries := data[ifdOffset+2:]
	putEntry := func(index int, tag, fieldType uint16, count, value uint32) {
		entry := entries[index*12 : (index+1)*12]
		binary.LittleEndian.PutUint16(entry[0:2], tag)
		binary.LittleEndian.PutUint16(entry[2:4], fieldType)
		binary.LittleEndian.PutUint32(entry[4:8], count)
		binary.LittleEndian.PutUint32(entry[8:12], value)
	}
	putEntry(0, 256, 3, 1, 1)
	putEntry(1, 257, 3, 1, 1)
	putEntry(2, 258, 3, 3, bitsPerSampleOffset)
	putEntry(3, 259, 3, 1, 1)
	putEntry(4, 262, 3, 1, 2)
	putEntry(5, 273, 4, 1, pixelOffset)
	putEntry(6, 277, 3, 1, 3)
	putEntry(7, 278, 4, 1, 1)
	putEntry(8, 279, 4, 1, 3)
	binary.LittleEndian.PutUint16(data[bitsPerSampleOffset:bitsPerSampleOffset+2], 8)
	binary.LittleEndian.PutUint16(data[bitsPerSampleOffset+2:bitsPerSampleOffset+4], 8)
	binary.LittleEndian.PutUint16(data[bitsPerSampleOffset+4:bitsPerSampleOffset+6], 8)
	copy(data[pixelOffset:], []byte{0x10, 0x20, 0x30})
	return data
}

func encodeImageInputPNG(t testing.TB) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return data.Bytes()
}
