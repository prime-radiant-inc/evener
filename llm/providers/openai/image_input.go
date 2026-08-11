package openai

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

func normalizeImageInput(data []byte, claimedMediaType string) ([]byte, string, error) {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("invalid OpenAI image input %q: %w", claimedMediaType, err)
	}

	if mediaType := openAIImageMediaType(format); mediaType != "" {
		return data, mediaType, nil
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode OpenAI image input %q: %w", claimedMediaType, err)
	}
	var normalized bytes.Buffer
	if err := png.Encode(&normalized, decoded); err != nil {
		return nil, "", fmt.Errorf("encode OpenAI image input as PNG: %w", err)
	}
	return normalized.Bytes(), "image/png", nil
}

func openAIImageMediaType(format string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}
